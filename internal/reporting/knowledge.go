package reporting

import (
	"context"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Knowledge-node kinds, named so the projection and index renderer agree with
// the store instead of restating its string values.
const (
	knowledgeKindFolder = storage.KnowledgeKindFolder
	knowledgeKindPage   = storage.KnowledgeKindPage
)

// bankConfigKnowledgePageTrigger is the bank configuration key whose value is
// merged over the built-in page default when a page is created.
const bankConfigKnowledgePageTrigger = "knowledge_page_default_trigger"

// KnowledgeNode is one node of the bank's knowledge tree: a folder grouping
// pages, or a page carrying text. The field set mirrors the reference's node
// model — a page's description/tags/trigger come from its backing mental model.
type KnowledgeNode struct {
	ID                  string          `json:"id"`
	Kind                string          `json:"kind"`
	Name                string          `json:"name"`
	ParentID            string          `json:"parent_id,omitempty"`
	MentalModelID       string          `json:"mental_model_id,omitempty"`
	Managed             bool            `json:"managed"`
	Description         string          `json:"description,omitempty"`
	Tags                []string        `json:"tags"`
	Timestamp           string          `json:"timestamp,omitempty"`
	IsStale             *bool           `json:"is_stale,omitempty"`
	LastRefreshFailedAt string          `json:"last_refresh_failed_at,omitempty"`
	Trigger             map[string]any  `json:"trigger,omitempty"`
	Children            []KnowledgeNode `json:"children"`
}

// KnowledgeTreeResponse is the knowledge tree envelope.
type KnowledgeTreeResponse struct {
	Roots []KnowledgeNode `json:"roots"`
}

// CreateKnowledgeNodeRequest is the create body for a folder or a page. The
// reference names the node `name`; `title` is retained as a compatibility
// alias, as are `text`/`content` for a page's initial body.
type CreateKnowledgeNodeRequest struct {
	ParentID    string         `json:"parent_id"`
	Name        string         `json:"name"`
	Title       string         `json:"title"`
	SourceQuery string         `json:"source_query"`
	Text        string         `json:"text"`
	Content     string         `json:"content"`
	Tags        []string       `json:"tags"`
	MaxTokens   int            `json:"max_tokens"`
	Trigger     map[string]any `json:"trigger"`
}

// nodeName returns the requested node name, accepting either spelling.
func (b CreateKnowledgeNodeRequest) nodeName() string {
	if strings.TrimSpace(b.Name) != "" {
		return b.Name
	}
	return b.Title
}

// pageText returns the requested initial body, accepting either spelling.
func (b CreateKnowledgeNodeRequest) pageText() string {
	if b.Text != "" {
		return b.Text
	}
	return b.Content
}

// CreateKnowledgePageResponse is the page-create result: the page is generated
// in the background, so the caller polls the operation. The backing mental
// model id is always present, because every page has one.
type CreateKnowledgePageResponse struct {
	PageID        string `json:"page_id"`
	MentalModelID string `json:"mental_model_id"`
	OperationID   string `json:"operation_id,omitempty"`
}

// KnowledgePageResponse is one page rendered as a markdown document.
type KnowledgePageResponse struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Type        string   `json:"type"`
	Description string   `json:"description,omitempty"`
	Tags        []string `json:"tags"`
	Timestamp   string   `json:"timestamp,omitempty"`
	// Body is null until a refresh writes content; the stored body stays empty
	// where `markdown` says so in words. It is dropped when null.
	Body     *string `json:"body,omitempty"`
	Markdown string  `json:"markdown"`
}

// KnowledgeFile is one file of an exported knowledge bundle.
type KnowledgeFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// KnowledgePageBundleResponse is an exported bundle of pages.
type KnowledgePageBundleResponse struct {
	Files []KnowledgeFile `json:"files"`
}

// KnowledgeSearchResult is one search result.
type KnowledgeSearchResult struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	MentalModelID string  `json:"mental_model_id,omitempty"`
	SourceQuery   string  `json:"source_query,omitempty"`
	Snippet       string  `json:"snippet"`
	Score         float64 `json:"score"`
	UpdatedAt     string  `json:"updated_at,omitempty"`
}

// KnowledgePageSearchResponse is the search envelope.
type KnowledgePageSearchResponse struct {
	Results []KnowledgeSearchResult `json:"results"`
	Total   int                     `json:"total"`
}

// UpdateKnowledgeNodeRequest is the partial-update body. `name`/`parent_id`
// rename and move the node; `source_query`, `tags`, `max_tokens` and `trigger`
// update a page's options on its backing mental model. `title`/`text`/`content`
// are retained as compatibility aliases.
type UpdateKnowledgeNodeRequest struct {
	Name        *string        `json:"name"`
	Title       *string        `json:"title"`
	ParentID    *string        `json:"parent_id"`
	SourceQuery *string        `json:"source_query"`
	Tags        *[]string      `json:"tags"`
	MaxTokens   *int           `json:"max_tokens"`
	Trigger     map[string]any `json:"trigger"`
	Text        *string        `json:"text"`
	Content     *string        `json:"content"`
}

// KnowledgeTree returns the bank's knowledge tree, nested by parent.
func (h *Handlers) KnowledgeTree(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
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
	projected := pruneKnowledgeNodes(projectKnowledgeNodes(nodes, models), filter)
	httpx.WriteJSONFor(w, r, http.StatusOK, KnowledgeTreeResponse{
		Roots: nestKnowledge(projected),
	})
}

// CreateKnowledgeFolder adds a folder.
func (h *Handlers) CreateKnowledgeFolder(w http.ResponseWriter, r *http.Request) {
	h.createKnowledgeNode(w, r, knowledgeKindFolder)
}

// CreateKnowledgePage adds a page.
func (h *Handlers) CreateKnowledgePage(w http.ResponseWriter, r *http.Request) {
	h.createKnowledgeNode(w, r, knowledgeKindPage)
}

// createKnowledgeNode implements both create forms.
func (h *Handlers) createKnowledgeNode(w http.ResponseWriter, r *http.Request, kind storage.KnowledgeKind) {
	bankID, ok := h.requireOrCreateBank(w, r)
	if !ok {
		return
	}
	var body CreateKnowledgeNodeRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	name := body.nodeName()
	if strings.TrimSpace(name) == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "name is required"))
		return
	}
	parentID := strings.TrimSpace(body.ParentID)
	// A parent must exist in the same bank, so the tree cannot be given a
	// dangling reference.
	if parentID != "" {
		if _, err := h.Store.GetKnowledgeNode(r.Context(), bankID, parentID); err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
	}
	if kind == knowledgeKindPage {
		// A page name is unique within its folder: the reference refuses a second
		// page of the same name rather than letting two nodes shadow each other.
		if err := h.rejectDuplicatePage(r, bankID, parentID, name); err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		h.createKnowledgePageNode(w, r, bankID, parentID, name, body)
		return
	}
	node, err := h.Store.PutKnowledgeNode(r.Context(), storage.KnowledgeNode{
		ID: "kf-" + newID(), BankID: bankID, ParentID: parentID, Kind: kind, Title: name,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusCreated, projectKnowledgeNode(node, nil))
}

// createKnowledgePageNode creates a page and the mental model that backs it, so
// the page has a body to render and an options record to update later. The page
// is then refreshed asynchronously, so the create returns the queued operation
// rather than waiting on a model call.
func (h *Handlers) createKnowledgePageNode(w http.ResponseWriter, r *http.Request, bankID, parentID, name string, body CreateKnowledgeNodeRequest) {
	ctx := r.Context()
	text := body.pageText()
	trigger, failure := h.knowledgePageTrigger(ctx, bankID, body.Trigger)
	if failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	modelID := "mm-" + newID()
	model, err := h.Store.PutMentalModel(ctx, storage.MentalModel{
		ID: modelID, BankID: bankID, Subtype: storage.SubtypePinned,
		Name: name, SourceQuery: body.SourceQuery, Content: text,
		Tags: body.Tags, MaxTokens: knowledgePageMaxTokens(body.MaxTokens), Trigger: trigger,
		Embedding: h.pageEmbedding(ctx, name+" "+text),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	node, err := h.Store.PutKnowledgeNode(ctx, storage.KnowledgeNode{
		ID: "kp-" + newID(), BankID: bankID, ParentID: parentID, Kind: knowledgeKindPage,
		Title: name, MentalModelID: model.ID, Text: text, Tags: body.Tags, Managed: true,
	})
	if err != nil {
		_ = h.Store.DeleteMentalModel(ctx, bankID, model.ID)
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	operationID, err := h.enqueuePageRefresh(ctx, bankID, model.ID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusCreated, CreateKnowledgePageResponse{
		PageID: node.ID, MentalModelID: model.ID, OperationID: operationID,
	})
}

// knowledgePageMaxTokens falls back to the reference's page default when the
// create names none.
func knowledgePageMaxTokens(requested int) int {
	if requested > 0 {
		return requested
	}
	return knowledgePageDefaultMaxTokens
}

// knowledgePageTrigger resolves the refresh policy a new page is created with:
// the built-in page default, overridden by the bank's
// `knowledge_page_default_trigger`, overridden by whatever the request named.
func (h *Handlers) knowledgePageTrigger(ctx context.Context, bankID string, supplied map[string]any) (map[string]any, *httpx.ValidationFailure) {
	if failure := mentalmodels.ValidateTriggerObject(supplied); failure != nil {
		return nil, failure
	}
	base := mentalmodels.KnowledgePageDefaultTrigger
	if config, err := h.Store.BankConfig(ctx, bankID); err == nil {
		if raw, ok := config[bankConfigKnowledgePageTrigger].(map[string]any); ok && raw != nil {
			base = mentalmodels.MergeTrigger(raw, mentalmodels.KnowledgePageDefaultTrigger)
		}
	}
	return mentalmodels.MergeTrigger(supplied, base), nil
}

// pageEmbedding embeds a page's searchable text — its name and content, the
// same text its BM25 index covers — so a knowledge search can fuse a vector arm
// with the text arm. A deployment with no embedding model stores no vector, and
// the search falls back to the text arm.
func (h *Handlers) pageEmbedding(ctx context.Context, text string) []float32 {
	if h.Embeddings == nil {
		return nil
	}
	vectors, err := h.Embeddings.EmbedPassages(ctx, []string{text})
	if err != nil || len(vectors) == 0 {
		return nil
	}
	return vectors[0]
}

// enqueuePageRefresh queues a page's first refresh and returns the operation
// identifier.
func (h *Handlers) enqueuePageRefresh(ctx context.Context, bankID, modelID string) (string, error) {
	result, err := h.Store.EnqueueOperation(ctx, storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           bankID,
			OperationType:    storage.OperationTypeRefreshMentalModel,
			Payload:          map[string]any{storage.MentalModelIDPayloadKey: modelID},
			SerializationKey: storage.MentalModelSerializationKey(modelID),
		},
		DedupeByPayloadKey: storage.MentalModelIDPayloadKey,
	})
	if err != nil {
		return "", err
	}
	return result.Operation.OperationID, nil
}

// rejectDuplicatePage returns a conflict error when a page of the same name
// already sits in the target folder.
func (h *Handlers) rejectDuplicatePage(r *http.Request, bankID, parentID, name string) error {
	nodes, err := h.Store.ListKnowledgeNodes(r.Context(), bankID)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if node.Kind == knowledgeKindPage && node.ParentID == parentID && node.Title == name {
			return httpx.NewHTTPError(httpx.KindConflict,
				"A page named '"+name+"' already exists in this folder")
		}
	}
	return nil
}

// KnowledgePage returns one page as a markdown document.
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
	// A folder is not a page: upstream's page read joins on kind='page' and a
	// folder reads as missing rather than as an empty document.
	if node.Kind != knowledgeKindPage {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "knowledge node not found"))
		return
	}
	model, hasModel := h.knowledgeModel(r.Context(), bankID, node)
	pageType := knowledgePageType(node.Tags)
	description := ""
	timestamp := formatTime(node.CreatedAt)
	if hasModel {
		description = model.SourceQuery
		if !model.LastRefreshedAt.IsZero() {
			timestamp = formatTime(model.LastRefreshedAt)
		}
	}
	body := node.Text
	writeKnowledgePage(w, r, node, pageType, description, timestamp, &body)
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
		Title: firstStringPtr(body.Name, body.Title), Text: firstStringPtr(body.Text, body.Content),
		ParentID: body.ParentID, Tags: body.Tags,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	model, hasModel := h.knowledgeModel(r.Context(), bankID, node)
	if hasModel && (body.SourceQuery != nil || body.Tags != nil || body.MaxTokens != nil || body.Trigger != nil) {
		// Tags live on the backing model too, so a page's tags are one value
		// across the tree, search and read surfaces rather than two that drift.
		patch := storage.MentalModelPatch{SourceQuery: body.SourceQuery, Tags: body.Tags, MaxTokens: body.MaxTokens}
		if body.Trigger != nil {
			patch.Trigger = &body.Trigger
		}
		updated, err := h.Store.UpdateMentalModel(r.Context(), bankID, model.ID, patch)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		model = updated
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, projectKnowledgeNode(node, modelPtr(model, hasModel)))
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
	// The pages in the doomed subtree are removed with it, so their backing
	// mental models would be orphaned if they were not deleted too.
	models := h.subtreeKnowledgeModels(r.Context(), bankID, nodeID)
	if err := h.Store.DeleteKnowledgeNode(r.Context(), bankID, nodeID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	for _, modelID := range models {
		_ = h.Store.DeleteMentalModel(r.Context(), bankID, modelID)
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, map[string]any{"status": "deleted"})
}

// Version reports the API version under the versioned namespace. It is the
// same payload as the root version endpoint.
func (h *Handlers) Version(w http.ResponseWriter, r *http.Request) {
	features := h.Features
	if features == nil {
		features = map[string]bool{}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, map[string]any{
		"api_version": h.APIVersion,
		"features":    features,
	})
}
