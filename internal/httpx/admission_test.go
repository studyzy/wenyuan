package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// A lane's own wait applies to that lane: a retain is shed sooner than a
// recall, which is the reference's per-lane sizing. The first request holds the
// lane while the second waits, so the second is shed when its own wait elapses
// rather than the global one.
func TestAdmissionRuleOwnWaitOverridesTheGlobal(t *testing.T) {
	cfg := AdmissionConfig{
		MaxWait: 5 * time.Second,
		Rules: []AdmissionRule{
			{Method: http.MethodPost, Suffix: "/memories/recall", Lane: "recall", Limit: 1,
				MaxWait: 20 * time.Millisecond},
		},
	}
	started := make(chan struct{})
	handler := AdmissionMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	first := httptest.NewRecorder()
	go handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b/memories/recall", nil))
	<-started // the first request now holds the lane

	second := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b/memories/recall", nil))
	elapsed := time.Since(start)

	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 once the lane's wait elapsed", second.Code)
	}
	if elapsed > time.Second {
		t.Fatalf("the lane's own wait was not applied: waited %s", elapsed)
	}
}

// A lane without its own wait keeps the global one.
func TestAdmissionFallsBackToTheGlobalWait(t *testing.T) {
	cfg := AdmissionConfig{
		MaxWait: 20 * time.Millisecond,
		Rules: []AdmissionRule{
			{Method: http.MethodPost, Suffix: "/memories/recall", Lane: "recall", Limit: 1},
		},
	}
	started := make(chan struct{})
	handler := AdmissionMiddleware(cfg)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		time.Sleep(150 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	first := httptest.NewRecorder()
	go handler.ServeHTTP(first, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b/memories/recall", nil))
	<-started

	second := httptest.NewRecorder()
	start := time.Now()
	handler.ServeHTTP(second, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b/memories/recall", nil))
	if second.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 once the global wait elapsed", second.Code)
	}
	if elapsed := time.Since(start); elapsed < 15*time.Millisecond {
		t.Fatalf("the global wait was not applied: shed after %s", elapsed)
	}
}
