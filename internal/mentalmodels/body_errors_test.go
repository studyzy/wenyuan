package mentalmodels

import (
	"encoding/json"
	"net/http"
	"testing"
)

// A request model reports every failing field in one envelope, in declaration
// order — name, source_query, max_tokens, tags, then the trigger. Stopping at the
// first failing member hides the rest, so a client that fixes what it was told
// about gets a second round of errors the reference would have named at once.
func TestCreateReportsEveryFailingFieldInDeclarationOrder(t *testing.T) {
	mux, _ := newServer(t)
	detail := decodeValidationFailure(t, send(mux, http.MethodPost, "/v1/default/banks/b1/mental-models",
		`{"max_tokens":5,"tags":[5,"ok"],"trigger":{"mode":"bogus"}}`))

	// This route files tags before max_tokens (the contract's own order), and the
	// trigger's items come last.
	want := []string{
		"body.name",
		"body.source_query",
		"body.tags.0",
		"body.max_tokens",
		"body.trigger.mode",
	}
	if len(detail) != len(want) {
		t.Fatalf("detail = %+v\nwant %d items", detail, len(want))
	}
	for i, wantLoc := range want {
		if got := locString(detail[i].Loc); got != wantLoc {
			t.Fatalf("item %d loc = %q, want %q (full %+v)", i, got, wantLoc, detail)
		}
	}
	if detail[2].Type != "string_type" {
		t.Fatalf("tags item = %+v, want string_type", detail[2])
	}
}

// A tags value that is not a list of strings is refused on the list itself or on
// the entry, as the declared list[str] member is read.
func TestCreateTagsShapeIsValidated(t *testing.T) {
	mux, _ := newServer(t)
	cases := map[string]string{
		`{"name":"a","source_query":"q","tags":5}`:    "list_type",
		`{"name":"a","source_query":"q","tags":null}`: "list_type",
	}
	for body, wantType := range cases {
		detail := decodeValidationFailure(t, send(mux, http.MethodPost, "/v1/default/banks/b1/mental-models", body))
		if len(detail) != 1 || detail[0].Type != wantType {
			t.Fatalf("%s: detail = %+v, want one %s item", body, detail, wantType)
		}
		if got := locString(detail[0].Loc); got != "body.tags" {
			t.Fatalf("%s: loc = %q", body, got)
		}
	}
}

// A patch accumulates across its declared members too: the tags failure must not
// hide the max_tokens or trigger failures behind it.
func TestUpdateReportsEveryFailingFieldInDeclarationOrder(t *testing.T) {
	mux := newTriggerServer(t)
	detail := triggerDetail(t, mux, `{"tags":[5],"max_tokens":5,"name":5,"trigger":{"mode":"bogus"}}`)

	// name is declared first and is a plain string, so a number is its own item.
	// A patch files its items in strict declaration order, which puts max_tokens
	// ahead of tags.
	want := []string{"body.name", "body.max_tokens", "body.tags.0", "body.trigger.mode"}
	if len(detail) != len(want) {
		t.Fatalf("detail = %+v\nwant %d items", detail, len(want))
	}
	for i, wantLoc := range want {
		if got := locString(detail[i].Loc); got != wantLoc {
			t.Fatalf("item %d loc = %q, want %q (full %+v)", i, got, wantLoc, detail)
		}
	}
}

// The bound item echoes the value the CLIENT sent. A boolean is coerced to an
// integer by the lax rules, so `true` becomes 1 — but the reference names the
// `true` it was given, not the 1 it coerced to.
func TestMaxTokensBoundEchoesTheSentValue(t *testing.T) {
	mux, _ := newServer(t)
	cases := []struct {
		body  string
		input string
	}{
		{`{"name":"a","source_query":"q","max_tokens":true}`, `true`},
		{`{"name":"a","source_query":"q","max_tokens":100}`, `100`},
	}
	for _, tc := range cases {
		t.Run(tc.body, func(t *testing.T) {
			rec := send(mux, http.MethodPost, "/v1/default/banks/b1/mental-models", tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("status = %d, want 422 (%s)", rec.Code, rec.Body.String())
			}
			var envelope struct {
				Detail []triggerItem `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if len(envelope.Detail) != 1 || envelope.Detail[0].Type != "greater_than_equal" {
				t.Fatalf("detail = %+v, want one greater_than_equal item", envelope.Detail)
			}
			got, _ := json.Marshal(envelope.Detail[0].Input)
			if string(got) != tc.input {
				t.Fatalf("input = %s, want %s", got, tc.input)
			}
		})
	}
}
