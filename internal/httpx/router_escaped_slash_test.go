package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// An escaped slash is a path separator to the reference's router.
//
// The reference matches its routes against the decoded path, so "%2F" splits a
// path into two segments and no single-segment parameter can capture it: every
// member route answers the framework's plain 404 for "/pages/a%2Fb". Go's mux
// matches on segments of the escaped path instead, so "%2F" stayed inside the
// captured value and reached the handler — a knowledge page read answered
// "Knowledge page 'a/b' not found" where the reference answers 404, and a
// patch route's UUID parse answered 500/400 where the reference answers 404.
func TestEscapedSlashIsAPathSeparator(t *testing.T) {
	router := NewRouter(RouterOptions{})
	var reached string
	router.Handle(http.MethodGet, "/v1/default/banks/{bank_id}/knowledge-base/pages/{page_id}",
		func(w http.ResponseWriter, r *http.Request) {
			reached = r.PathValue("page_id")
			w.WriteHeader(http.StatusOK)
		})

	rec := httptest.NewRecorder()
	router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/v1/default/banks/b1/knowledge-base/pages/a%2Fb", nil))
	if reached != "" {
		t.Fatalf("the escaped slash reached the handler as %q; it must split the path", reached)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (the path matches no route)", rec.Code)
	}
}

// An ordinary identifier still reaches its handler, and a percent-encoded
// character that is not a slash stays inside the captured value.
func TestUnexescapedAndOtherEscapesStillRoute(t *testing.T) {
	router := NewRouter(RouterOptions{})
	got := map[string]string{}
	router.Handle(http.MethodGet, "/v1/default/banks/{bank_id}/knowledge-base/pages/{page_id}",
		func(w http.ResponseWriter, r *http.Request) {
			got["bank"] = r.PathValue("bank_id")
			got["page"] = r.PathValue("page_id")
			w.WriteHeader(http.StatusOK)
		})
	for path, want := range map[string]string{
		"/v1/default/banks/b1/knowledge-base/pages/kp-123":    "kp-123",
		"/v1/default/banks/b1/knowledge-base/pages/a%20b":     "a b",
		"/v1/default/banks/b1/knowledge-base/pages/%E4%B8%AD": "中",
	} {
		rec := httptest.NewRecorder()
		router.Service().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", path, rec.Code)
		}
		if got["page"] != want {
			t.Fatalf("%s: page_id = %q, want %q", path, got["page"], want)
		}
	}
}
