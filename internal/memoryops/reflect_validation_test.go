package memoryops

import (
	"encoding/json"
	"net/http"
	"testing"
)

// reflectUnionCase pins one reflection request body's validation answer,
// taken from the live reference service.
type reflectUnionCase struct {
	name string
	body string
	// wantItems is the rendered envelope for a refused body; "" means the body
	// is accepted.
	wantItems string
}

var reflectUnionCases = []reflectUnionCase{
	{
		name: "fact type outside the set",
		body: `{"query":"q","fact_types":["bogus"]}`,
		wantItems: `[{"type":"literal_error","loc":["body","fact_types",0],` +
			`"msg":"Input should be 'world', 'experience' or 'observation'","input":"bogus",` +
			`"ctx":{"expected":"'world', 'experience' or 'observation'"}}]`,
	},
	{
		name:      "empty fact type list",
		body:      `{"query":"q","fact_types":[]}`,
		wantItems: `[{"type":"value_error","loc":["body","fact_types"],"msg":"Value error, fact_types must not be empty. Use null to include all fact types.","input":[],"ctx":{"error":{}}}]`,
	},
	{
		name: "tag group member that is not a list",
		body: `{"query":"q","tag_groups":[{"and":5}]}`,
		wantItems: `[{"type":"missing","loc":["body","tag_groups",0,"TagGroupLeaf","tags"],"msg":"Field required","input":{"and":5}},` +
			`{"type":"list_type","loc":["body","tag_groups",0,"TagGroupAnd","and"],"msg":"Input should be a valid list","input":5},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"and":5}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupNot","not"],"msg":"Field required","input":{"and":5}}]`,
	},
	{
		name: "valid tag group",
		body: `{"query":"q","tag_groups":[{"tags":["a"]}]}`,
	},
	{
		name: "empty leaf tag group",
		body: `{"query":"q","tag_groups":[{"tags":[]}]}`,
	},
	{
		name: "phantom trace member is ignored",
		body: `{"query":"q","trace":true}`,
	},
}

// TestReflectUnionItemStream pins the reflection route's validation answers
// against the reference. Two of them are easy to get wrong: the fact-type
// selection reports one item per element (not a single body-level message), and
// a tag group reports the whole branch tree rather than its first failure.
func TestReflectUnionItemStream(t *testing.T) {
	for _, tc := range reflectUnionCases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := reflectServer(t)
			rec := post(t, mux, "/v1/default/banks/b1/reflect", tc.body)
			if tc.wantItems == "" {
				if rec.Code == http.StatusUnprocessableEntity {
					t.Fatalf("%s must be accepted, got 422 %s", tc.body, rec.Body.String())
				}
				return
			}
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s status = %d, want 422 (body %s)", tc.body, rec.Code, rec.Body.String())
			}
			var envelope struct {
				Detail json.RawMessage `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode envelope: %v", err)
			}
			if got, want := normaliseItems(t, envelope.Detail), normaliseItems(t, []byte(tc.wantItems)); got != want {
				t.Fatalf("%s detail mismatch\n got: %s\nwant: %s", tc.body, got, want)
			}
		})
	}
}

// A reflect request has no `trace` member: the reference models only
// `include.tool_calls`, so a body that states `trace` is not an error, it is
// ignored, and the response reports no trace. A decoder that accepted the
// member would turn it into a trace the reference never produces.
func TestReflectIgnoresPhantomTraceMember(t *testing.T) {
	mux, _ := reflectServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/reflect", `{"query":"q","trace":true}`)
	if rec.Code == http.StatusUnprocessableEntity {
		t.Fatalf("a phantom member must not be refused: %s", rec.Body.String())
	}
	// Decoding must not turn the member into a request field, whatever the
	// route answered: the struct is the wire contract.
	var request ReflectRequest
	if err := json.Unmarshal([]byte(`{"query":"q","trace":true}`), &request); err != nil {
		t.Fatalf("decode: %v", err)
	}
	rendered, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(rendered, &fields); err != nil {
		t.Fatalf("decode rendered request: %v", err)
	}
	if _, present := fields["trace"]; present {
		t.Fatalf("the request model gained a trace member: %s", rendered)
	}
	if rec.Code == http.StatusOK && rec.Body.Len() > 0 {
		var response struct {
			Trace json.RawMessage `json:"trace"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatalf("decode response: %v", err)
		}
		if len(response.Trace) != 0 && string(response.Trace) != "null" {
			t.Fatalf("a phantom trace member produced a trace: %s", rec.Body.String())
		}
	}
}
