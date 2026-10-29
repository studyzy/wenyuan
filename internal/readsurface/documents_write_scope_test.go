package readsurface

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The scopes a document update names are writes too — consolidation writes the
// document's observations at them — so the reference checks them beside the
// tags, after the document itself is writable (update_document's
// _refuse_unwritable_scopes call). "shared" writes the untagged scope, an
// explicit list checks only its literal entries, and a caller with no write
// scope is untouched.
func TestUpdateDocumentChecksObservationScopes(t *testing.T) {
	cases := []struct {
		name    string
		apiKey  string
		scopes  string
		status  int
		message string
	}{
		{
			name:    "a literal scope outside the write scope is refused",
			apiKey:  "dan",
			scopes:  `[["user:kate"]]`,
			status:  http.StatusForbidden,
			message: "You can't write observations tagged user:kate",
		},
		{
			name:   "a literal scope inside the write scope is accepted",
			apiKey: "dan",
			scopes: `[["user:dan"]]`,
			status: http.StatusOK,
		},
		{
			name:    "shared writes the untagged scope, which nobody confined may write",
			apiKey:  "dan",
			scopes:  `"shared"`,
			status:  http.StatusForbidden,
			message: "You can't write observations tagged (untagged)",
		},
		{
			name:   "a group of only globs checks nothing",
			apiKey: "dan",
			scopes: `[["topic:*"]]`,
			status: http.StatusOK,
		},
		{
			name:    "a mixed group names only the tags outside the scope, sorted",
			apiKey:  "dan",
			scopes:  `[["user:zed","user:dan","user:kate"]]`,
			status:  http.StatusForbidden,
			message: "You can't write observations tagged user:kate, user:zed",
		},
		{
			name:   "the same shared scope from an unconfined caller is fine",
			apiKey: "",
			scopes: `"shared"`,
			status: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
				t.Fatal(err)
			}
			// A document inside dan's write scope, so the refusal — when there
			// is one — is about the scopes and not the document's own tags.
			if _, err := store.PutDocument(context.Background(), bank, "dan-doc", storage.Document{Tags: []string{"user:dan"}}); err != nil {
				t.Fatal(err)
			}
			handler := scopedHandler(t, store)
			rec := sendAsBody(t, handler, tc.apiKey, http.MethodPatch,
				"/v1/default/banks/"+bank+"/documents/dan-doc",
				`{"observation_scopes":`+tc.scopes+`}`)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.message != "" && !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("body = %s, want it to name %q", rec.Body.String(), tc.message)
			}
		})
	}
}

// A document the caller cannot read stays a 404: the check on the scopes comes
// after the document's own writability, the way update_document orders
// _require_writable before its scopes check.
func TestUpdateDocumentScopesCheckFollowsTheDocumentCheck(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDocument(context.Background(), bank, "kate-doc", storage.Document{Tags: []string{"user:kate"}}); err != nil {
		t.Fatal(err)
	}
	handler := scopedHandler(t, store)
	rec := sendAsBody(t, handler, "dan", http.MethodPatch,
		"/v1/default/banks/"+bank+"/documents/kate-doc",
		`{"observation_scopes":[["user:kate"]]}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404, body = %s", rec.Code, rec.Body.String())
	}
}
