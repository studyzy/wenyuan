package reporting

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// KnowledgeNode is one node of the bank's knowledge tree. A folder groups
// pages; a page carries text.
type KnowledgeNode struct {
	ID       string          `json:"id"`
	BankID   string          `json:"bank_id"`
	ParentID string          `json:"parent_id,omitempty"`
	Kind     string          `json:"kind"`
	Title    string          `json:"title"`
	Text     string          `json:"text,omitempty"`
	Tags     []string        `json:"tags"`
	Children []KnowledgeNode `json:"children,omitempty"`
	Created  string          `json:"created_at"`
	Updated  string          `json:"updated_at"`
}

// KnowledgeTreeResponse is the knowledge tree envelope.
type KnowledgeTreeResponse struct {
	BankID string          `json:"bank_id"`
	Nodes  []KnowledgeNode `json:"nodes"`
}

// CreateKnowledgeNodeRequest is the create body for a folder or a page.
type CreateKnowledgeNodeRequest struct {
	ParentID string   `json:"parent_id"`
	Title    string   `json:"title"`
	Text     string   `json:"text"`
	Tags     []string `json:"tags"`
}

// CreateKnowledgePageResponse is the page-create result.
type CreateKnowledgePageResponse struct {
	Page KnowledgeNode `json:"page"`
}

// KnowledgePageResponse is one page.
type KnowledgePageResponse struct {
	Page KnowledgeNode `json:"page"`
}

// KnowledgePageBundleResponse is an exported bundle of pages.
type KnowledgePageBundleResponse struct {
	BankID string          `json:"bank_id"`
	Pages  []KnowledgeNode `json:"pages"`
	Total  int             `json:"total"`
}

// KnowledgeSearchHit is one search result.
type KnowledgeSearchHit struct {
	Page  KnowledgeNode `json:"page"`
	Score float64       `json:"score"`
}

// KnowledgePageSearchResponse is the search envelope.
type KnowledgePageSearchResponse struct {
	BankID string               `json:"bank_id"`
	Query  string               `json:"query"`
	Hits   []KnowledgeSearchHit `json:"hits"`
	Total  int                  `json:"total"`
}

// UpdateKnowledgeNodeRequest is the partial-update body.
type UpdateKnowledgeNodeRequest struct {
	Title    *string   `json:"title"`
	Text     *string   `json:"text"`
	ParentID *string   `json:"parent_id"`
	Tags     *[]string `json:"tags"`
}

// KnowledgeTree returns the bank's knowledge tree, nested by parent.
func (h *Handlers) KnowledgeTree(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	nodes, err := h.Store.ListKnowledgeNodes(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgeTreeResponse{
		BankID: bankID, Nodes: nestKnowledge(nodes),
	})
}

// CreateKnowledgeFolder adds a folder.
func (h *Handlers) CreateKnowledgeFolder(w http.ResponseWriter, r *http.Request) {
	h.createKnowledgeNode(w, r, storage.KnowledgeKindFolder)
}

// CreateKnowledgePage adds a page.
func (h *Handlers) CreateKnowledgePage(w http.ResponseWriter, r *http.Request) {
	h.createKnowledgeNode(w, r, storage.KnowledgeKindPage)
}

// createKnowledgeNode implements both create forms.
func (h *Handlers) createKnowledgeNode(w http.ResponseWriter, r *http.Request, kind storage.KnowledgeKind) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var body CreateKnowledgeNodeRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if strings.TrimSpace(body.Title) == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "title is required"))
		return
	}
	// A parent must exist in the same bank, so the tree cannot be given a
	// dangling reference.
	if parent := strings.TrimSpace(body.ParentID); parent != "" {
		if _, err := h.Store.GetKnowledgeNode(r.Context(), bankID, parent); err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
	}
	node, err := h.Store.PutKnowledgeNode(r.Context(), storage.KnowledgeNode{
		ID: newID(), BankID: bankID, ParentID: strings.TrimSpace(body.ParentID),
		Kind: kind, Title: body.Title, Text: body.Text, Tags: body.Tags,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusCreated, CreateKnowledgePageResponse{Page: toKnowledgeNode(node)})
}

// KnowledgeExport bundles every page of the bank.
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
	pages := make([]KnowledgeNode, 0, len(nodes))
	for _, node := range nodes {
		if node.Kind == storage.KnowledgeKindPage {
			pages = append(pages, toKnowledgeNode(node))
		}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgePageBundleResponse{
		BankID: bankID, Pages: pages, Total: len(pages),
	})
}

// KnowledgeSearch searches the bank's pages.
func (h *Handlers) KnowledgeSearch(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	hits, err := h.Store.SearchKnowledgeNodes(r.Context(), bankID, query, pageLimit(r))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]KnowledgeSearchHit, 0, len(hits))
	for _, hit := range hits {
		out = append(out, KnowledgeSearchHit{Page: toKnowledgeNode(hit.Node), Score: hit.Score})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgePageSearchResponse{
		BankID: bankID, Query: query, Hits: out, Total: len(out),
	})
}

// KnowledgePage returns one page.
func (h *Handlers) KnowledgePage(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	pageID := strings.TrimSpace(r.PathValue("page_id"))
	node, err := h.Store.GetKnowledgeNode(r.Context(), bankID, pageID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgePageResponse{Page: toKnowledgeNode(node)})
}

// UpdateKnowledgeNode applies a partial update to one node.
func (h *Handlers) UpdateKnowledgeNode(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	nodeID := strings.TrimSpace(r.PathValue("node_id"))
	var body UpdateKnowledgeNodeRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if body.ParentID != nil {
		if parent := strings.TrimSpace(*body.ParentID); parent != "" {
			if _, err := h.Store.GetKnowledgeNode(r.Context(), bankID, parent); err != nil {
				httpx.WriteErrorFor(w, r, asHTTPError(err))
				return
			}
		}
	}
	node, err := h.Store.UpdateKnowledgeNode(r.Context(), bankID, nodeID, storage.KnowledgeNodePatch{
		Title: body.Title, Text: body.Text, ParentID: body.ParentID, Tags: body.Tags,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toKnowledgeNode(node))
}

// DeleteKnowledgeNode removes one node and its descendants.
func (h *Handlers) DeleteKnowledgeNode(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	nodeID := strings.TrimSpace(r.PathValue("node_id"))
	if _, err := h.Store.GetKnowledgeNode(r.Context(), bankID, nodeID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	if err := h.Store.DeleteKnowledgeNode(r.Context(), bankID, nodeID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// nestKnowledge arranges a flat node list into a tree, so a client receives the
// hierarchy rather than having to rebuild it.
func nestKnowledge(nodes []storage.KnowledgeNode) []KnowledgeNode {
	byParent := map[string][]storage.KnowledgeNode{}
	for _, node := range nodes {
		byParent[node.ParentID] = append(byParent[node.ParentID], node)
	}
	var build func(parentID string) []KnowledgeNode
	build = func(parentID string) []KnowledgeNode {
		children := byParent[parentID]
		out := make([]KnowledgeNode, 0, len(children))
		for _, child := range children {
			rendered := toKnowledgeNode(child)
			rendered.Children = build(child.ID)
			out = append(out, rendered)
		}
		return out
	}
	return build("")
}

// toKnowledgeNode renders a stored node for the wire.
func toKnowledgeNode(node storage.KnowledgeNode) KnowledgeNode {
	tags := node.Tags
	if tags == nil {
		tags = []string{}
	}
	return KnowledgeNode{
		ID: node.ID, BankID: node.BankID, ParentID: node.ParentID,
		Kind: string(node.Kind), Title: node.Title, Text: node.Text, Tags: tags,
		Created: formatTime(node.CreatedAt), Updated: formatTime(node.UpdatedAt),
	}
}

// BankTemplateSchema reports the shape a bank template must have.
func (h *Handlers) BankTemplateSchema(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSONFor(w, r, http.StatusOK, map[string]any{
		"schema_version": 1,
		"fields": map[string]any{
			"name":               map[string]any{"type": "string", "required": true},
			"disposition_traits": map[string]any{"type": "object"},
			"reflect_mission":    map[string]any{"type": "string"},
			"mental_models":      map[string]any{"type": "array"},
			"directives":         map[string]any{"type": "array"},
		},
	})
}

// Version reports the API version under the versioned namespace.
func (h *Handlers) Version(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSONFor(w, r, http.StatusOK, map[string]any{"version": httpx.APIVersion})
}

// ensure the identifier helper is reachable from this file.
var _ = hex.EncodeToString
var _ = json.Marshal
var _ = fmt.Sprintf
var _ = time.Now
var _ = rand.Read
