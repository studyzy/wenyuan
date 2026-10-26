package reporting

import (
	"net/http"
	"strings"
	"testing"
)

// The dry-run request model declares `entity_labels` as a list of the reference's
// LabelGroups, so the framework validates it in the same pass as the content and
// refuses an invalid one before the handler runs — the item is filed at
// `body.entity_labels.<index>.<member path>` and carries the model's own message.
// Accepting it (this route's earlier behaviour) ran the extraction under a
// vocabulary the reference would have refused.
func TestDryRunRejectsAnInvalidEntityLabelsOverride(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "a group with no key",
			body: `{"content":"x","entity_labels":[{"values":[{"value":"a"}]}]}`,
			// The model has already migrated and serialised the group it
			// reports, so the legacy `type` is present.
			want: `{"detail":[{"type":"missing","loc":["body","entity_labels",0,"key"],` +
				`"msg":"Field required","input":{"type":"value","values":[{"value":"a"}]}}]}`,
		},
		{
			name: "an unknown type",
			body: `{"content":"x","entity_labels":[{"key":"k","type":"bogus"}]}`,
			want: `{"detail":[{"type":"literal_error","loc":["body","entity_labels",0,"type"],` +
				`"msg":"Input should be 'value', 'multi-values', 'text', 'multi-text' or 'map'",` +
				`"input":"bogus","ctx":{"expected":"'value', 'multi-values', 'text', 'multi-text' or 'map'"}}]}`,
		},
		{
			name: "a key of the wrong type",
			body: `{"content":"x","entity_labels":[{"key":5}]}`,
			want: `{"detail":[{"type":"string_type","loc":["body","entity_labels",0,"key"],` +
				`"msg":"Input should be a valid string","input":5}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux := dryRunServer(t, nil, "b1")
			rec := send(mux, http.MethodPost, "/v1/default/banks/b1/memories/dry-run-extract", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422, body = %s", rec.Code, rec.Body.String())
			}
			if got := strings.TrimSuffix(rec.Body.String(), "\n"); got != tc.want {
				t.Fatalf("body = %s\nwant %s", got, tc.want)
			}
		})
	}
}

// A valid override is accepted, and an empty list is the caller saying "no
// override" rather than an invalid model.
func TestDryRunAcceptsAValidEntityLabelsOverride(t *testing.T) {
	for _, body := range []string{
		`{"content":"x","entity_labels":[{"key":"topic","type":"value","values":[{"value":"a"}]}]}`,
		`{"content":"x","entity_labels":[]}`,
	} {
		mux := dryRunServer(t, nil, "b1")
		rec := send(mux, http.MethodPost, "/v1/default/banks/b1/memories/dry-run-extract", body)
		if rec.Code != http.StatusOK {
			t.Fatalf("body %s: status = %d, want 200, response = %s", body, rec.Code, rec.Body.String())
		}
	}
}
