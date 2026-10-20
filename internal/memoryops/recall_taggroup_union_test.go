package memoryops

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// tagGroupCases pins the item stream the reference's left-to-right union emits
// for one `tag_groups` body. Each case was taken from the live reference
// service, so the vertex list is the contract rather than an inference.
type tagGroupCase struct {
	name string
	body string
	// want is the rendered envelope for a refused body, and "" for a body the
	// union accepts.
	want string
}

var tagGroupCases = []tagGroupCase{
	{
		name: "valid leaf",
		body: `[{"tags":["a"]}]`,
	},
	{
		name: "empty leaf is a valid filter",
		body: `[{"tags":[]}]`,
	},
	{
		name: "valid compound",
		body: `[{"and":[{"tags":["a"]},{"or":[{"tags":["b"]},{"not":{"tags":["c"]}}]}]}]`,
	},
	{
		name: "leaf member that is not a list",
		body: `[{"tags":"a"}]`,
		want: `[{"type":"list_type","loc":["body","tag_groups",0,"TagGroupLeaf","tags"],"msg":"Input should be a valid list","input":"a"},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupAnd","and"],"msg":"Field required","input":{"tags":"a"}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"tags":"a"}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupNot","not"],"msg":"Field required","input":{"tags":"a"}}]`,
	},
	{
		name: "and member that is not a list",
		body: `[{"and":5}]`,
		want: `[{"type":"missing","loc":["body","tag_groups",0,"TagGroupLeaf","tags"],"msg":"Field required","input":{"and":5}},` +
			`{"type":"list_type","loc":["body","tag_groups",0,"TagGroupAnd","and"],"msg":"Input should be a valid list","input":5},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"and":5}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupNot","not"],"msg":"Field required","input":{"and":5}}]`,
	},
	{
		name: "leaf literal outside its set",
		body: `[{"tags":["a"],"resolve":"bogus"}]`,
		want: `[{"type":"literal_error","loc":["body","tag_groups",0,"TagGroupLeaf","resolve"],"msg":"Input should be 'exact' or 'fuzzy'","input":"bogus","ctx":{"expected":"'exact' or 'fuzzy'"}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupAnd","and"],"msg":"Field required","input":{"tags":["a"],"resolve":"bogus"}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"tags":["a"],"resolve":"bogus"}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupNot","not"],"msg":"Field required","input":{"tags":["a"],"resolve":"bogus"}}]`,
	},
	{
		name: "leaf tag that is not a string",
		body: `[{"tags":[5]}]`,
		want: `[{"type":"string_type","loc":["body","tag_groups",0,"TagGroupLeaf","tags",0],"msg":"Input should be a valid string","input":5},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupAnd","and"],"msg":"Field required","input":{"tags":[5]}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"tags":[5]}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupNot","not"],"msg":"Field required","input":{"tags":[5]}}]`,
	},
	{
		name: "child that is not an object",
		body: `[{"and":[10]}]`,
		want: `[{"type":"missing","loc":["body","tag_groups",0,"TagGroupLeaf","tags"],"msg":"Field required","input":{"and":[10]}},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupAnd","and",0,"TagGroupLeaf"],"msg":"Input should be a valid dictionary or object to extract fields from","input":10},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupAnd","and",0,"TagGroupAnd"],"msg":"Input should be a valid dictionary or object to extract fields from","input":10},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupAnd","and",0,"TagGroupOr"],"msg":"Input should be a valid dictionary or object to extract fields from","input":10},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupAnd","and",0,"TagGroupNot"],"msg":"Input should be a valid dictionary or object to extract fields from","input":10},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"and":[10]}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupNot","not"],"msg":"Field required","input":{"and":[10]}}]`,
	},
	{
		name: "not member that is not an object",
		body: `[{"not":5}]`,
		want: `[{"type":"missing","loc":["body","tag_groups",0,"TagGroupLeaf","tags"],"msg":"Field required","input":{"not":5}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupAnd","and"],"msg":"Field required","input":{"not":5}},` +
			`{"type":"missing","loc":["body","tag_groups",0,"TagGroupOr","or"],"msg":"Field required","input":{"not":5}},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupNot","not","TagGroupLeaf"],"msg":"Input should be a valid dictionary or object to extract fields from","input":5},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupNot","not","TagGroupAnd"],"msg":"Input should be a valid dictionary or object to extract fields from","input":5},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupNot","not","TagGroupOr"],"msg":"Input should be a valid dictionary or object to extract fields from","input":5},` +
			`{"type":"model_attributes_type","loc":["body","tag_groups",0,"TagGroupNot","not","TagGroupNot"],"msg":"Input should be a valid dictionary or object to extract fields from","input":5}]`,
	},
}

// TestTagGroupUnionItems pins the union's item stream, including the two
// properties that are easy to get backwards: a valid group reports nothing, and
// a group every branch refuses reports every branch in declaration order (not
// only the first failure). Both are checked against the reference.
func TestTagGroupUnionItems(t *testing.T) {
	for _, tc := range tagGroupCases {
		t.Run(tc.name, func(t *testing.T) {
			var field TagGroupsField
			if err := json.Unmarshal([]byte(tc.body), &field); err != nil {
				t.Fatalf("%s must decode its own failure: %v", tc.body, err)
			}
			got, err := json.Marshal(field.Items())
			if err != nil {
				t.Fatalf("marshal items: %v", err)
			}
			if tc.want == "" {
				if len(field.Items()) != 0 {
					t.Fatalf("%s is a valid filter, got items %s", tc.body, got)
				}
				return
			}
			// The item stream and every value in it are compared, but not the
			// key order inside an `input` object: a JSON object carries no key
			// order of its own, and the reference's own order follows its JSON
			// decoder rather than the request document.
			if gotNorm, wantNorm := normaliseItems(t, got), normaliseItems(t, []byte(tc.want)); gotNorm != wantNorm {
				t.Fatalf("%s items mismatch\n got: %s\nwant: %s", tc.body, gotNorm, wantNorm)
			}
		})
	}
}

// normaliseItems re-encodes an item list with sorted object keys, so a
// comparison is about the items and their values rather than about the order a
// decoder happened to emit an input object in.
func normaliseItems(t *testing.T, raw []byte) string {
	t.Helper()
	var items []any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	normalised, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("re-encode %s: %v", raw, err)
	}
	return string(normalised)
}

// TestTagGroupNumbersStayNumbers guards the item input's JSON kind: the
// reference echoes the value it read, so a number must not come back quoted.
func TestTagGroupNumbersStayNumbers(t *testing.T) {
	var field TagGroupsField
	if err := json.Unmarshal([]byte(`[{"and":5}]`), &field); err != nil {
		t.Fatalf("decode: %v", err)
	}
	items := field.Items()
	if len(items) != 4 {
		t.Fatalf("items = %d, want 4", len(items))
	}
	if input, ok := items[1].Input.(json.Number); !ok || string(input) != "5" {
		t.Fatalf("list_type input = %#v, want the JSON number 5", items[1].Input)
	}
}

// A tag_groups value that is not a list is the field's own list-type failure,
// reported once at the field with the value as its input — not as a group.
func TestTagGroupsNonListIsOneFieldItem(t *testing.T) {
	for _, body := range []string{`5`, `true`, `"x"`, `{"tags":["a"]}`} {
		var field TagGroupsField
		if err := json.Unmarshal([]byte(body), &field); err != nil {
			t.Fatalf("%s must decode its own failure: %v", body, err)
		}
		got, _ := json.Marshal(field.Items())
		want := `[{"type":"list_type","loc":["body","tag_groups"],"msg":"Input should be a valid list","input":` + body + `}]`
		if string(got) != want {
			t.Fatalf("%s items = %s, want %s", body, got, want)
		}
	}
}

// An empty leaf names no tag and still filters: under the reference's default
// any_strict it keeps tagged memories and drops untagged ones, and under exact
// it keeps only the untagged ones. Treating it as "no filter" would widen a
// filter the caller wrote to narrow.
func TestEmptyLeafStillFilters(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "tagged", Text: "the deploy runbook is blue-green", Type: "world", Tags: []string{"ops"}})
	store.SeedMemory("b1", storage.RecallMatch{ID: "untagged", Text: "the deploy runbook is canary", Type: "world"})

	cases := map[string][]string{
		`[{"tags":[]}]`:                      {"tagged"},
		`[{"tags":[],"match":"exact"}]`:      {"untagged"},
		`[{"tags":[],"match":"any_strict"}]`: {"tagged"},
	}
	for groups, wantIDs := range cases {
		rec := post(t, mux, "/v1/default/banks/b1/memories/recall",
			`{"query":"deploy runbook","tag_groups":`+groups+`}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, body = %s", groups, rec.Code, rec.Body.String())
		}
		var response RecallResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		got := make([]string, 0, len(response.Results))
		for _, result := range response.Results {
			got = append(got, result.ID)
		}
		if len(got) != len(wantIDs) || (len(got) == 1 && got[0] != wantIDs[0]) {
			t.Fatalf("%s matched %v, want %v (body %s)", groups, got, wantIDs, rec.Body.String())
		}
	}
}
