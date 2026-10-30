package reporting

import (
	"math/big"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// KnowledgeExport bundles every page of the bank: a nested index first, then one
// markdown document per page.
func (h *Handlers) KnowledgeExport(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	nodes, err := h.Store.ListKnowledgeNodes(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	models, err := h.knowledgeModels(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	projected := projectKnowledgeNodes(nodes, models)
	// A scoped caller exports only the pages it may read. As in the tree the
	// scope is applied on its own: a hidden page is dropped from the bundle and
	// its history is never read. Without a scope every node is visible and the
	// bundle is unchanged.
	scope := memoryops.ScopeFromRequest(r)
	scoped := len(scope) > 0
	if scoped {
		projected = pruneKnowledgeNodesToScope(projected, scope)
	}
	visible := make(map[string]bool, len(projected))
	for _, node := range projected {
		visible[node.ID] = true
	}
	files := make([]KnowledgeFile, 0, len(projected)+1)
	files = append(files, KnowledgeFile{
		Path:    knowledgeIndexFilename,
		Content: renderKnowledgeIndex(nestKnowledge(projected)),
	})
	for _, node := range nodes {
		if node.Kind != knowledgeKindPage {
			continue
		}
		if scoped && !visible[node.ID] {
			continue
		}
		files = append(files, KnowledgeFile{
			Path:    node.ID + ".md",
			Content: renderKnowledgeDocument(knowledgeDocFields(node, models[node.MentalModelID]), node.Text, false),
		})
		// A page's refresh history travels with it, so a bundle that is read back
		// in keeps the revisions the export carried rather than only the last body.
		if node.MentalModelID == "" {
			continue
		}
		history, err := h.Store.MentalModelHistory(r.Context(), bankID, node.MentalModelID)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		if len(history) == 0 {
			continue
		}
		title := node.Title
		if title == "" {
			title = node.ID
		}
		files = append(files, KnowledgeFile{
			Path:    node.ID + knowledgeLogSuffix,
			Content: renderKnowledgeLog(title, history),
		})
	}
	httpx.WriteJSON(w, http.StatusOK, KnowledgePageBundleResponse{Files: files})
}

// KnowledgeSearch searches the bank's pages.
func (h *Handlers) KnowledgeSearch(w http.ResponseWriter, r *http.Request) {
	// Every parameter is validated before the bank is looked up: in the
	// reference `q` and `limit` are `Query(...)` declarations and `tag_filter`
	// is a `Depends` (api/http.py:7469-7475), so the framework reports a bad one
	// as a 422 before the handler body — and therefore before the bank — is
	// reached. All three are declared in one run, so every bad one is reported in
	// one response, in declaration order; the tag filter's own `tag_groups`
	// failure is the exception, raised as an HTTPException from inside the
	// dependency and replacing the collected list.
	query, limit, filter, items, groupsDetail := knowledgeSearchParams(r)
	if groupsDetail != "" {
		// The tag-filter dependency raised its own HTTPException, so its string
		// detail is the whole response — the 400 of _decode_tag_groups_query,
		// not the framework's 422.
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, groupsDetail))
		return
	}
	if len(items) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
		return
	}
	bankID, bankOK := h.requireBank(w, r)
	if !bankOK {
		return
	}
	filter, err := h.resolveKnowledgeFilter(r.Context(), bankID, filter)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// The caller's resolved read scope joins the filter: search returns pages
	// only, so unlike the tree the scope can simply be AND-ed on. The top level
	// of a group list is an AND, so appending narrows rather than replaces.
	if scope := memoryops.ScopeFromRequest(r); len(scope) > 0 {
		filter.groups = append(filter.groups, fromStorageTagGroups(scope)...)
	}
	// `enable_text_search` is per bank, so it is resolved rather than taken from
	// a constant: a bank that switched the keyword arm off in recall must not
	// keep hitting a text index here. With no embedding either there is no arm
	// left to run, so the search has no answer to give rather than a degraded one.
	search := storage.KnowledgeSearchQuery{
		BankID:         bankID,
		Query:          query,
		Limit:          limit,
		DisableTextArm: !h.knowledgeTextSearchEnabled(r, bankID),
		// The tag filter rides into the storage arms so it narrows before
		// ranking: the returned `limit` hits are always the best matching pages,
		// never a short set left by matching pages that ranked above the window.
		// The reference binds the same clause into each of its arms.
		Tags:      filter.tags,
		TagsMatch: filter.tagsMatch,
		TagGroups: toStorageTagGroups(filter.groups),
	}
	out := make([]KnowledgeSearchResult, 0)
	// Upstream strips the query and answers an all-whitespace one with no results.
	if strings.TrimSpace(query) != "" {
		// With an embedding model configured the query text is embedded so the
		// vector arm can rank by similarity; a failed embedding degrades to the
		// text arm rather than failing the read, matching recall.
		if h.Embeddings != nil {
			if vector, err := h.Embeddings.EmbedQuery(r.Context(), query); err == nil {
				search.Embedding = vector
			}
		}
		hits, err := h.Store.SearchKnowledgeNodes(r.Context(), search)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		models, err := h.knowledgeModels(r.Context(), bankID)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		for _, hit := range hits {
			model, hasModel := models[hit.Node.MentalModelID]
			out = append(out, ProjectKnowledgeSearchHit(hit, model, hasModel))
		}
	}
	httpx.WriteJSON(w, http.StatusOK, KnowledgePageSearchResponse{
		Results: out, Total: len(out),
	})
}

// knowledgeTextSearchEnabled resolves the bank's `enable_text_search` setting,
// which defaults to on: the bank's own override wins, then the caller's tenant
// layer, then the process default.
func (h *Handlers) knowledgeTextSearchEnabled(r *http.Request, bankID string) bool {
	if config, err := h.Store.BankConfig(r.Context(), bankID); err == nil {
		if value, ok := config[bankConfigEnableTextSearch].(bool); ok {
			return value
		}
	}
	if value, ok := httpx.TenantOverridesFrom(r)[bankConfigEnableTextSearch].(bool); ok {
		return value
	}
	if value, ok := h.ProcessDefaults[bankConfigEnableTextSearch].(bool); ok {
		return value
	}
	return true
}

// knowledgeSearchParams reads every declared parameter of the search route and
// returns the ones that were accepted together with the rejection items of the
// ones that were not. The caller reports all of them at once: the reference
// declares `q`, `limit` and the tag filter in a single run, so a request with
// three bad values is one response with three items in declaration order.
//
// `tag_groups` is the exception: the reference's dependency raises an
// HTTPException from inside itself, which replaces the collected items with the
// single string detail, so that failure is reported on its own.
// The third result is the dependency's rendered report detail, which replaces
// the collected items; it is returned so the caller can write it verbatim.
func knowledgeSearchParams(r *http.Request) (string, int, knowledgeTagFilter, []httpx.ValidationError, string) {
	// The tag filter is appended first because it is assembled by a dependency,
	// which the reference runs before the endpoint's own parameters; within the
	// endpoint `q` and `limit` then follow their declaration order.
	var items []httpx.ValidationError
	filter, filterItems, treeDetail := knowledgeTagFilterItems(r)
	if treeDetail != "" {
		// The dependency raised its own HTTPException, so its string detail is
		// the whole response and the collected items never render.
		return "", 0, knowledgeTagFilter{}, nil, treeDetail
	}
	items = append(items, filterItems...)
	query, item := knowledgeSearchQuery(r)
	if item != nil {
		items = append(items, *item)
	}
	values, present := httpx.QueryPresent(r, "limit")
	raw := ""
	if present && len(values) > 0 {
		raw = values[len(values)-1]
	}
	limit, item := knowledgeSearchLimit(raw, present)
	if item != nil {
		items = append(items, *item)
	}
	if len(items) > 0 {
		return "", 0, knowledgeTagFilter{}, items, ""
	}
	return query, limit, filter, nil, ""
}

// knowledgeSearchQuery reads the required `q` parameter. A missing or empty
// value is the reference's 422, never an empty query.
func knowledgeSearchQuery(r *http.Request) (string, *httpx.ValidationError) {
	values, present := httpx.QueryPresent(r, "q")
	if !present || len(values) == 0 {
		// A missing query parameter has no value to report, so its input is null.
		return "", itemPtr(httpx.Missing([]string{"query", "q"}, nil))
	}
	// A scalar query parameter comes from the reference's multimap, whose lookup
	// returns the LAST occurrence, so a repeated `q` binds its final value.
	value := values[len(values)-1]
	if value == "" {
		return "", itemPtr(httpx.StringTooShort([]string{"query", "q"}, "", 1))
	}
	return value, nil
}

// knowledgeSearchLimit reads the `limit` parameter: default 10, range 1..50.
//
// The value is parsed the way the reference's framework parses an integer: an
// unbounded Python int, so a value past 64 bits is still a number and reports
// the range violation rather than a parse failure, and a value written as a
// float with a zero fraction ("5.0") is accepted. The raw text is what the
// rejection echoes back, as the framework echoes the string it was given.
func knowledgeSearchLimit(raw string, present bool) (int, *httpx.ValidationError) {
	if !present {
		return 10, nil
	}
	if strings.TrimSpace(raw) == "" {
		return 0, itemPtr(httpx.TypeMismatch("int_parsing", []string{"query", "limit"}, raw))
	}
	value, ok := frameworkIntFromString(raw)
	if !ok {
		return 0, itemPtr(httpx.TypeMismatch("int_parsing", []string{"query", "limit"}, raw))
	}
	if value.Sign() <= 0 {
		return 0, itemPtr(httpx.GreaterThanEqual([]string{"query", "limit"}, raw, 1))
	}
	if value.Cmp(big.NewInt(50)) > 0 {
		return 0, itemPtr(httpx.LessThanEqual([]string{"query", "limit"}, raw, 50))
	}
	return int(value.Int64()), nil
}

// itemPtr returns a pointer to one validation item, the shape the accumulating
// validators collect.
func itemPtr(item httpx.ValidationError) *httpx.ValidationError { return &item }

// ProjectKnowledgeSearchHit renders one search hit as the result the reference
// reports: identity, the page's own name, the model backing it (its id, the
// question it answers and when it last refreshed), the opening text as a
// snippet, and the fused rank score. It is shared by the HTTP search route and
// the MCP tool, so the two cannot drift.
func ProjectKnowledgeSearchHit(hit storage.KnowledgeHit, model storage.MentalModel, hasModel bool) KnowledgeSearchResult {
	var sourceQuery, updatedAt *string
	if hasModel {
		sourceQuery = &model.SourceQuery
		updatedAt = httpx.FormatTimestampPtr(&model.LastRefreshedAt)
	}
	return KnowledgeSearchResult{
		ID:            hit.Node.ID,
		Name:          hit.Node.Title,
		MentalModelID: optionalString(hit.Node.MentalModelID),
		SourceQuery:   sourceQuery,
		Snippet:       knowledgeSnippet(hit.Node.Text),
		Score:         httpx.PythonFloat(hit.Score),
		UpdatedAt:     updatedAt,
	}
}
