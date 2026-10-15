package memoryops

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// A recall whose fuzzy `exact` scope expands past the cap is a 422 client
// error, not a 500.
func TestRecallOverTagCapIsClientError(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	// Each distinct tag is similar to the token below, so the fuzzy `exact`
	// leaf expands into more branches than MaxTagExactCombinations allows.
	for i := 0; i < storage.MaxTagExactCombinations+8; i++ {
		item := storage.RetainItem{Content: fmt.Sprintf("memory %d", i), Tags: []string{fmt.Sprintf("team:alpha%02d", i)}}
		if _, err := store.RetainWrite(context.TODO(), storage.RetainRequest{BankID: "b1", Items: []storage.RetainItem{item}}); err != nil {
			t.Fatal(err)
		}
	}

	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})

	body := `{"query":"q","tag_groups":[{"tag":"team:alpha","match":"exact","resolve":"fuzzy"}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (not 500); body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "above") {
		t.Fatalf("the message must carry the storage numbers: %s", rec.Body.String())
	}
	// Recall answers with the reference's message, not a bare sentinel string.
	if !strings.Contains(rec.Body.String(), "Narrow the tags, or use resolve='exact'.") {
		t.Fatalf("the message must be the reference's: %s", rec.Body.String())
	}
}

// Both tag-scope cap sentinels map to a client error (422) rather than 500.
func TestMapReadErrorTagCaps(t *testing.T) {
	h := NewHandlers(storage.NewMemoryStore("public"), 5, 30000)
	for _, sentinel := range []error{storage.ErrTagVocabularyTooLarge, storage.ErrTagExactCombinationsTooLarge} {
		httpErr, ok := h.mapReadError(sentinel).(*httpx.HTTPError)
		if !ok {
			t.Fatalf("mapReadError(%v) did not return an HTTPError", sentinel)
		}
		if httpErr.Status != httpx.KindUnprocessable {
			t.Fatalf("mapReadError(%v) status = %d, want 422", sentinel, httpErr.Status)
		}
		if failure := tagScopeFailure(sentinel); failure == nil || len(failure.Errors) != 1 {
			t.Fatalf("tagScopeFailure(%v) = %+v", sentinel, failure)
		}
	}
}
