package reporting

import (
	"net/http"
	"strconv"
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
	// reached. `q` is checked first because it is declared first.
	query, ok := knowledgeSearchQuery(w, r)
	if !ok {
		return
	}
	limit, ok := knowledgeSearchLimit(w, r)
	if !ok {
		return
	}
	filter, ok := knowledgeTagFilterFromRequest(w, r)
	if !ok {
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
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
			var sourceQuery, updatedAt *string
			if hasModel {
				sourceQuery = &model.SourceQuery
				updatedAt = httpx.FormatTimestampPtr(&model.LastRefreshedAt)
			}
			out = append(out, KnowledgeSearchResult{
				ID:            hit.Node.ID,
				Name:          hit.Node.Title,
				MentalModelID: optionalString(hit.Node.MentalModelID),
				SourceQuery:   sourceQuery,
				Snippet:       knowledgeSnippet(hit.Node.Text),
				Score:         hit.Score,
				UpdatedAt:     updatedAt,
			})
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

// knowledgeSearchQuery reads the required `q` parameter. A missing or empty
// value is the reference's 422, never an empty query.
func knowledgeSearchQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	values, present := httpx.QueryPresent(r, "q")
	if !present || len(values) == 0 {
		// A missing query parameter has no value to report, so its input is null.
		writeKnowledgeItem(w, r, httpx.Missing([]string{"query", "q"}, nil))
		return "", false
	}
	// A scalar query parameter comes from the reference's multimap, whose lookup
	// returns the LAST occurrence, so a repeated `q` binds its final value.
	value := values[len(values)-1]
	if value == "" {
		writeKnowledgeItem(w, r, httpx.StringTooShort([]string{"query", "q"}, "", 1))
		return "", false
	}
	return value, true
}

// knowledgeSearchLimit reads the `limit` parameter: default 10, range 1..50. An
// explicitly empty value is not the same as an absent one — the reference parses
// the raw string, so `limit=` fails to parse and is its `int_parsing` 422.
func knowledgeSearchLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	// The reference reads the scalar from a multimap, so a repeated `limit` binds
	// its LAST occurrence; an explicitly empty value is not the same as an absent
	// one — the raw string is parsed, so `limit=` is its `int_parsing` 422.
	values, present := httpx.QueryPresent(r, "limit")
	raw := ""
	if present && len(values) > 0 {
		raw = strings.TrimSpace(values[len(values)-1])
	}
	if raw == "" {
		if !present {
			return 10, true
		}
		writeKnowledgeItem(w, r, httpx.TypeMismatch("int_parsing", []string{"query", "limit"}, ""))
		return 0, false
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeKnowledgeItem(w, r, httpx.TypeMismatch("int_parsing", []string{"query", "limit"}, raw))
		return 0, false
	}
	if value < 1 {
		writeKnowledgeItem(w, r, httpx.GreaterThanEqual([]string{"query", "limit"}, raw, 1))
		return 0, false
	}
	if value > 50 {
		writeKnowledgeItem(w, r, httpx.LessThanEqual([]string{"query", "limit"}, raw, 50))
		return 0, false
	}
	return value, true
}
