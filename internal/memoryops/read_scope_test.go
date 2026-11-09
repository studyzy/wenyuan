package memoryops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TestMemoryReadScope pins the parameter's answers: absent means team, the two
// supported values map to their preference, and both a refused-but-known value
// and an unknown one are field-level items that say which they are.
func TestMemoryReadScope(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		want     storage.FallbackPreference
		wantKind string // "" means accepted
	}{
		{"absent", "", storage.PreferTeam, ""},
		{"empty", "?scope=", storage.PreferTeam, ""},
		{"team", "?scope=team", storage.PreferTeam, ""},
		{"personal", "?scope=personal", storage.PreferPersonal, ""},
		{"both is known but unsupported", "?scope=both", storage.PreferTeam, "value_error"},
		{"unknown", "?scope=bogus", storage.PreferTeam, "literal_error"},
		{"case matters", "?scope=PERSONAL", storage.PreferTeam, "literal_error"},
		{"last occurrence wins", "?scope=personal&scope=team", storage.PreferTeam, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b/memories/recall"+tc.query, nil)
			got, failure := memoryReadScope(req)
			if tc.wantKind == "" {
				if failure != nil {
					t.Fatalf("scope %q was refused: %v", tc.query, failure)
				}
				if got != tc.want {
					t.Fatalf("scope %q = %v, want %v", tc.query, got, tc.want)
				}
				return
			}
			if failure == nil || len(failure.Errors) != 1 {
				t.Fatalf("scope %q was accepted, want one failure item", tc.query)
			}
			if kind := failure.Errors[0].Type; kind != tc.wantKind {
				t.Fatalf("scope %q error type = %q, want %q", tc.query, kind, tc.wantKind)
			}
			loc := strings.Join(failure.Errors[0].Loc, ".")
			if loc != "query.scope" {
				t.Fatalf("scope %q error location = %q, want query.scope", tc.query, loc)
			}
		})
	}
}

// TestMemoryReadScopeBothNamesItself pins that "both" reads as a known value
// that is not available yet, while an unknown value is told what exists.
func TestMemoryReadScopeBothNamesItself(t *testing.T) {
	_, failure := memoryReadScope(httptest.NewRequest(http.MethodPost, "/x?scope=both", nil))
	if failure == nil {
		t.Fatal("both must be refused while fusion is unimplemented")
	}
	if msg := failure.Errors[0].Msg; !strings.Contains(msg, "not supported yet") {
		t.Fatalf("both message = %q, want it to say the value is not supported yet", msg)
	}
	_, failure = memoryReadScope(httptest.NewRequest(http.MethodPost, "/x?scope=bogus", nil))
	if failure == nil {
		t.Fatal("an unknown value must be refused")
	}
	if msg := failure.Errors[0].Msg; !strings.Contains(msg, "'team'") ||
		!strings.Contains(msg, "'personal'") || !strings.Contains(msg, "'both'") {
		t.Fatalf("unknown-value message = %q, want the accepted set rendered in full", msg)
	}
}
