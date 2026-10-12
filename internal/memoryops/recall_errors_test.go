package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// err-0006: a reranker floor combined with a rank-position reranker is a 400.
func TestRecallRerankerFloorVsRankPositionReranker(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{ID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.RerankerScoresByRankPosition = true
	bound := bindHandlers(t, h)

	rec := post(t, bound, "/v1/default/banks/b1/memories/recall", `{"query":"q","min_scores":{"reranker":0.5}}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400, body = %s", rec.Code, rec.Body.String())
	}
}

// Without the rank-position reranker, the same floor is accepted.
func TestRecallRerankerFloorAcceptedByDefaultReranker(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q","min_scores":{"reranker":0.5}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// err-0004: a malformed query-time anchor is a 400 with an explanatory message.
func TestRecallMalformedTimestampMessage(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q","query_timestamp":"13/40/2026"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	var envelope map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["detail"] == "" {
		t.Fatalf("bad-request must carry a message: %s", rec.Body.String())
	}
}

// err-0009: a shed request is a 503 carrying the retry-after header.
func TestRecallShedRetryAfterHeaderValue(t *testing.T) {
	mux, store := newServer(t)
	store.SetReadError(storage.ErrReadShed)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("retry-after must carry the store's suggested delay")
	}
}

// err-0010: an unhandled failure is a 500 carrying the message detail and no
// traceback.
func TestRecallInternalErrorDetail(t *testing.T) {
	mux, store := newServer(t)
	store.SetReadError(errSentinel)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	var envelope map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["detail"] == "" {
		t.Fatalf("internal error must carry a detail: %s", rec.Body.String())
	}
}

// bindHandlers wires a handler set onto a fresh mux for tests that need a
// custom handler configuration.
func bindHandlers(t *testing.T, h *Handlers) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}
