package memoryops

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// A client that goes away mid-run cancels the request's context; the operations
// the reference monitors stop their work and answer 499 with the reference's
// disconnect reason rather than a server fault (api/http.py:470-476). The
// harness holds the store's long-running call open until the test has cancelled
// the request's context, which is what a real disconnect does while the work is
// in flight.

// startedRelease is the handshake the disconnect wrappers share.
type startedRelease struct {
	started chan struct{}
	release chan struct{}
}

func (s *startedRelease) hold() {
	close(s.started)
	<-s.release
}

// recallDisconnectStore is a store whose recall the test holds open.
type recallDisconnectStore struct {
	storage.Store
	startedRelease
}

func (s *recallDisconnectStore) Recall(ctx context.Context, q storage.RecallQuery) (storage.RecallResult, error) {
	s.hold()
	return storage.RecallResult{}, ctx.Err()
}

// retainDisconnectStore is a store whose write the test holds open.
type retainDisconnectStore struct {
	storage.Store
	startedRelease
}

func (s *retainDisconnectStore) RetainWrite(ctx context.Context, req storage.RetainRequest) (storage.RetainOutcome, error) {
	s.hold()
	return storage.RetainOutcome{}, ctx.Err()
}

// disconnectRequest posts body with a cancellable request context and cancels
// it once the store reports the operation in flight, then returns the response.
func disconnectRequest(t *testing.T, mux *http.ServeMux, method, path, body string, inFlight chan struct{}, release chan struct{}) *httptest.ResponseRecorder {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(rec, req)
	}()
	<-inFlight
	cancel()
	close(release)
	<-done
	return rec
}

// A recall abandoned mid-run answers 499, not the 500 its cancelled store error
// maps to.
func TestRecallDisconnectAnswers499(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	inner := &recallDisconnectStore{Store: store, startedRelease: startedRelease{
		started: make(chan struct{}), release: make(chan struct{}),
	}}
	bound := bindHandlers(t, NewHandlers(inner, 5, 30000))

	rec := disconnectRequest(t, bound, http.MethodPost, "/v1/default/banks/b1/memories/recall",
		`{"query":"q"}`, inner.started, inner.release)
	if rec.Code != httpx.KindClientClosed {
		t.Fatalf("status = %d, want 499: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"detail":"client disconnected"}` {
		t.Fatalf("body = %s, want the disconnect reason", got)
	}
}

// A synchronous retain abandoned mid-run answers 499 the same way.
func TestRetainDisconnectAnswers499(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	inner := &retainDisconnectStore{Store: store, startedRelease: startedRelease{
		started: make(chan struct{}), release: make(chan struct{}),
	}}
	bound := bindHandlers(t, NewHandlers(inner, 5, 30000))

	rec := disconnectRequest(t, bound, http.MethodPost, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"hello world"}],"async":false}`, inner.started, inner.release)
	if rec.Code != httpx.KindClientClosed {
		t.Fatalf("status = %d, want 499: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"detail":"client disconnected"}` {
		t.Fatalf("body = %s, want the disconnect reason", got)
	}
}
