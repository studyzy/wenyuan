package reporting

import (
	"encoding/json"
	"net/http"
	"testing"
)

// Contract reference: the reference's create-body model
// (hindsight-api-slim/hindsight_api/api/http.py create-page / create-folder) and
// its framework's single validation pass.
//
// The framework validates the whole model before it asks whether a member is
// required, so a body that both omits a required member and carries a wrongly
// typed one reports BOTH items — the required one first, in declaration order,
// then the field's own check. An implementation that returns early on the first
// problem reports one item where the reference reports two, so each case here
// fails without the fix.

// knowledgeCreateDetail returns the item list of a knowledge create response.
func knowledgeCreateDetail(t *testing.T, body []byte) []map[string]any {
	t.Helper()
	var envelope struct {
		Detail []map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("decode %s: %v", body, err)
	}
	return envelope.Detail
}

// knowledgeItemLoc renders one item's loc as a JSON path for comparison.
func knowledgeItemLoc(t *testing.T, item map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(item["loc"])
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestKnowledgeCreateReportsEveryItemInOnePass(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base/pages"

	cases := []struct {
		name string
		body string
		want []string // type + loc pairs, in order
	}{
		{
			name: "missing required member and a wrong field",
			body: `{"name":"x","tags":"a"}`,
			want: []string{
				`missing ["body","source_query"]`,
				`list_type ["body","tags"]`,
			},
		},
		{
			name: "two missing required members then a wrong field",
			body: `{"tags":"a"}`,
			want: []string{
				`missing ["body","name"]`,
				`missing ["body","source_query"]`,
				`list_type ["body","tags"]`,
			},
		},
		{
			name: "wrong members report in declaration order, missing first",
			body: `{"tags":1,"name":2}`,
			want: []string{
				`string_type ["body","name"]`,
				`missing ["body","source_query"]`,
				`list_type ["body","tags"]`,
			},
		},
		{
			name: "a wrong tag element is one item, the missing member another",
			body: `{"name":"x","tags":["a",1]}`,
			want: []string{
				`missing ["body","source_query"]`,
				`string_type ["body","tags",1]`,
			},
		},
		{
			name: "a nested model's own items follow the fields",
			body: `{"name":"x","trigger":"a"}`,
			want: []string{
				`missing ["body","source_query"]`,
				`model_attributes_type ["body","trigger"]`,
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(mux, http.MethodPost, base, tc.body)
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s = %d, want 422: %s", tc.body, rec.Code, rec.Body.String())
			}
			items := knowledgeCreateDetail(t, rec.Body.Bytes())
			if len(items) != len(tc.want) {
				t.Fatalf("%s items = %d (%s), want %d", tc.body, len(items), rec.Body.String(), len(tc.want))
			}
			for i, want := range tc.want {
				got := items[i]["type"].(string) + " " + knowledgeItemLoc(t, items[i])
				if got != want {
					t.Fatalf("%s item %d = %q, want %q (%s)", tc.body, i, got, want, rec.Body.String())
				}
			}
		})
	}
}

// A `missing` item echoes the body as the client wrote it: the reference reports
// its framework's raw_request, an insertion-ordered mapping, so the members come
// back in the order they were sent rather than in a sorted order.
func TestKnowledgeCreateMissingItemEchoesBodyInOrder(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base/pages"

	rec := send(mux, http.MethodPost, base, `{"zeta":1,"alpha":2,"b":{"y":1,"x":2}}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	const want = `{"detail":[{"type":"missing","loc":["body","name"],"msg":"Field required","input":{"zeta":1,"alpha":2,"b":{"y":1,"x":2}}},{"type":"missing","loc":["body","source_query"],"msg":"Field required","input":{"zeta":1,"alpha":2,"b":{"y":1,"x":2}}}]}`
	if got := rec.Body.String(); got != want {
		t.Fatalf("body =\n%s\nwant\n%s", got, want)
	}
}

// A page body carries the page options; the folder body does not. The reference
// ignores an unknown member, so a folder create with a page-only option and an
// untyped name is refused for the name alone rather than for the option.
func TestKnowledgeFolderBodyIgnoresPageOptions(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base/folders"

	rec := send(mux, http.MethodPost, base, `{"tags":"a","tags_match":"any"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	items := knowledgeCreateDetail(t, rec.Body.Bytes())
	if len(items) != 1 || items[0]["type"] != "missing" || knowledgeItemLoc(t, items[0]) != `["body","name"]` {
		t.Fatalf("items = %s, want the missing name alone", rec.Body.String())
	}
}

// An empty option is still a value the framework validates, but an option a
// folder body does not declare is not looked at — so a folder still creates.
func TestKnowledgeFolderIgnoresUndeclaredOptions(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base/folders"

	rec := send(mux, http.MethodPost, base, `{"name":"x","tags":"a"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("code = %d, want 201: %s", rec.Code, rec.Body.String())
	}
}

// An explicitly empty `tag_groups` is treated as absent: the reference's
// dependency reads `adapter.validate_json(raw) if raw else None` (api/http.py:3712),
// so the request filters nothing instead of failing to parse. A whitespace value
// is not empty there and still reaches the adapter.
func TestKnowledgeEmptyTagGroupsIsAbsent(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base"

	if rec := send(mux, http.MethodGet, base+"/tree?tag_groups=", ""); rec.Code != http.StatusOK {
		t.Fatalf("tree tag_groups= = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	if rec := send(mux, http.MethodGet, base+"/tree?tag_groups=%20", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("tree tag_groups=%%20 = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// `tags_match` is declared before `tag_groups`, so a request carrying a bad enum
// and an unparseable tree reports only the enum: the tree's failure comes from a
// dependency that never runs.
func TestKnowledgeBadTagsMatchHidesBadTagGroups(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base"

	rec := send(mux, http.MethodGet, base+"/tree?tags_match=bogus&tag_groups=%7B%7B", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	items := knowledgeCreateDetail(t, rec.Body.Bytes())
	if len(items) != 1 || items[0]["type"] != "literal_error" || knowledgeItemLoc(t, items[0]) != `["query","tags_match"]` {
		t.Fatalf("items = %s, want the tags_match literal error alone", rec.Body.String())
	}
}

// The search route collects every rejected parameter into one 422, with the
// dependency's item ahead of the query parameters' — the reference's dependency
// tree is resolved before the endpoint's own parameters.
func TestKnowledgeSearchCollectsEveryItem(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base"

	rec := send(mux, http.MethodGet, base+"/search?tags_match=bogus&limit=x", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("code = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	items := knowledgeCreateDetail(t, rec.Body.Bytes())
	if len(items) < 2 {
		t.Fatalf("items = %s, want the enum item and the limit item", rec.Body.String())
	}
	if items[0]["type"] != "literal_error" || knowledgeItemLoc(t, items[0]) != `["query","tags_match"]` {
		t.Fatalf("first item = %s, want the tags_match literal error", rec.Body.String())
	}
}

// The reference declares `limit` as an int under the framework's lax coercion and
// unbounded Python range, so a float-like text is a number while a huge one is a
// range violation rather than a parse failure.
func TestKnowledgeSearchLimitUsesFrameworkParsing(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base"

	for _, tc := range []struct {
		limit string
		want  int
	}{
		// The framework's coercion reads 5.0 as the integer 5, so it is inside the
		// 1..50 bound rather than a parse failure.
		{"5.0", http.StatusOK},
		{"1_000", http.StatusUnprocessableEntity},
		{"abc", http.StatusUnprocessableEntity},
		{"99999999999999999999", http.StatusUnprocessableEntity},
	} {
		rec := send(mux, http.MethodGet, base+"/search?q=deploy&limit="+tc.limit, "")
		if rec.Code != tc.want {
			t.Fatalf("limit=%s = %d, want %d: %s", tc.limit, rec.Code, tc.want, rec.Body.String())
		}
	}
}

// A body the parser cannot read at all is the parser's own item: the framework's
// request layer never reaches the model, so no `missing` member is reported
// alongside it. A body carrying a wrongly typed member is different — the model
// does run, and reports the rest with it.
func TestKnowledgeCreateParserFailureStandsAlone(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base"

	for _, path := range []string{"/pages", "/folders"} {
		rec := send(mux, http.MethodPost, base+path, `notjson`)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
		items := knowledgeCreateDetail(t, rec.Body.Bytes())
		if len(items) != 1 || items[0]["type"] != "json_invalid" {
			t.Fatalf("%s items = %s, want the json_invalid item alone", path, rec.Body.String())
		}
	}
}

// The reference's search results carry a plain Python float score, so a whole
// score renders `1.0` — not Go's `1`, which is a different token on the wire.
func TestKnowledgeSearchRendersScoreAsAPythonFloat(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank + "/knowledge-base"

	if rec := send(mux, http.MethodPost, base+"/pages", `{"name":"Deploy","source_query":"steps"}`); rec.Code != http.StatusCreated {
		t.Fatalf("create page: %d %s", rec.Code, rec.Body.String())
	}
	rec := send(mux, http.MethodGet, base+"/search?q=deploy", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}
	var envelope struct {
		Results []struct {
			Score json.Number `json:"score"`
		} `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Results) == 0 {
		t.Fatalf("no results: %s", rec.Body.String())
	}
	if got := envelope.Results[0].Score.String(); got != "1.0" {
		t.Fatalf("score = %s, want 1.0 (%s)", got, rec.Body.String())
	}
}
