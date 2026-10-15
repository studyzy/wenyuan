package httpx

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The access log writes one line per request in the reference's shape, and a
// caller-caused rejection is not an error line.
func TestAccessLogLineShape(t *testing.T) {
	var out bytes.Buffer
	router := NewRouter(RouterOptions{AccessLog: &out})
	router.Handle(http.MethodGet, "/known", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/known?a=1", nil))
	line := out.String()
	if !strings.HasPrefix(line, "INFO:     ") {
		t.Fatalf("line = %q, want the reference's level prefix", line)
	}
	if !strings.Contains(line, `"GET /known?a=1 HTTP/1.1" 200`) {
		t.Fatalf("line = %q, want the request line and status", line)
	}
	if strings.Count(line, "\n") != 1 {
		t.Fatalf("one request must write exactly one line: %q", line)
	}

	out.Reset()
	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/absent", nil))
	if !strings.HasPrefix(out.String(), "INFO:     ") || !strings.Contains(out.String(), " 404") {
		t.Fatalf("a not-found is information, not an error: %q", out.String())
	}
}

// A server fault is logged at error level; without a writer nothing is written.
func TestAccessLogLevelsAndGate(t *testing.T) {
	var out bytes.Buffer
	router := NewRouter(RouterOptions{AccessLog: &out})
	router.Handle(http.MethodGet, "/boom", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	router.Service().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/boom", nil))
	if !strings.HasPrefix(out.String(), "ERROR:    ") {
		t.Fatalf("a 500 must log at error level: %q", out.String())
	}

	quiet := NewRouter(RouterOptions{})
	quiet.Handle(http.MethodGet, "/known", func(w http.ResponseWriter, _ *http.Request) {})
	// No writer means no access log: the default a deployment gets.
	quiet.Service().ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/known", nil))
}
