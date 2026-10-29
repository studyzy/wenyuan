package readsurface

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// graphDisconnectStore is a store whose graph read the test holds open until the
// request's context is cancelled, then reports the context's own error — what a
// store that honours the cancellation returns.
type graphDisconnectStore struct {
	storage.Store
	started chan struct{}
	release chan struct{}
}

func (s *graphDisconnectStore) Graph(ctx context.Context, query storage.GraphQuery) (storage.GraphData, error) {
	close(s.started)
	<-s.release
	return storage.GraphData{}, ctx.Err()
}

// The graph read is one of the reads the reference cancels when the client
// leaves, answered 499 with its disconnect reason rather than a server fault
// (api/http.py:5965-5987).
func TestGraphDisconnectAnswers499(t *testing.T) {
	store := storage.NewMemoryStore("public")
	seedFixture(t, store)
	inner := &graphDisconnectStore{Store: store, started: make(chan struct{}), release: make(chan struct{})}
	mux := http.NewServeMux()
	NewHandlers(inner).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			rest := strings.TrimPrefix(r.URL.Path, "/v1/default/banks/")
			if bankID, _, ok := strings.Cut(rest, "/"); ok {
				r.SetPathValue("bank_id", bankID)
			}
			handler(w, r)
		})
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, "/v1/default/banks/"+bank+"/graph", nil)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		defer close(done)
		mux.ServeHTTP(rec, req)
	}()
	<-inner.started
	cancel()
	close(inner.release)
	<-done

	if rec.Code != httpx.KindClientClosed {
		t.Fatalf("status = %d, want 499: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"detail":"client disconnected"}` {
		t.Fatalf("body = %s, want the disconnect reason", got)
	}
}
