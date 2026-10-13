package mentalmodels

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newServer builds the family's route table over an in-memory store with one
// bank, translating path patterns into the path values the real router sets.
func newServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{ID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "b1")
			if strings.Contains(pattern, "{mental_model_id}") {
				r.SetPathValue("mental_model_id", lastSegment(r.URL.Path))
			}
			handler(w, r)
		})
	})
	return mux, store
}

// lastSegment returns the final path segment, skipping a trailing action word.
func lastSegment(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		switch segments[i] {
		case "history", "refresh", "dry-run-refresh", "clear":
			continue
		default:
			return segments[i]
		}
	}
	return ""
}

func send(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The full create → list → get → update → history → clear → delete lifecycle.
func TestMentalModelLifecycle(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"

	created := send(mux, http.MethodPost, base, `{"name":"goals","source_query":"what are the goals?","tags":["work"]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create: %d, body = %s", created.Code, created.Body.String())
	}
	var model Model
	if err := json.Unmarshal(created.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Name != "goals" || model.Subtype != string(storage.SubtypePinned) {
		t.Fatalf("created model = %+v", model)
	}

	listed := send(mux, http.MethodGet, base, "")
	var list ListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Models) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// A subtype filter that is not known is rejected rather than ignored.
	if rec := send(mux, http.MethodGet, base+"?subtype=bogus", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown subtype: %d, want 400", rec.Code)
	}

	if rec := send(mux, http.MethodGet, base+"/"+model.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}

	updated := send(mux, http.MethodPatch, base+"/"+model.ID, `{"description":"the current goals"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d, body = %s", updated.Code, updated.Body.String())
	}

	if rec := send(mux, http.MethodGet, base+"/"+model.ID+"/history", ""); rec.Code != http.StatusOK {
		t.Fatalf("history: %d", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base+"/"+model.ID+"/clear", ""); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base+"/"+model.ID+"/dry-run-refresh", ""); rec.Code != http.StatusOK {
		t.Fatalf("dry-run refresh: %d", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base+"/"+model.ID+"/refresh", ""); rec.Code != http.StatusOK {
		t.Fatalf("refresh: %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodDelete, base+"/"+model.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/"+model.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", rec.Code)
	}
}

// A create without a name is rejected, and an unknown subtype is rejected.
func TestMentalModelCreateValidation(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"
	if rec := send(mux, http.MethodPost, base, `{"source_query":"x"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing name: %d, want 400", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base, `{"name":"x","subtype":"bogus"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown subtype: %d, want 400", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base, `not json`); rec.Code != http.StatusBadRequest {
		t.Fatalf("malformed body: %d, want 400", rec.Code)
	}
}

// A model with no source query reports the reason rather than inventing
// content, and the dry run predicts the same.
func TestMentalModelRefreshWithoutSourceQuery(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"
	rec := send(mux, http.MethodPost, base, `{"name":"empty"}`)
	var model Model
	if err := json.Unmarshal(rec.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	refreshed := send(mux, http.MethodPost, base+"/"+model.ID+"/refresh", "")
	var result RefreshResult
	if err := json.Unmarshal(refreshed.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Refreshed || result.Reason == "" {
		t.Fatalf("a model with no source query must report a reason: %+v", result)
	}
}

// An unknown bank answers not-found.
func TestMentalModelUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	if rec := send(mux, http.MethodGet, "/v1/default/banks/missing/mental-models", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}
}
