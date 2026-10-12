package monitoring

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// ts-0004: the response carries the Prometheus text exposition content type
// and a parseable metrics body.
func TestMetricsContentTypeAndBody(t *testing.T) {
	var requests atomic.Int64
	requests.Store(7)
	h := &MetricsHandler{Requests: &requests}
	mux := http.NewServeMux()
	BindMetricsRoutes(h, func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathMetrics, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != PrometheusContentType {
		t.Fatalf("content type = %q, want %q", got, PrometheusContentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "# TYPE wenyuan_http_requests_total counter") {
		t.Fatalf("missing counter type line: %s", body)
	}
	if !strings.Contains(body, "wenyuan_http_requests_total 7") {
		t.Fatalf("missing counter sample: %s", body)
	}
}

func TestMetricsWithoutCountersIsStillValid(t *testing.T) {
	h := &MetricsHandler{}
	mux := http.NewServeMux()
	BindMetricsRoutes(h, func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathMetrics, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != PrometheusContentType {
		t.Fatalf("content type = %q", got)
	}
}

func TestMetricsExtraSamplesAreSorted(t *testing.T) {
	h := &MetricsHandler{Extra: func() map[string]float64 {
		return map[string]float64{"wenyuan_z_gauge": 1, "wenyuan_a_gauge": 2}
	}}
	rec := httptest.NewRecorder()
	h.Serve(rec, httptest.NewRequest(http.MethodGet, httpx.PathMetrics, nil))
	body := rec.Body.String()
	if strings.Index(body, "wenyuan_a_gauge") > strings.Index(body, "wenyuan_z_gauge") {
		t.Fatalf("extra metrics must be emitted in deterministic sorted order: %s", body)
	}
}
