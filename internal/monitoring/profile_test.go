package monitoring

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The profiling routes are registered on request and answer.
func TestBindProfileRoutes(t *testing.T) {
	mux := http.NewServeMux()
	BindProfileRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/debug/pprof/heap", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("pprof heap = %d, want 200", rec.Code)
	}
}
