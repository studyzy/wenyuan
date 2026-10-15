package reporting

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

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
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	createKnowledgePage(t, mux, base, `{"name":"Empty","source_query":"q"}`)
	createKnowledgePage(t, mux, base, `{"name":"Deploy","content":"  first line  "}`)

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
	if node.Name != "Release" || node.Description != "How do we release?" {
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
	if read.Name != "Release" || read.Type != "runbook" || read.Description != "How do we release?" {
		t.Fatalf("page = %+v", read)
	}
}

// The tree honors the flat tag filter and its match mode.
func TestKnowledgeTreeTagFilter(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	createKnowledgePage(t, mux, base, `{"name":"Alpha","tags":["team:blue"]}`)
	createKnowledgePage(t, mux, base, `{"name":"Beta","tags":["team:red"]}`)

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

	// A malformed tag_groups is the reference's 422.
	if rec := send(mux, http.MethodGet, base+"/knowledge-base/tree?tag_groups=notjson", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("bad tag_groups = %d, want 422: %s", rec.Code, rec.Body.String())
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
	createKnowledgePage(t, mux, base, `{"name":"Alpha","tags":["name:thee"]}`)

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
	createKnowledgePage(t, mux, base, `{"name":"Deploy Blue","tags":["team:blue"]}`)
	createKnowledgePage(t, mux, base, `{"name":"Deploy Red","tags":["team:red"]}`)

	rec := send(mux, http.MethodGet, base+"/knowledge-base/search?q=deploy&tags=team:blue", "")
	var response KnowledgePageSearchResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Name != "Deploy Blue" {
		t.Fatalf("filtered search = %+v", response.Results)
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
	if read.Description != "What is the runbook?" {
		t.Fatalf("description = %q", read.Description)
	}
	if len(read.Tags) != 1 || read.Tags[0] != "ops" {
		t.Fatalf("tags = %v (the type tag must not leak)", read.Tags)
	}
}

// The default page type applies when no `type:<x>` tag is present.
func TestKnowledgePageDefaultType(t *testing.T) {
	mux := newServer(t)
	base := "/v1/default/banks/" + bank
	page := createKnowledgePage(t, mux, base, `{"name":"Plain"}`)
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
	} {
		if _, ok := config[field]; !ok {
			t.Fatalf("BankTemplateConfig missing %q", field)
		}
	}
	trigger := properties(t, defs, "MentalModelTrigger")
	for _, field := range []string{"exclude_mental_models", "fact_types", "min_refresh_interval_seconds"} {
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
