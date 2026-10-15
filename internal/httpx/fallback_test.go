package httpx

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// An unmatched path and an unmatched method answer the reference's JSON
// envelope, not the router's plain text.
func TestRouteFallbackEnvelope(t *testing.T) {
	router := NewRouter(RouterOptions{})
	router.Handle(http.MethodGet, "/known", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// A handler's own not-found must be preserved as the handler wrote it.
	router.Handle(http.MethodPost, "/handled", func(w http.ResponseWriter, _ *http.Request) {
		WriteErrorFor(w, httptest.NewRequest(http.MethodPost, "/handled", nil),
			NewHTTPError(KindNotFound, "no such thing"))
	})
	service := router.Service()

	cases := []struct {
		method string
		path   string
		status int
		detail string
	}{
		{http.MethodGet, "/absent", http.StatusNotFound, DetailNotFound},
		{http.MethodDelete, "/known", http.StatusMethodNotAllowed, DetailMethodNotAllowed},
		{http.MethodPost, "/handled", http.StatusNotFound, "no such thing"},
		{http.MethodGet, "/known", http.StatusOK, ""},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		service.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		if rec.Code != tc.status {
			t.Fatalf("%s %s: status = %d, want %d", tc.method, tc.path, rec.Code, tc.status)
		}
		if tc.detail == "" {
			continue
		}
		if got := rec.Header().Get("Content-Type"); got != "application/json" {
			t.Fatalf("%s %s: content type = %q", tc.method, tc.path, got)
		}
		var body struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s %s: body %q is not JSON: %v", tc.method, tc.path, rec.Body.String(), err)
		}
		if body.Detail != tc.detail {
			t.Fatalf("%s %s: detail = %q, want %q", tc.method, tc.path, body.Detail, tc.detail)
		}
	}
	// The method rejection keeps the router's Allow header.
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/known", nil))
	if allow := rec.Header().Get("Allow"); allow == "" {
		t.Fatal("the method rejection must keep the Allow header")
	}
}
