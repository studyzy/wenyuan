package reporting

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
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
	files := make([]KnowledgeFile, 0, len(nodes)+1)
	files = append(files, KnowledgeFile{
		Path:    knowledgeIndexFilename,
		Content: renderKnowledgeIndex(nestKnowledge(projected)),
	})
	for _, node := range nodes {
		if node.Kind != knowledgeKindPage {
			continue
		}
		files = append(files, KnowledgeFile{
			Path:    node.ID + ".md",
			Content: renderKnowledgeDocument(knowledgeDocFields(node, models[node.MentalModelID]), node.Text, false),
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgePageBundleResponse{Files: files})
}

// KnowledgeSearch searches the bank's pages.
func (h *Handlers) KnowledgeSearch(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
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
	filter, err := h.resolveKnowledgeFilter(r.Context(), bankID, filter)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]KnowledgeSearchResult, 0)
	// Upstream strips the query and answers an all-whitespace one with no results.
	if strings.TrimSpace(query) != "" {
		// Over-fetch so a tag filter applied after ranking can still fill `limit`.
		fetch := limit * 4
		if fetch < 40 {
			fetch = 40
		}
		if fetch > 200 {
			fetch = 200
		}
		search := storage.KnowledgeSearchQuery{BankID: bankID, Query: query, Limit: fetch}
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
			if filter.active() && !filter.matches(pageTags(hit.Node, models)) {
				continue
			}
			model := models[hit.Node.MentalModelID]
			out = append(out, KnowledgeSearchResult{
				ID:            hit.Node.ID,
				Name:          hit.Node.Title,
				MentalModelID: hit.Node.MentalModelID,
				SourceQuery:   model.SourceQuery,
				Snippet:       knowledgeSnippet(hit.Node.Text),
				Score:         hit.Score,
				UpdatedAt:     formatTime(model.LastRefreshedAt),
			})
			if len(out) == limit {
				break
			}
		}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgePageSearchResponse{
		Results: out, Total: len(out),
	})
}

// knowledgeSearchQuery reads the required `q` parameter. A missing or empty
// value is the reference's 422, never an empty query.
func knowledgeSearchQuery(w http.ResponseWriter, r *http.Request) (string, bool) {
	values, present := httpx.QueryPresent(r, "q")
	if !present || len(values) == 0 {
		writeKnowledgeItem(w, r, httpx.Missing([]string{"query", "q"}, map[string]any{}))
		return "", false
	}
	if values[0] == "" {
		writeKnowledgeItem(w, r, httpx.StringTooShort([]string{"query", "q"}, "", 1))
		return "", false
	}
	return values[0], true
}

// knowledgeSearchLimit reads the `limit` parameter: default 10, range 1..50.
func knowledgeSearchLimit(w http.ResponseWriter, r *http.Request) (int, bool) {
	raw := strings.TrimSpace(httpx.Query(r, "limit"))
	if raw == "" {
		return 10, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		writeKnowledgeItem(w, r, httpx.TypeMismatch("int_parsing", []string{"query", "limit"}, raw))
		return 0, false
	}
	if value < 1 {
		writeKnowledgeItem(w, r, httpx.GreaterThanEqual([]string{"query", "limit"}, value, 1))
		return 0, false
	}
	if value > 50 {
		writeKnowledgeItem(w, r, httpx.LessThanEqual([]string{"query", "limit"}, value, 50))
		return 0, false
	}
	return value, true
}
