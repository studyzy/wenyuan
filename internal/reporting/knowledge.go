package reporting

import (
	"context"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/monitoring"
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

// bankConfigEnableTextSearch is the bank configuration key that switches the
// keyword arm off for a bank, resolved rather than read off the process config.
const bankConfigEnableTextSearch = "enable_text_search"

// KnowledgeTree returns the bank's knowledge tree, nested by parent.
func (h *Handlers) KnowledgeTree(w http.ResponseWriter, r *http.Request) {
	// The query parameters are validated before the bank is looked up, because
	// in the reference `tag_filter` is a `Depends` (api/http.py:7314): the
	// framework resolves the dependency — and reports a bad `tags_match` or
	// `tag_groups` as a 422 — before the handler body runs, so a malformed
	// filter on an unknown bank is the 422, not the 404.
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
	// The caller's resolved read scope is applied on its own rather than folded
	// into the filter: a filter drops the folders it leaves empty, but a scoped
	// caller must still see an empty folder so it can put pages in it.
	projected = pruneKnowledgeNodesToScope(projected, memoryops.ScopeFromRequest(r))
	// The tree is the one surface that reports staleness: it answers the same
	// scoped question a scheduled refresh asks before spending a model call, so
	// a flagged page is one a refresh would actually rewrite. A folder has no
	// backing model, so it keeps the null.
	for i := range projected {
		if projected[i].Kind != string(knowledgeKindPage) || projected[i].MentalModelID == nil {
			continue
		}
		model, ok := models[*projected[i].MentalModelID]
		if !ok {
			continue
		}
		// The tree reports the reference's batch staleness, which deliberately
		// skips the retraction check the single-model read applies.
		stale := h.Store.IsStaleBatch(r.Context(), bankID, model)
		projected[i].IsStale = &stale
	}
	httpx.WriteJSON(w, http.StatusOK, KnowledgeTreeResponse{
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
	var target any
	var body CreateKnowledgeNodeRequest
	var folder CreateKnowledgeFolderRequest
	if kind == knowledgeKindFolder {
		target = &folder
	} else {
		target = &body
	}
	if failure := decodeKnowledgeCreateBody(r, target, kind); failure != nil {
		httpx.WriteErrorFor(w, r, failure)
		return
	}
	var name *string
	parentRaw := folder.ParentID
	if kind == knowledgeKindFolder {
		name = folder.Name
	} else {
		name = body.Name
		parentRaw = body.ParentID
	}
	parentID, failure := knowledgeParentFromBody(parentRaw)
	if failure != nil {
		httpx.WriteErrorFor(w, r, failure)
		return
	}
	// A parent must be an existing folder in this bank, so the tree cannot be
	// given a dangling reference or a page with children.
	if parentID != nil {
		if httpErr := h.assertKnowledgeFolderParent(r.Context(), bankID, *parentID); httpErr != nil {
			httpx.WriteErrorFor(w, r, httpErr)
			return
		}
	}
	if kind == knowledgeKindPage {
		// A page name is unique within its folder, compared case-insensitively:
		// the reference refuses a second page of the same name rather than letting
		// two nodes shadow each other.
		if err := h.rejectDuplicatePage(r, bankID, derefString(parentID), *name); err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		h.createKnowledgePageNode(w, r, bankID, parentID, *name, body)
		return
	}
	node, err := h.Store.PutKnowledgeNode(r.Context(), storage.KnowledgeNode{
		ID: knowledgeFolderID(), BankID: bankID, ParentID: derefString(parentID), Kind: kind, Title: *name,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, projectKnowledgeNode(node, nil))
}

// createKnowledgePageNode creates a page and the mental model that backs it, so
// the page has a body to render and an options record to update later. The page
// is then refreshed asynchronously, so the create returns the queued operation
// rather than waiting on a model call.
//
// The page starts with an empty body: the reference's create body carries no
// content member, so the document is written by the refresh the create queues.
func (h *Handlers) createKnowledgePageNode(w http.ResponseWriter, r *http.Request, bankID string, parentID *string, name string, body CreateKnowledgeNodeRequest) {
	ctx := r.Context()
	trigger, failure := h.knowledgePageTrigger(ctx, r, bankID, body.Trigger)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	sourceQuery := derefString(body.SourceQuery)
	tags := derefStrings(body.Tags)
	maxTokens := 0
	if body.MaxTokens != nil {
		maxTokens = *body.MaxTokens
	}
	modelID := knowledgeMentalModelID()
	model, err := h.Store.PutMentalModel(ctx, storage.MentalModel{
		ID: modelID, BankID: bankID, Subtype: storage.SubtypePinned,
		Name: name, SourceQuery: sourceQuery, Content: "",
		Tags: tags, MaxTokens: knowledgePageMaxTokens(maxTokens), Trigger: trigger,
		Embedding: h.pageEmbedding(ctx, name+" "),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	node, err := h.Store.PutKnowledgeNode(ctx, storage.KnowledgeNode{
		ID: knowledgePageID(), BankID: bankID, ParentID: derefString(parentID), Kind: knowledgeKindPage,
		Title: name, MentalModelID: model.ID, Tags: tags,
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
	httpx.WriteJSON(w, http.StatusCreated, CreateKnowledgePageResponse{
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
// the built-in page default, overridden by the process, tenant and bank layers
// of `knowledge_page_default_trigger`, overridden by whatever the request named.
func (h *Handlers) knowledgePageTrigger(ctx context.Context, r *http.Request, bankID string, supplied map[string]any) (map[string]any, *httpx.ValidationFailure) {
	if failure := mentalmodels.ValidateTriggerObject(supplied); failure != nil {
		return nil, failure
	}
	// The layers, deepest first: the engine's built-in page default, the process
	// default, the caller's tenant override, the bank's own override, and
	// finally what the request named — the reference's own order.
	base := mentalmodels.KnowledgePageDefaultTrigger
	base = overlayTrigger(base, h.ProcessDefaults[bankConfigKnowledgePageTrigger])
	base = overlayTrigger(base, httpx.TenantOverridesFrom(r)[bankConfigKnowledgePageTrigger])
	if config, err := h.Store.BankConfig(ctx, bankID); err == nil {
		base = overlayTrigger(base, config[bankConfigKnowledgePageTrigger])
	}
	return mentalmodels.MergeTrigger(supplied, base), nil
}

// overlayTrigger layers one `knowledge_page_default_trigger` value over a base.
// A layer that names nothing, or that is not an object, leaves the base alone:
// the value is validated on write, so a stored shape this layer cannot read is
// the bank's business rather than a reason to fail the create.
func overlayTrigger(base map[string]any, raw any) map[string]any {
	trigger, ok := raw.(map[string]any)
	if !ok || trigger == nil {
		return base
	}
	return mentalmodels.MergeTrigger(trigger, base)
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
// already sits in the target folder. The reference's uniqueness index compares
// lower-cased names, so `Deploy` and `deploy` cannot coexist in one folder.
func (h *Handlers) rejectDuplicatePage(r *http.Request, bankID, parentID, name string) error {
	nodes, err := h.Store.ListKnowledgeNodes(r.Context(), bankID)
	if err != nil {
		return err
	}
	for _, node := range nodes {
		if node.Kind == knowledgeKindPage && node.ParentID == parentID && strings.EqualFold(node.Title, name) {
			return httpx.NewHTTPError(httpx.KindConflict,
				"A page named '"+name+"' already exists in this folder")
		}
	}
	return nil
}

// KnowledgePage returns one page as a markdown document. The bank is not
// pre-checked: a page in an unknown bank answers the page's own not-found.
func (h *Handlers) KnowledgePage(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	pageID := strings.TrimSpace(r.PathValue("page_id"))
	node, err := h.Store.GetKnowledgeNode(r.Context(), bankID, pageID)
	if err != nil {
		httpx.WriteErrorFor(w, r, knowledgeNotFound(err, knowledgePageNotFound(pageID)))
		return
	}
	// A folder is not a page: the reference's page read joins on kind='page' and
	// a folder reads as a missing page rather than as an empty document.
	if node.Kind != knowledgeKindPage {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, knowledgePageNotFound(pageID)))
		return
	}
	model, hasModel := h.knowledgeModel(r.Context(), bankID, node)
	pageType := knowledgePageType(node.Tags)
	var description *string
	timestamp := httpx.FormatTimestampPtr(&node.CreatedAt)
	if hasModel {
		description = &model.SourceQuery
		if !model.LastRefreshedAt.IsZero() {
			timestamp = httpx.FormatTimestampPtr(&model.LastRefreshedAt)
		}
	}
	body := node.Text
	writeKnowledgePage(w, r, node, pageType, description, timestamp, &body)
}

// UpdateKnowledgeNode applies a partial update to one node. The bank is not
// pre-checked: an unknown bank answers the node's own not-found.
func (h *Handlers) UpdateKnowledgeNode(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	nodeID := strings.TrimSpace(r.PathValue("node_id"))
	var body UpdateKnowledgeNodeRequest
	if _, failure := decodeKnowledgeBody(r, &body); failure != nil {
		httpx.WriteErrorFor(w, r, failure)
		return
	}
	// parent_id is applied only when the key is present, so an explicit null
	// moves the node to the root and an absent key leaves the parent alone.
	moving := len(body.ParentID) > 0
	newParent, failure := knowledgeParentFromBody(body.ParentID)
	if failure != nil {
		httpx.WriteErrorFor(w, r, failure)
		return
	}
	trigger, triggerSupplied, failure := knowledgeUpdateTrigger(body.Trigger)
	if failure != nil {
		httpx.WriteErrorFor(w, r, failure)
		return
	}
	// Page options live on the backing mental model and take the opposite
	// convention: an absent member is "not changing this", while an empty value
	// is a real change (tags=[] clears them). A body that is null all the way
	// down changes nothing at all, and is refused before anything is read.
	pageOptions := body.SourceQuery != nil || body.Tags != nil || body.MaxTokens != nil || triggerSupplied
	if body.Name == nil && !moving && !pageOptions {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"Provide name, parent_id, source_query, tags, max_tokens, and/or trigger to update"))
		return
	}
	if moving && newParent != nil && *newParent == nodeID {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "A node cannot be its own parent"))
		return
	}
	node, err := h.Store.GetKnowledgeNode(r.Context(), bankID, nodeID)
	if err != nil {
		httpx.WriteErrorFor(w, r, knowledgeNotFound(err, knowledgeNodeNotFound(nodeID)))
		return
	}
	// Page options only exist on a page's backing model; asking for them on a
	// folder is the reference's "not found", not a silent no-op.
	if pageOptions && (node.Kind != knowledgeKindPage || node.MentalModelID == "") {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, knowledgeNodeNotFound(nodeID)))
		return
	}
	if moving && newParent != nil {
		if httpErr := h.assertKnowledgeFolderParent(r.Context(), bankID, *newParent); httpErr != nil {
			httpx.WriteErrorFor(w, r, httpErr)
			return
		}
		if httpErr := h.assertKnowledgeMove(r.Context(), bankID, nodeID, *newParent); httpErr != nil {
			httpx.WriteErrorFor(w, r, httpErr)
			return
		}
	}
	patch := storage.KnowledgeNodePatch{Title: body.Name, Tags: body.Tags}
	if moving {
		parent := derefString(newParent)
		patch.ParentID = &parent
	}
	// The node row and the model backing it commit as one change: the reference
	// runs the whole patch in one transaction, so a rename cannot survive the
	// option update that failed after it, and no reader sees the pair disagree.
	//
	// The model is read here only to merge a stated trigger over the page's
	// current one; the storage write re-reads it inside the transaction.
	current, pageHasModel := h.knowledgeModel(r.Context(), bankID, node)
	var modelPatch *storage.MentalModelPatch
	if pageHasModel && pageOptions {
		// Tags live on the backing model too, so a page's tags are one value
		// across the tree, search and read surfaces rather than two that drift.
		pending := storage.MentalModelPatch{
			SourceQuery: body.SourceQuery, Tags: body.Tags, MaxTokens: body.MaxTokens,
		}
		if triggerSupplied {
			// The trigger patches: the members the client stated are updated and
			// the rest keep the page's current values, so moving a page onto a
			// schedule does not reset how it refreshes. The merge is the same one
			// a mental-model patch uses, so the reference's directional
			// exclusivity holds here too: a stated cron clears a stale
			// refresh_after_consolidation, while enabling consolidation leaves a
			// configured cron in place.
			merged := mentalmodels.MergeTrigger(trigger, current.Trigger)
			pending.Trigger = &merged
		}
		modelPatch = &pending
	}
	node, err = h.Store.UpdateKnowledgeNodeWithModel(r.Context(), bankID, nodeID, patch, modelPatch)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// A rename carries the name onto the backing model, so the model is read
	// after the patch rather than rendered from the pre-patch copy.
	model, hasModel := h.knowledgeModel(r.Context(), bankID, node)
	// A new source query means the content is stale, so the page is rebuilt —
	// scheduled only once the patch has committed, so a refresh is never queued
	// for a change that rolled back.
	if body.SourceQuery != nil && hasModel {
		if _, err := h.enqueuePageRefresh(r.Context(), bankID, model.ID); err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, projectKnowledgeNode(node, modelPtr(model, hasModel)))
}

// DeleteKnowledgeNode removes one node and its descendants. The bank is not
// pre-checked: an unknown bank answers the node's own not-found.
func (h *Handlers) DeleteKnowledgeNode(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	nodeID := strings.TrimSpace(r.PathValue("node_id"))
	if _, err := h.Store.GetKnowledgeNode(r.Context(), bankID, nodeID); err != nil {
		httpx.WriteErrorFor(w, r, knowledgeNotFound(err, knowledgeNodeNotFound(nodeID)))
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
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"status": "deleted"})
}

// Version reports the API version under the versioned namespace. It is the
// same payload as the root version endpoint, so it renders through the same
// body: the feature map's members are emitted in the reference's declaration
// order rather than in Go's map order.
func (h *Handlers) Version(w http.ResponseWriter, r *http.Request) {
	features := h.Features
	if features == nil {
		features = map[string]bool{}
	}
	httpx.WriteJSON(w, http.StatusOK, monitoring.VersionBody{
		APIVersion: h.APIVersion,
		Features:   features,
	})
}

// The reference's node identifiers are its mental-model convention with a kind
// prefix: `kf-` and `kp-` followed by a bare 32-character uuid4 hex
// (engine/memory_engine.py:19303,19361,19375). newID renders them the same way,
// so an identifier minted here is the length and shape a client parsing the
// reference's ids expects.
func knowledgeFolderID() string { return "kf-" + newID() }

// knowledgePageID is the page node's identifier; see knowledgeFolderID.
func knowledgePageID() string { return "kp-" + newID() }

// knowledgeMentalModelID is the identifier of the mental model backing a page.
func knowledgeMentalModelID() string { return "mm-" + newID() }
