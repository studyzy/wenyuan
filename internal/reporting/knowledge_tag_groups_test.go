package reporting

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The reference validates `tag_groups` through a Pydantic
// `TypeAdapter(list[TagGroup])` where TagGroup is a left-to-right union
// (api/http.py:3712-3720, engine/search/tags.py:307-346), so a document that
// parses but is not a well-formed tag-group tree is a 422 — not a filter that
// silently does nothing. Each case below pins the exact detail string, because
// it is the whole 422 body the caller reads.
func TestKnowledgeTagGroupsValidationReport(t *testing.T) {
	cases := []struct {
		name   string
		rawn   string
		detail string
	}{
		{
			// A JSON syntax error is reported with the parser's own wording, not
			// the CPython json module's ("expected ident", not "Expecting value").
			name:   "json syntax error",
			rawn:   `notjson`,
			detail: "Invalid tag_groups: 1 validation error for list[union[TagGroupLeaf,TagGroupAnd,TagGroupOr,TagGroupNot]]\n  Invalid JSON: expected ident at line 1 column 2 [type=json_invalid, input_value='notjson', input_type=str]\n    For further information visit https://errors.pydantic.dev/2.12/v/json_invalid",
		},
		{
			// A root-level failure prints no location line at all.
			name:   "not a list",
			rawn:   `{}`,
			detail: "Invalid tag_groups: 1 validation error for list[union[TagGroupLeaf,TagGroupAnd,TagGroupOr,TagGroupNot]]\n  Input should be a valid array [type=list_type, input_value={}, input_type=dict]\n    For further information visit https://errors.pydantic.dev/2.12/v/list_type",
		},
		{
			// A union in left-to-right mode reports every member's failure, so one
			// malformed object yields four errors: the leaf's, then the three
			// compound members' missing fields.
			name: "leaf field wrong type",
			rawn: `[{"tags":"x"}]`,
			detail: "Invalid tag_groups: 4 validation errors for list[union[TagGroupLeaf,TagGroupAnd,TagGroupOr,TagGroupNot]]\n" +
				"0.TagGroupLeaf.tags\n  Input should be a valid array [type=list_type, input_value='x', input_type=str]\n    For further information visit https://errors.pydantic.dev/2.12/v/list_type\n" +
				"0.TagGroupAnd.and\n  Field required [type=missing, input_value={'tags': 'x'}, input_type=dict]\n    For further information visit https://errors.pydantic.dev/2.12/v/missing\n" +
				"0.TagGroupOr.or\n  Field required [type=missing, input_value={'tags': 'x'}, input_type=dict]\n    For further information visit https://errors.pydantic.dev/2.12/v/missing\n" +
				"0.TagGroupNot.not\n  Field required [type=missing, input_value={'tags': 'x'}, input_type=dict]\n    For further information visit https://errors.pydantic.dev/2.12/v/missing",
		},
		{
			// TagResolution is a two-member Literal.
			name: "resolve outside its literal",
			rawn: `[{"tags":["a"],"resolve":"bogus"}]`,
			detail: "Invalid tag_groups: 4 validation errors for list[union[TagGroupLeaf,TagGroupAnd,TagGroupOr,TagGroupNot]]\n" +
				"0.TagGroupLeaf.resolve\n  Input should be 'exact' or 'fuzzy' [type=literal_error, input_value='bogus', input_type=str]\n    For further information visit https://errors.pydantic.dev/2.12/v/literal_error\n" +
				"0.TagGroupAnd.and\n  Field required [type=missing, input_value={'tags': ['a'], 'resolve': 'bogus'}, input_type=dict]\n    For further information visit https://errors.pydantic.dev/2.12/v/missing\n" +
				"0.TagGroupOr.or\n  Field required [type=missing, input_value={'tags': ['a'], 'resolve': 'bogus'}, input_type=dict]\n    For further information visit https://errors.pydantic.dev/2.12/v/missing\n" +
				"0.TagGroupNot.not\n  Field required [type=missing, input_value={'tags': ['a'], 'resolve': 'bogus'}, input_type=dict]\n    For further information visit https://errors.pydantic.dev/2.12/v/missing",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, detail, ok := decodeKnowledgeTagGroups(tc.rawn)
			if ok {
				t.Fatalf("decodeKnowledgeTagGroups(%q) accepted the document", tc.rawn)
			}
			if detail != tc.detail {
				t.Fatalf("detail mismatch\n got: %q\nwant: %q", detail, tc.detail)
			}
		})
	}
}

// Pydantic renders a dict's `input_value` in the order the document stated its
// keys (Python dicts keep insertion order), which is why the report cannot be
// built from a Go map.
func TestKnowledgeTagGroupsReportKeepsInputKeyOrder(t *testing.T) {
	_, detail, ok := decodeKnowledgeTagGroups(`[{"bogus":1,"tags":5}]`)
	if ok {
		t.Fatal("accepted a document whose tags is not a list")
	}
	if !strings.Contains(detail, `input_value={'bogus': 1, 'tags': 5}`) {
		t.Fatalf("report did not preserve key order: %q", detail)
	}
}

// A well-formed tree validates; `not` holds a *single* nested group (the
// reference declares `filter: TagGroup`), while `and`/`or` hold lists.
func TestKnowledgeTagGroupsAcceptsWellFormedTrees(t *testing.T) {
	for _, raw := range []string{
		`[]`,
		`[{"tags":["a"]}]`,
		`[{"tags":["a"],"match":"all_strict","resolve":"fuzzy"}]`,
		`[{"and":[{"tags":["a"]},{"tags":["b"]}]}]`,
		`[{"or":[{"tags":["a"]}]}]`,
		`[{"not":{"tags":["a"]}}]`,
		`[{"and":[{"not":{"tags":["a"]}}]}]`,
		// Unknown members are ignored, as a non-strict model does.
		`[{"tags":["a"],"extra":1}]`,
	} {
		if _, detail, ok := decodeKnowledgeTagGroups(raw); !ok {
			t.Fatalf("decodeKnowledgeTagGroups(%q) refused: %s", raw, detail)
		}
	}
}

// A `not` holding a list is refused, because the reference's `not` is one group,
// not a list of them.
func TestKnowledgeTagGroupsRefusesNotAsList(t *testing.T) {
	_, detail, ok := decodeKnowledgeTagGroups(`[{"not":[]}]`)
	if ok {
		t.Fatal("accepted `not` as a list")
	}
	if !strings.Contains(detail, "0.TagGroupNot.not.TagGroupLeaf") {
		t.Fatalf("report did not walk into the not group: %q", detail)
	}
	if !strings.Contains(detail, "7 validation errors") {
		t.Fatalf("want 7 errors (3 missing + 4 nested), got: %q", detail)
	}
}

// `not` negates its single child, so a page carrying the child's tags is
// excluded and one without them is kept.
func TestKnowledgeTagGroupNotNegatesItsSingleChild(t *testing.T) {
	group := knowledgeTagGroup{Not: &knowledgeTagGroup{Tags: []string{"secret"}}}
	if group.matches([]string{"secret", "team"}) {
		t.Fatal("not(tags=[secret]) matched a page carrying the tag")
	}
	if !group.matches([]string{"team"}) {
		t.Fatal("not(tags=[secret]) rejected a page without the tag")
	}
}

// The knowledge routes resolve their query parameters *before* looking the bank
// up, because the reference declares the tag filter as a `Depends`
// (api/http.py:7314) and the framework resolves dependencies before the handler
// body runs. A malformed filter on an unknown bank is therefore the 422, not the
// 404 — and a bad `tags_match` beats the 404 the same way.
func TestKnowledgeRoutesValidateParametersBeforeTheBankLookup(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := boundMux(store)
	cases := []struct {
		name string
		path string
		want string
	}{
		{"tree tag_groups", "/v1/default/banks/never-created/knowledge-base/tree?tag_groups=%7B%7D", "Invalid tag_groups"},
		{"tree tags_match", "/v1/default/banks/never-created/knowledge-base/tree?tags_match=bogus", "tags_match"},
		{"search tag_groups", "/v1/default/banks/never-created/knowledge-base/search?q=a&tag_groups=%7B%7D", "Invalid tag_groups"},
		{"search missing q", "/v1/default/banks/never-created/knowledge-base/search", `"q"`},
		{"search bad limit", "/v1/default/banks/never-created/knowledge-base/search?q=a&limit=0", "limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tc.path, nil))
			if rec.Code != http.StatusUnprocessableEntity {
				t.Fatalf("%s = %d, want 422: %s", tc.path, rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("body %q does not mention %q", rec.Body.String(), tc.want)
			}
		})
	}
}

// Served-level: a `tag_groups` document that parses but is not a well-formed
// tree is a 422 on both knowledge routes. Before this validation existed the
// parameter was unmarshalled and any JSON at all was accepted, so a filter the
// reference refuses was applied as "no filter" here.
func TestKnowledgeTagGroupsShapeIsValidatedOnBothRoutes(t *testing.T) {
	mux := boundMux(storage.NewMemoryStore("public"))
	for _, path := range []string{
		// a non-list, and a list whose item is not an object
		"/v1/default/banks/b/knowledge-base/tree?tag_groups=%7B%7D",
		"/v1/default/banks/b/knowledge-base/tree?tag_groups=%5B1%5D",
		"/v1/default/banks/b/knowledge-base/search?q=x&tag_groups=%7B%7D",
		"/v1/default/banks/b/knowledge-base/search?q=x&tag_groups=%5B1%5D",
	} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), "validation error") {
			t.Fatalf("%s body %q lacks the validation report", path, rec.Body.String())
		}
	}
}
