package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// createKnowledgePage creates a page and returns its decoded create response.
func createKnowledgePage(t *testing.T, mux *http.ServeMux, base, body string) CreateKnowledgePageResponse {
	t.Helper()
	rec := send(mux, http.MethodPost, base+"/knowledge-base/pages", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create page: %d, body = %s", rec.Code, rec.Body.String())
	}
	var created CreateKnowledgePageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created
}

// POST /knowledge-base/pages and /folders answer 201, and the page create
// carries the reference's page_id/mental_model_id/operation_id object.
func TestKnowledgeCreateReturns201WithPageIdentity(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	if rec := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"name":"Ops"}`); rec.Code != http.StatusCreated {
		t.Fatalf("folder = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	rec := send(mux, http.MethodPost, base+"/knowledge-base/pages",
		`{"name":"Deploy","source_query":"How do we deploy?"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("page = %d, want 201: %s", rec.Code, rec.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["page_id"].(string); !ok {
		t.Fatalf("page_id missing: %v", payload)
	}
	modelID, ok := payload["mental_model_id"].(string)
	if !ok || modelID == "" {
		t.Fatalf("mental_model_id must be a non-empty string: %v", payload)
	}
}

// A search result carries the page's opening text, or the empty-page notice when
// the body has not been written yet.
func TestKnowledgeSearchSnippet(t *testing.T) {
	mux, store := newServerWithStore(t)
	base := "/v1/default/banks/" + bank
	createKnowledgePage(t, mux, base, `{"name":"Empty","source_query":"q"}`)
	written := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"Deploy question"}`)
	// A create writes no body — the reference generates one on refresh — so the
	// text a snippet shows is seeded directly.
	text := "  first line  "
	if _, err := store.UpdateKnowledgeNode(context.Background(), bank, written.PageID,
		storage.KnowledgeNodePatch{Text: &text}); err != nil {
		t.Fatal(err)
	}

	rec := send(mux, http.MethodGet, base+"/knowledge-base/search?q=deploy", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("search = %d: %s", rec.Code, rec.Body.String())
	}
	var response KnowledgePageSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Snippet != "first line" {
		t.Fatalf("snippet = %+v", response.Results)
	}

	rec = send(mux, http.MethodGet, base+"/knowledge-base/search?q=empty", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Snippet != knowledgeEmptyNotice {
		t.Fatalf("empty snippet = %+v", response.Results)
	}
}

// The export bundle starts with the reserved index.md, rendered as nested
// navigation over the tree.
func TestKnowledgeExportStartsWithIndex(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	folder := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"name":"Ops"}`)
	if folder.Code != http.StatusCreated {
		t.Fatalf("folder = %d: %s", folder.Code, folder.Body.String())
	}
	var folderNode KnowledgeNode
	if err := json.Unmarshal(folder.Body.Bytes(), &folderNode); err != nil {
		t.Fatal(err)
	}
	page := createKnowledgePage(t, mux, base,
		`{"name":"Deploy","source_query":"How do we deploy?","parent_id":"`+folderNode.ID+`"}`)

	rec := send(mux, http.MethodGet, base+"/knowledge-base/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body.String())
	}
	var bundle KnowledgePageBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	if len(bundle.Files) == 0 || bundle.Files[0].Path != knowledgeIndexFilename {
		t.Fatalf("export must start with %s: %+v", knowledgeIndexFilename, bundle.Files)
	}
	index := bundle.Files[0].Content
	wantPrefix := "---\ntype: \"index\"\ntitle: \"Knowledge base\"\n---\n\n# Knowledge base\n\n"
	if !strings.HasPrefix(index, wantPrefix) {
		t.Fatalf("index prefix = %q", index)
	}
	if !strings.Contains(index, "- **Ops/**") {
		t.Fatalf("index must nest the folder: %q", index)
	}
	if !strings.Contains(index, "- [Deploy](./"+page.PageID+".md) \u2014 How do we deploy?") {
		t.Fatalf("index must link the page with its question: %q", index)
	}
}

// PATCH accepts the reference's UpdateNodeRequest fields and reflects them on the
// read surface: a rename, a new source query, tags (which set the page type) and
// max_tokens.
func TestKnowledgeUpdateNodeRequestFields(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"old?"}`)

	body := `{"name":"Release","source_query":"How do we release?","tags":["ops","type:runbook"],"max_tokens":1024,"trigger":{"refresh_cron":"0 * * * *"}}`
	rec := send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}
	var node KnowledgeNode
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.Name != "Release" || node.Description == nil || *node.Description != "How do we release?" {
		t.Fatalf("node = %+v", node)
	}
	// The tree node carries the page's raw tags (the type tag included); only the
	// page read strips the type tag out into `type`.
	if len(node.Tags) != 2 || node.Tags[0] != "ops" || node.Tags[1] != "type:runbook" {
		t.Fatalf("tags = %v", node.Tags)
	}
	if node.Trigger["refresh_cron"] != "0 * * * *" {
		t.Fatalf("trigger = %v", node.Trigger)
	}

	rec = send(mux, http.MethodGet, base+"/knowledge-base/pages/"+page.PageID, "")
	var read KnowledgePageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Name != "Release" || read.Type != "runbook" || read.Description == nil || *read.Description != "How do we release?" {
		t.Fatalf("page = %+v", read)
	}
}

// The tree honors the flat tag filter and its match mode.
func TestKnowledgeTreeTagFilter(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	createKnowledgePage(t, mux, base, `{"name":"Alpha","source_query":"a","tags":["team:blue"]}`)
	createKnowledgePage(t, mux, base, `{"name":"Beta","source_query":"b","tags":["team:red"]}`)

	rec := send(mux, http.MethodGet, base+"/knowledge-base/tree?tags=team:blue", "")
	var tree KnowledgeTreeResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Roots) != 1 || tree.Roots[0].Name != "Alpha" {
		t.Fatalf("tree = %+v", tree.Roots)
	}

	// exact with no tags selects the untagged scope, so nothing tagged matches.
	rec = send(mux, http.MethodGet, base+"/knowledge-base/tree?tags_match=exact", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Roots) != 0 {
		t.Fatalf("exact-untagged tree = %+v", tree.Roots)
	}

	// A compound tag group narrows to its own leaf.
	groups := `[{"tags":["team:blue"],"match":"all_strict"}]`
	rec = send(mux, http.MethodGet, base+"/knowledge-base/tree?tag_groups="+url.QueryEscape(groups), "")
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Roots) != 1 || tree.Roots[0].Name != "Alpha" {
		t.Fatalf("grouped tree = %+v", tree.Roots)
	}

	// A whitespace-only tag_groups is blank to the reference's shared decoder,
	// which strips before its emptiness test (api/http.py:283): no filter, the
	// whole tree.
	rec = send(mux, http.MethodGet, base+"/knowledge-base/tree?tag_groups=%20", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
		t.Fatal(err)
	}
	if len(tree.Roots) != 2 {
		t.Fatalf("whitespace tag_groups tree = %+v", tree.Roots)
	}

	// A malformed tag_groups is the reference's 400.
	if rec := send(mux, http.MethodGet, base+"/knowledge-base/tree?tag_groups=notjson", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad tag_groups = %d, want 400: %s", rec.Code, rec.Body.String())
	}
}

// A fuzzy leaf resolves through the bank's tag vocabulary rather than matching
// literally: the token is folded against the vocabulary first, and the page
// then has to carry one of the tags the token resolved to. So a near-miss token
// reaches the page only once the vocabulary holds its tag, a literal leaf never
// does, and a token with no near vocabulary tag stays unsatisfiable.
func TestKnowledgeTreeFuzzyTagGroup(t *testing.T) {
	mux, store := newServerWithStore(t)
	base := "/v1/default/banks/" + bank
	createKnowledgePage(t, mux, base, `{"name":"Alpha","source_query":"a","tags":["name:thee"]}`)

	roots := func(query string) []KnowledgeNode {
		t.Helper()
		rec := send(mux, http.MethodGet, base+"/knowledge-base/tree?"+query, "")
		var tree KnowledgeTreeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
			t.Fatal(err)
		}
		return tree.Roots
	}

	// A literal reading of the near-miss token never matches.
	if got := roots("tag_groups=" + url.QueryEscape(`[{"tags":["name:the"]}]`)); len(got) != 0 {
		t.Fatalf("a literal leaf must not match the near-miss tag: %+v", got)
	}

	// With no memory tag to resolve against, the fuzzy token resolves to itself
	// and is just as unsatisfiable as the literal read.
	if got := roots("tag_groups=" + url.QueryEscape(`[{"tags":["name:the"],"resolve":"fuzzy"}]`)); len(got) != 0 {
		t.Fatalf("a fuzzy leaf with an empty vocabulary must not match: %+v", got)
	}

	// The vocabulary is the bank's memory tags: once a memory carries the page's
	// tag, the fuzzy token resolves to it and the page is visible.
	if _, err := store.UpsertUnit(context.Background(), storage.MemoryUnit{
		BankID: bank, Text: "The scheme is named thee.", FactType: storage.TypeWorld,
		Tags: []string{"name:thee"},
	}); err != nil {
		t.Fatal(err)
	}
	got := roots("tag_groups=" + url.QueryEscape(`[{"tags":["name:the"],"resolve":"fuzzy"}]`))
	if len(got) != 1 || got[0].Name != "Alpha" {
		t.Fatalf("fuzzy leaf tree = %+v", got)
	}
	// The same leaf read literally still does not match: it is the resolution,
	// not a widened comparison, that found the page.
	if got := roots("tag_groups=" + url.QueryEscape(`[{"tags":["name:the"]}]`)); len(got) != 0 {
		t.Fatalf("a literal leaf must not match a resolved tag: %+v", got)
	}
	// A token with no near vocabulary tag resolves to itself and stays
	// unsatisfiable.
	if got := roots("tag_groups=" + url.QueryEscape(`[{"tags":["name:zzzzzz"],"resolve":"fuzzy"}]`)); len(got) != 0 {
		t.Fatalf("an unresolvable fuzzy token must not match: %+v", got)
	}
}

// Search honors the tag filter too, so a filtered-away page is not returned.
func TestKnowledgeSearchTagFilter(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	createKnowledgePage(t, mux, base, `{"name":"Deploy Blue","source_query":"a","tags":["team:blue"]}`)
	createKnowledgePage(t, mux, base, `{"name":"Deploy Red","source_query":"b","tags":["team:red"]}`)

	rec := send(mux, http.MethodGet, base+"/knowledge-base/search?q=deploy&tags=team:blue", "")
	var response KnowledgePageSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Name != "Deploy Blue" {
		t.Fatalf("filtered search = %+v", response.Results)
	}

	// A whitespace-only tag_groups is blank to the reference's shared decoder,
	// which strips before its emptiness test (api/http.py:283): no filter, so
	// both pages answer the query.
	rec = send(mux, http.MethodGet, base+"/knowledge-base/search?q=deploy&tag_groups=%20", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("whitespace tag_groups search = %+v", response.Results)
	}
}

// Search requires `q` and bounds `limit` with the reference's 422 envelope.
func TestKnowledgeSearchValidation(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	for _, path := range []string{
		"/knowledge-base/search",
		"/knowledge-base/search?q=",
		"/knowledge-base/search?q=x&limit=0",
		"/knowledge-base/search?q=x&limit=",
		"/knowledge-base/search?q=x&limit=51",
		"/knowledge-base/search?q=x&limit=abc",
	} {
		rec := send(mux, http.MethodGet, base+path, "")
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s = %d, want 422: %s", path, rec.Code, rec.Body.String())
		}
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		raw, ok := payload["detail"]
		if !ok {
			raw, ok = payload["errors"]
		}
		if !ok {
			t.Fatalf("%s envelope = %s", path, rec.Body.String())
		}
		var items []struct {
			Loc []string `json:"loc"`
			Msg string   `json:"msg"`
		}
		if err := json.Unmarshal(raw, &items); err != nil {
			t.Fatal(err)
		}
		if len(items) != 1 || len(items[0].Loc) != 2 || items[0].Loc[0] != "query" || items[0].Loc[1] == "" {
			t.Fatalf("%s envelope = %s", path, rec.Body.String())
		}
	}
}

// The page read lifts `type` out of a `type:<x>` tag and takes `description`
// from the node's source query, dropping the type tag from the display tags.
func TestKnowledgePageTypeAndDescription(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base,
		`{"name":"Run","source_query":"What is the runbook?","tags":["ops","type:runbook"]}`)

	rec := send(mux, http.MethodGet, base+"/knowledge-base/pages/"+page.PageID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get page = %d: %s", rec.Code, rec.Body.String())
	}
	var read KnowledgePageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Type != "runbook" {
		t.Fatalf("type = %q", read.Type)
	}
	if read.Description == nil || *read.Description != "What is the runbook?" {
		t.Fatalf("description = %v", read.Description)
	}
	if len(read.Tags) != 1 || read.Tags[0] != "ops" {
		t.Fatalf("tags = %v (the type tag must not leak)", read.Tags)
	}
}

// The default page type applies when no `type:<x>` tag is present.
func TestKnowledgePageDefaultType(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base, `{"name":"Plain","source_query":"q"}`)
	rec := send(mux, http.MethodGet, base+"/knowledge-base/pages/"+page.PageID, "")
	var read KnowledgePageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Type != knowledgeDefaultPageType {
		t.Fatalf("type = %q, want %q", read.Type, knowledgeDefaultPageType)
	}
}

// The bank-template-schema route serves the reference's generated manifest
// schema: its $defs, the manifest example, the full config field set and the
// trigger enums.
func TestBankTemplateSchemaMatchesReference(t *testing.T) {
	mux := newServer(t)
	rec := send(mux, http.MethodGet, "/v1/bank-template-schema", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("schema = %d: %s", rec.Code, rec.Body.String())
	}
	var schema map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &schema); err != nil {
		t.Fatal(err)
	}
	if _, ok := schema["example"]; !ok {
		t.Fatalf("manifest schema must carry the model example")
	}
	if got := stringList(schema["required"]); len(got) != 1 || got[0] != "version" {
		t.Fatalf("manifest required = %v", got)
	}
	defs, ok := schema["$defs"].(map[string]any)
	if !ok {
		t.Fatalf("no $defs: %v", schema)
	}
	for _, name := range []string{
		"BankTemplateConfig", "BankTemplateDirective", "BankTemplateMentalModel",
		"MentalModelTrigger", "LabelGroup", "ConsolidationStrategySpec", "Budget", "TagGroupAnd",
	} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("missing $defs.%s", name)
		}
	}
	config := properties(t, defs, "BankTemplateConfig")
	for _, field := range []string{
		"memory_defense", "recall_budget_function", "reflect_default_options",
		"audit_log_enabled", "store_document_text", "consolidation_strategies",
		// The v1.1.0 fields a stale capture predates.
		"reflect_use_decision_model", "mental_model_refresh_use_decision_model",
		"mental_model_delta_use_decision_model", "default_observation_scopes",
		"consolidation_use_decision_model", "recall_prefer_observations",
	} {
		if _, ok := config[field]; !ok {
			t.Fatalf("BankTemplateConfig missing %q", field)
		}
	}
	trigger := properties(t, defs, "MentalModelTrigger")
	for _, field := range []string{"exclude_mental_models", "fact_types", "min_refresh_interval_seconds", "full_refresh_cron"} {
		if _, ok := trigger[field]; !ok {
			t.Fatalf("MentalModelTrigger missing %q", field)
		}
	}
	mode, _ := trigger["mode"].(map[string]any)
	if got := stringList(mode["enum"]); len(got) != 2 || got[0] != "full" || got[1] != "delta" {
		t.Fatalf("trigger mode enum = %v", got)
	}
	mental, _ := defs["BankTemplateMentalModel"].(map[string]any)
	if got := stringList(mental["required"]); strings.Join(got, ",") != "id,name,source_query" {
		t.Fatalf("mental model required = %v", got)
	}
	maxTokens, _ := properties(t, defs, "BankTemplateMentalModel")["max_tokens"].(map[string]any)
	if maxTokens["default"] != float64(2048) {
		t.Fatalf("max_tokens default = %v", maxTokens["default"])
	}
}

// properties returns the top-level properties of one $defs entry.
func properties(t *testing.T, defs map[string]any, name string) map[string]any {
	t.Helper()
	entry, _ := defs[name].(map[string]any)
	props, _ := entry["properties"].(map[string]any)
	if props == nil {
		t.Fatalf("no properties for %s", name)
	}
	return props
}

// stringList converts a decoded JSON array of strings.
func stringList(value any) []string {
	items, _ := value.([]any)
	out := make([]string, 0, len(items))
	for _, item := range items {
		text, _ := item.(string)
		out = append(out, text)
	}
	return out
}

// A create body is validated the way the reference's model is: the required
// members differ between a folder and a page, an absent one is a structured
// `missing` item naming the whole body, and a body that is not JSON is
// `json_invalid`. A `title`/`content` spelling is not a member, so it neither
// supplies a required field nor a page body.
func TestKnowledgeCreateValidation(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank

	cases := []struct {
		name   string
		path   string
		body   string
		status int
		items  []struct {
			Type  string         `json:"type"`
			Loc   []string       `json:"loc"`
			Input map[string]any `json:"input"`
		}
	}{
		{
			name: "page without name or source query", path: "/knowledge-base/pages", body: `{}`,
			status: http.StatusUnprocessableEntity,
			items: []struct {
				Type  string         `json:"type"`
				Loc   []string       `json:"loc"`
				Input map[string]any `json:"input"`
			}{
				{"missing", []string{"body", "name"}, map[string]any{}},
				{"missing", []string{"body", "source_query"}, map[string]any{}},
			},
		},
		{
			name: "page without a source query", path: "/knowledge-base/pages", body: `{"name":"Deploy"}`,
			status: http.StatusUnprocessableEntity,
		},
		{
			name: "folder without a name", path: "/knowledge-base/folders", body: `{}`,
			status: http.StatusUnprocessableEntity,
		},
		{
			name: "folder titled instead of named", path: "/knowledge-base/folders", body: `{"title":"X"}`,
			status: http.StatusUnprocessableEntity,
		},
		{
			name: "malformed body", path: "/knowledge-base/folders", body: `{not json`,
			status: http.StatusUnprocessableEntity,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(mux, http.MethodPost, base+tc.path, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.status, rec.Body.String())
			}
			if len(tc.items) == 0 {
				return
			}
			var payload struct {
				Detail []struct {
					Type  string         `json:"type"`
					Loc   []string       `json:"loc"`
					Input map[string]any `json:"input"`
				} `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if len(payload.Detail) != len(tc.items) {
				t.Fatalf("detail = %+v", payload.Detail)
			}
			for i, want := range tc.items {
				got := payload.Detail[i]
				if got.Type != want.Type || strings.Join(got.Loc, ".") != strings.Join(want.Loc, ".") {
					t.Fatalf("item %d = %+v, want %+v", i, got, want)
				}
			}
		})
	}
}

// A `content` member is not part of the create body, so a page is created with
// an empty body and its document read says so in words.
func TestKnowledgeCreateIgnoresContentMember(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base,
		`{"name":"X","source_query":"q","content":"hello world","text":"hello again"}`)
	rec := send(mux, http.MethodGet, base+"/knowledge-base/pages/"+page.PageID, "")
	var read struct {
		Body     *string `json:"body"`
		Markdown string  `json:"markdown"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &read); err != nil {
		t.Fatal(err)
	}
	if read.Body == nil || *read.Body != "" {
		t.Fatalf("body = %v, want an empty body", read.Body)
	}
	if !strings.Contains(read.Markdown, knowledgeEmptyNotice) {
		t.Fatalf("markdown must carry the empty notice: %q", read.Markdown)
	}
}

// A parent must be an existing folder: a missing parent, an empty parent id and
// a parent that is a page are all the reference's 400 with its own wording.
func TestKnowledgeParentMustBeAFolder(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"q"}`)

	cases := []struct {
		name string
		path string
		body string
		code int
		want string
	}{
		{"page parent on a folder", "/knowledge-base/folders",
			`{"name":"Nested","parent_id":"` + page.PageID + `"}`,
			http.StatusBadRequest, "Parent '" + page.PageID + "' is not a folder"},
		{"missing parent on a folder", "/knowledge-base/folders",
			`{"name":"Nested","parent_id":"kf-nope"}`,
			http.StatusBadRequest, "Parent folder 'kf-nope' not found"},
		{"empty parent", "/knowledge-base/folders",
			`{"name":"Nested","parent_id":""}`,
			http.StatusBadRequest, "Parent folder '' not found"},
		{"page under a page", "/knowledge-base/pages",
			`{"name":"Nested","source_query":"q","parent_id":"` + page.PageID + `"}`,
			http.StatusBadRequest, "Parent '" + page.PageID + "' is not a folder"},
		{"missing parent on a page", "/knowledge-base/pages",
			`{"name":"Nested","source_query":"q","parent_id":"kf-nope"}`,
			http.StatusBadRequest, "Parent folder 'kf-nope' not found"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := send(mux, http.MethodPost, base+tc.path, tc.body)
			if rec.Code != tc.code {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tc.want) {
				t.Fatalf("detail = %s, want %q", rec.Body.String(), tc.want)
			}
		})
	}
}

// A page name is unique within its folder without regard to case, so a second
// page differing only in case is the reference's 409.
func TestKnowledgeDuplicatePageIsCaseInsensitive(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"q"}`)
	rec := send(mux, http.MethodPost, base+"/knowledge-base/pages", `{"name":"deploy","source_query":"q"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate = %d, want 409: %s", rec.Code, rec.Body.String())
	}
}

// A node patch that changes nothing is refused before the node is read, an
// absent parent_id keeps the parent while an explicit null moves the node to
// the root, and a move that would create a cycle is refused.
func TestKnowledgeNodeUpdateRules(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	folder := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"name":"Ops"}`)
	var folderNode KnowledgeNode
	if err := json.Unmarshal(folder.Body.Bytes(), &folderNode); err != nil {
		t.Fatal(err)
	}
	page := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"q"}`)

	rec := send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID, `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(),
		"Provide name, parent_id, source_query, tags, max_tokens, and/or trigger to update") {
		t.Fatalf("empty patch = %d: %s", rec.Code, rec.Body.String())
	}
	// The no-op check runs before the read, so an unknown node reports the same.
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/unknown", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty patch on unknown node = %d: %s", rec.Code, rec.Body.String())
	}

	// Move under the folder, then back to the root with an explicit null.
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID,
		`{"parent_id":"`+folderNode.ID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", rec.Code, rec.Body.String())
	}
	var node KnowledgeNode
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.ParentID == nil || *node.ParentID != folderNode.ID {
		t.Fatalf("parent = %v, want %s", node.ParentID, folderNode.ID)
	}
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID, `{"parent_id":null}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.ParentID != nil {
		t.Fatalf("parent = %v, want the root", node.ParentID)
	}
	// An absent parent_id leaves the stored parent alone.
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID,
		`{"parent_id":"`+folderNode.ID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("move = %d: %s", rec.Code, rec.Body.String())
	}
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID, `{"name":"Deploy 2"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.ParentID == nil || *node.ParentID != folderNode.ID {
		t.Fatalf("a rename must not move the node: %v", node.ParentID)
	}

	// A node cannot be its own parent, and a move into its own subtree is refused.
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+folderNode.ID,
		`{"parent_id":"`+folderNode.ID+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "own parent") {
		t.Fatalf("self parent = %d: %s", rec.Code, rec.Body.String())
	}
	nested := send(mux, http.MethodPost, base+"/knowledge-base/folders",
		`{"name":"Nested","parent_id":"`+folderNode.ID+`"}`)
	if nested.Code != http.StatusCreated {
		t.Fatalf("nested folder = %d: %s", nested.Code, nested.Body.String())
	}
	var nestedNode KnowledgeNode
	if err := json.Unmarshal(nested.Body.Bytes(), &nestedNode); err != nil {
		t.Fatal(err)
	}
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+folderNode.ID,
		`{"parent_id":"`+nestedNode.ID+`"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "own subtree") {
		t.Fatalf("cycle = %d: %s", rec.Code, rec.Body.String())
	}

	// Page options do not exist on a folder, so asking for them is a not-found
	// naming the node.
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+folderNode.ID, `{"source_query":"q"}`)
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), "Knowledge node '"+folderNode.ID+"' not found") {
		t.Fatalf("folder page options = %d: %s", rec.Code, rec.Body.String())
	}
}

// A trigger patch updates the members the client stated and keeps the rest, and
// a new source query re-queues a refresh so the page rebuilds.
func TestKnowledgeNodeUpdatePatchesTriggerAndRequeues(t *testing.T) {
	mux, store := newServerWithStore(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"q"}`)

	// Clear the create's own refresh, so the queued operation after the patch is
	// the one the new source query scheduled rather than the one it deduped into.
	if _, err := store.CancelOperation(context.Background(), bank, page.OperationID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteOperation(context.Background(), bank, page.OperationID); err != nil {
		t.Fatal(err)
	}
	before := refreshOperations(t, store)
	rec := send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+page.PageID,
		`{"source_query":"a new question","trigger":{"refresh_cron":"0 3 * * *"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d: %s", rec.Code, rec.Body.String())
	}
	var node KnowledgeNode
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.Trigger["refresh_cron"] != "0 3 * * *" {
		t.Fatalf("trigger = %v", node.Trigger)
	}
	// The page default's members survive the patch; naming a cron clears the
	// auto-refresh the page was created with, since a page refreshes on one or
	// the other, never both.
	if node.Trigger["mode"] != "delta" || node.Trigger["exclude_mental_models"] != true {
		t.Fatalf("a trigger patch must keep the unstated members: %v", node.Trigger)
	}
	if node.Trigger["refresh_after_consolidation"] != false {
		t.Fatalf("refresh_cron must clear refresh_after_consolidation: %v", node.Trigger)
	}
	if got := refreshOperations(t, store); got <= before {
		t.Fatalf("a new source query must queue a refresh: %d -> %d", before, got)
	}
}

// refreshOperations counts the bank's queued mental-model refreshes.
func refreshOperations(t *testing.T, store *storage.MemoryStore) int {
	t.Helper()
	page, err := store.ListOperations(context.Background(), storage.OperationQuery{
		BankID: bank, Type: storage.OperationTypeRefreshMentalModel, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	return page.Total
}

// The tree reports staleness for a page and null for a folder, and a page whose
// last refresh failed carries the failure time.
func TestKnowledgeTreeStalenessAndRefreshFailure(t *testing.T) {
	mux, store := newServerWithStore(t)
	base := "/v1/default/banks/" + bank
	if rec := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"name":"Ops"}`); rec.Code != http.StatusCreated {
		t.Fatalf("folder = %d", rec.Code)
	}
	page := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"q"}`)

	failedAt := time.Now().UTC().Truncate(time.Microsecond)
	model, err := store.GetMentalModel(context.Background(), bank, page.MentalModelID)
	if err != nil {
		t.Fatal(err)
	}
	model.LastRefreshFailedAt = &failedAt
	if _, err := store.PutMentalModel(context.Background(), model); err != nil {
		t.Fatal(err)
	}

	rec := send(mux, http.MethodGet, base+"/knowledge-base/tree", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tree = %d: %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Roots []map[string]json.RawMessage `json:"roots"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, root := range payload.Roots {
		var kind string
		if err := json.Unmarshal(root["kind"], &kind); err != nil {
			t.Fatal(err)
		}
		_, hasStale := root["is_stale"]
		if !hasStale {
			t.Fatalf("%s must always carry is_stale: %v", kind, root)
		}
		if kind == string(knowledgeKindFolder) {
			if string(root["is_stale"]) != "null" {
				t.Fatalf("a folder must report null staleness: %v", root["is_stale"])
			}
			continue
		}
		if string(root["is_stale"]) == "null" {
			t.Fatalf("a page must report its staleness: %v", root["is_stale"])
		}
		want := httpx.FormatTimestamp(failedAt)
		var got *string
		if err := json.Unmarshal(root["last_refresh_failed_at"], &got); err != nil {
			t.Fatal(err)
		}
		if got == nil || *got != want {
			t.Fatalf("last_refresh_failed_at = %v, want %s", got, want)
		}
	}
}

// The export bundle carries a page's refresh history as its own `<id>.log.md`.
func TestKnowledgeExportEmitsHistoryLogs(t *testing.T) {
	mux, store := newServerWithStore(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"q"}`)

	previous := "the old body"
	if err := store.RecordMentalModelHistory(context.Background(), bank, page.MentalModelID,
		storage.MentalModelHistoryEntry{ChangedAt: time.Now().UTC(), PreviousContent: &previous}); err != nil {
		t.Fatal(err)
	}

	rec := send(mux, http.MethodGet, base+"/knowledge-base/export", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", rec.Code, rec.Body.String())
	}
	var bundle KnowledgePageBundleResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
		t.Fatal(err)
	}
	var log *KnowledgeFile
	for i := range bundle.Files {
		if bundle.Files[i].Path == page.PageID+knowledgeLogSuffix {
			log = &bundle.Files[i]
		}
	}
	if log == nil {
		t.Fatalf("no history log in %+v", bundle.Files)
	}
	wantPrefix := "---\ntype: \"log\"\ntitle: \"Deploy \u2014 history\"\n---\n\n# Deploy \u2014 history\n\n"
	if !strings.HasPrefix(log.Content, wantPrefix) {
		t.Fatalf("log prefix = %q", log.Content)
	}
	if !strings.Contains(log.Content, "the old body") {
		t.Fatalf("log must carry the replaced content: %q", log.Content)
	}
}

// A knowledge member route answers the resource's own not-found on an unknown
// bank, the way the reference does: it looks the page or node up and reports
// that, rather than pre-checking the bank. The tree route keeps the bank check.
func TestKnowledgeMemberRoutesAnswerResourceNotFoundOnUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "ghostbank")
			for _, param := range []string{"page_id", "node_id"} {
				if strings.Contains(pattern, "{"+param+"}") {
					r.SetPathValue(param, "kp-x")
				}
			}
			handler(w, r)
		})
	})
	base := "/v1/default/banks/ghostbank/knowledge-base"
	cases := []struct {
		method, path, body, detail string
	}{
		{http.MethodGet, base + "/pages/kp-x", "", "Knowledge page 'kp-x' not found"},
		{http.MethodPatch, base + "/nodes/kp-x", `{"name":"n"}`, "Knowledge node 'kp-x' not found"},
		{http.MethodDelete, base + "/nodes/kp-x", "", "Knowledge node 'kp-x' not found"},
	}
	for _, tc := range cases {
		rec := send(mux, tc.method, tc.path, tc.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s: %d, want 404 (body %s)", tc.method, tc.path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), tc.detail) {
			t.Fatalf("%s %s detail = %s, want %q", tc.method, tc.path, rec.Body.String(), tc.detail)
		}
	}
	if rec := send(mux, http.MethodGet, base+"/tree", ""); rec.Code != http.StatusNotFound ||
		!strings.Contains(rec.Body.String(), "Bank 'ghostbank' not found") {
		t.Fatalf("tree on an unknown bank: %d %s", rec.Code, rec.Body.String())
	}
}

// createScopedPage creates a page through a scoped handler, returning its id.
func createScopedPage(t *testing.T, handler http.Handler, apiKey, base, body string) string {
	t.Helper()
	rec := sendKeyedBody(handler, apiKey, http.MethodPost, base+"/knowledge-base/pages", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create page as %q: %d, body = %s", apiKey, rec.Code, rec.Body.String())
	}
	var created CreateKnowledgePageResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	return created.PageID
}

// createScopedFolder creates a folder through a scoped handler, returning its id.
func createScopedFolder(t *testing.T, handler http.Handler, apiKey, base, name string) string {
	t.Helper()
	rec := sendKeyedBody(handler, apiKey, http.MethodPost, base+"/knowledge-base/folders", `{"name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create folder as %q: %d, body = %s", apiKey, rec.Code, rec.Body.String())
	}
	var node KnowledgeNode
	if err := json.Unmarshal(rec.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	return node.ID
}

// The knowledge tree is pruned by the caller's resolved read scope: a page
// outside the scope is hidden, a folder left holding only out-of-scope pages is
// dropped, and a folder the scope leaves empty stays visible so its creator can
// fill it. An unscoped caller sees the whole tree.
func TestKnowledgeTreeHonorsTagScope(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	handler := scopedHandler(t, store)
	base := "/v1/default/banks/" + bank

	// A folder holding only kate's page, and an empty folder dan just made.
	kateFolder := createScopedFolder(t, handler, "kate", base, "Kate")
	createScopedPage(t, handler, "kate", base,
		`{"name":"Kate Page","source_query":"q","parent_id":"`+kateFolder+`","tags":["user:kate"]}`)
	createScopedPage(t, handler, "dan", base, `{"name":"Dan Page","source_query":"q","tags":["user:dan"]}`)
	createScopedFolder(t, handler, "dan", base, "Dan Empty")

	roots := func(apiKey string) map[string]KnowledgeNode {
		t.Helper()
		rec := sendKeyed(handler, apiKey, http.MethodGet, base+"/knowledge-base/tree")
		if rec.Code != http.StatusOK {
			t.Fatalf("tree as %q = %d: %s", apiKey, rec.Code, rec.Body.String())
		}
		var tree KnowledgeTreeResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &tree); err != nil {
			t.Fatal(err)
		}
		byName := map[string]KnowledgeNode{}
		for _, node := range tree.Roots {
			byName[node.Name] = node
		}
		return byName
	}

	// An administrator (no key) sees every node.
	if admin := roots(""); len(admin) != 3 {
		t.Fatalf("unscoped tree roots = %d, want 3: %+v", len(admin), admin)
	}
	// Dan sees his own page and his empty folder; kate's folder holds only her
	// page, so it is dropped whole.
	dan := roots("dan")
	if _, ok := dan["Dan Page"]; !ok {
		t.Fatalf("dan must see his own page: %+v", dan)
	}
	if _, ok := dan["Dan Empty"]; !ok {
		t.Fatalf("dan must still see the empty folder he made: %+v", dan)
	}
	if _, ok := dan["Kate"]; ok {
		t.Fatalf("dan must not see kate's folder: %+v", dan)
	}
	// Kate sees her folder and its page, and the empty folder (no one's yet),
	// but never dan's page.
	kate := roots("kate")
	if folder, ok := kate["Kate"]; !ok || len(folder.Children) != 1 || folder.Children[0].Name != "Kate Page" {
		t.Fatalf("kate's folder must keep its page: %+v", kate)
	}
	if _, ok := kate["Dan Page"]; ok {
		t.Fatalf("kate must not see dan's page: %+v", kate)
	}
}

// The knowledge export bundle is pruned by the caller's resolved read scope: a
// page outside the scope is dropped, its markdown and its refresh history never
// read. An unscoped caller exports every page.
func TestKnowledgeExportHonorsTagScope(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	handler := scopedHandler(t, store)
	base := "/v1/default/banks/" + bank

	katePage := createScopedPage(t, handler, "kate", base, `{"name":"Kate Page","source_query":"q","tags":["user:kate"]}`)
	danPage := createScopedPage(t, handler, "dan", base, `{"name":"Dan Page","source_query":"q","tags":["user:dan"]}`)

	paths := func(apiKey string) map[string]bool {
		t.Helper()
		rec := sendKeyed(handler, apiKey, http.MethodGet, base+"/knowledge-base/export")
		if rec.Code != http.StatusOK {
			t.Fatalf("export as %q = %d: %s", apiKey, rec.Code, rec.Body.String())
		}
		var bundle KnowledgePageBundleResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &bundle); err != nil {
			t.Fatal(err)
		}
		present := map[string]bool{}
		for _, file := range bundle.Files {
			present[file.Path] = true
		}
		return present
	}

	admin := paths("")
	if !admin[katePage+".md"] || !admin[danPage+".md"] {
		t.Fatalf("unscoped export must carry both pages: %+v", admin)
	}
	dan := paths("dan")
	if dan[katePage+".md"] {
		t.Fatalf("dan's export leaked kate's page: %+v", dan)
	}
	if !dan[danPage+".md"] {
		t.Fatalf("dan's export must carry his own page: %+v", dan)
	}
}

// Clearing a memory's observations is gated by the caller's resolved scope:
// outside its read scope the memory reads as not-found, and a readable memory
// whose tags the caller may not write is refused with 403. An unscoped caller
// is unaffected.
func TestClearMemoryObservationsHonorsTagScope(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	for _, unit := range []storage.MemoryUnit{
		{ID: "m-kate", BankID: bank, Text: "Kate is interviewing", FactType: storage.TypeWorld, Tags: []string{"user:kate"}},
		// Readable by dan (kind:rule is in his read scope) but not writable by him.
		{ID: "m-rule", BankID: bank, Text: "Nobody deploys on Fridays", FactType: storage.TypeWorld, Tags: []string{"kind:rule"}},
	} {
		if _, err := store.UpsertUnit(ctx, unit); err != nil {
			t.Fatal(err)
		}
	}
	handler := scopedHandler(t, store)
	base := "/v1/default/banks/" + bank

	// dan cannot read kate's memory at all: the scope hides it behind the 404.
	rec := sendKeyed(handler, "dan", http.MethodDelete, base+"/memories/m-kate/observations")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("dan clearing kate's memory = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	// dan can read the shared rule memory but may not write it (his write scope
	// is user:dan), so the clear is refused with the reference's 403.
	rec = sendKeyed(handler, "dan", http.MethodDelete, base+"/memories/m-rule/observations")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("dan clearing a rule memory = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "You can't write memory 'm-rule' tagged kind:rule") {
		t.Fatalf("403 detail = %s", rec.Body.String())
	}
	// kate may read and write her own memory.
	rec = sendKeyed(handler, "kate", http.MethodDelete, base+"/memories/m-kate/observations")
	if rec.Code != http.StatusOK {
		t.Fatalf("kate clearing her memory = %d, want 200: %s", rec.Code, rec.Body.String())
	}
	// An unscoped caller (an administrator) is unaffected: even an unknown memory
	// keeps its current 200/deleted_count=0 answer.
	rec = sendKeyed(handler, "", http.MethodDelete, base+"/memories/00000000-0000-0000-0000-000000000000/observations")
	if rec.Code != http.StatusOK {
		t.Fatalf("unscoped clear of an unknown memory = %d, want 200: %s", rec.Code, rec.Body.String())
	}
}

// A malformed `tag_groups` is the reference's 400 with the errors-repr detail
// of `_decode_tag_groups_query` (api/http.py:284-286) — the same body the
// entity routes send, not the validation-item list envelope the other query
// errors use.
func TestKnowledgeTagGroupsInvalidDetailIsBadRequest(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	for _, path := range []string{
		"/knowledge-base/tree?tag_groups=%5B%7B",
		"/knowledge-base/search?q=x&tag_groups=%5B%7B",
	} {
		rec := send(mux, http.MethodGet, base+path, "")
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s = %d, want 400: %s", path, rec.Code, rec.Body.String())
		}
		var envelope struct {
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s: decode detail: %v (%s)", path, err, rec.Body.String())
		}
		want := `Invalid tag_groups: [{'type': 'json_invalid', 'loc': (), ` +
			`'msg': 'Invalid JSON: EOF while parsing an object at line 1 column 2', 'input': '[{', ` +
			`'ctx': {'error': 'EOF while parsing an object at line 1 column 2'}}]`
		if envelope.Detail != want {
			t.Fatalf("%s detail = %q, want %q", path, envelope.Detail, want)
		}
	}
}

// A repeated scalar query parameter binds its LAST occurrence, the reference's
// multimap lookup, so `limit=5&limit=99` is validated against 99 and a trailing
// empty `q=` after a value still fails the non-empty check.
func TestKnowledgeSearchRepeatedParamLastWins(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	rec := send(mux, http.MethodGet, base+"/knowledge-base/search?q=x&limit=5&limit=99", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repeated limit = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	rec = send(mux, http.MethodGet, base+"/knowledge-base/search?q=x&q=", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("repeated q = %d, want 422: %s", rec.Code, rec.Body.String())
	}
}

// The frontmatter keeps a key whose value is present but empty. The reference
// builds its mapping with a page's name and source query and drops only the
// None members (api/page_markdown.py:frontmatter/render_frontmatter), so a page
// created with an empty name renders `title: ""` — treating "" as absent deleted
// the line, which is what the export bundle and the page read showed.
func TestKnowledgeFrontmatterKeepsPresentButEmptyScalars(t *testing.T) {
	fields := knowledgeDocumentFields("kp-1", "knowledge-page", "", knowledgeOptional{text: "", known: true},
		"2026-01-01T00:00:00+00:00", nil)
	block := renderKnowledgeFrontmatter(fields)
	for _, want := range []string{
		`id: "kp-1"`,
		`type: "knowledge-page"`,
		`title: ""`,
		`description: ""`,
		`timestamp: "2026-01-01T00:00:00+00:00"`,
	} {
		if !strings.Contains(block, want) {
			t.Fatalf("frontmatter lacks %q:\n%s", want, block)
		}
	}
	// The tags list stays dropped when empty, as the reference does for [].
	if strings.Contains(block, "tags:") {
		t.Fatalf("empty tags list must not render:\n%s", block)
	}
	// A member the reference has as None is still dropped.
	absent := renderKnowledgeFrontmatter(knowledgeDocumentFields("kp-1", "knowledge-page", "n",
		knowledgeOptional{}, "", nil))
	if strings.Contains(absent, "description:") || strings.Contains(absent, "timestamp:") {
		t.Fatalf("null members must not render:\n%s", absent)
	}
}

// The path parameters a knowledge read binds are the client's text, verbatim:
// the reference declares them as plain strings, so surrounding whitespace names
// nothing (and the not-found detail echoes it back) instead of being trimmed
// into a name that exists.
func TestKnowledgePageIDIsBoundVerbatim(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	created := createKnowledgePage(t, mux, base, `{"name":"Deploy","source_query":"q"}`)

	rec := send(mux, http.MethodGet, base+"/knowledge-base/pages/%20"+created.PageID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("padded page id = %d, want 404: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `Knowledge page ' `+created.PageID+`' not found`) {
		t.Fatalf("not-found detail must echo the id verbatim: %s", rec.Body.String())
	}
	// A space alone is a page id too, and the detail echoes it rather than "".
	rec = send(mux, http.MethodGet, base+"/knowledge-base/pages/%20", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `Knowledge page ' ' not found`) {
		t.Fatalf("space page id = %d %s", rec.Code, rec.Body.String())
	}
}

// A padded bank identifier is not the bank: the reference binds the path
// parameter verbatim, so ` rt-b ` is a bank nobody created and a read answers
// 404 rather than silently succeeding against `rt-b`.
func TestKnowledgeBankIDIsBoundVerbatim(t *testing.T) {
	// The mux is the served one rather than the recording harness: the harness
	// overwrites {bank_id} with the fixture name to keep its route matches
	// simple, which is exactly the value this case has to observe.
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	base := "/v1/default/banks/" + bank
	if rec := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"name":"Ops"}`); rec.Code != http.StatusCreated {
		t.Fatalf("folder = %d: %s", rec.Code, rec.Body.String())
	}
	padded := "/v1/default/banks/%20" + bank + "%20/knowledge-base"
	for _, path := range []string{padded + "/tree", padded + "/export", padded + "/search?q=x"} {
		rec := send(mux, http.MethodGet, path, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s = %d, want 404: %s", path, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), `Bank ' `+bank+` ' not found`) {
			t.Fatalf("%s detail must echo the id verbatim: %s", path, rec.Body.String())
		}
	}
}

// An empty `trigger` object is a supplied page option, not an absent one.
//
// The reference passes the body's trigger through `model_dump(exclude_unset=True)`,
// which turns `{"trigger": {}}` into `{}` rather than None, and its no-op guard
// tests `trigger is not None` (engine/memory_engine.py:19917-19918). So
// `{"trigger": {}}` is a real (empty) patch: it passes the guard and reaches the
// node lookup, answering the node's own answer. Treating the empty object as
// "not supplied" answered the "nothing to update" 400 instead — a status the
// reference never sends for that body.
func TestKnowledgeNodeEmptyTriggerObjectIsSupplied(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	folder := send(mux, http.MethodPost, base+"/knowledge-base/folders", `{"name":"OpsT"}`)
	var folderNode KnowledgeNode
	if err := json.Unmarshal(folder.Body.Bytes(), &folderNode); err != nil {
		t.Fatal(err)
	}

	// An existing node accepts it (nothing to change, so the response is the node).
	rec := send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+folderNode.ID, `{"trigger":{}}`)
	if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "Provide name, parent_id") {
		t.Fatalf("empty trigger object = %d: %s; it is a supplied option, not a no-op",
			rec.Code, rec.Body.String())
	}
	// An unknown node now reports the node's own answer instead of the no-op 400.
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/kf-unknown", `{"trigger":{}}`)
	if rec.Code == http.StatusBadRequest && strings.Contains(rec.Body.String(), "Provide name, parent_id") {
		t.Fatalf("empty trigger object on an unknown node = %d: %s; the no-op guard must not fire",
			rec.Code, rec.Body.String())
	}
	// A truly empty body is still the no-op 400.
	rec = send(mux, http.MethodPatch, base+"/knowledge-base/nodes/"+folderNode.ID, `{}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Provide name, parent_id") {
		t.Fatalf("empty body = %d: %s", rec.Code, rec.Body.String())
	}
}
