package reporting

// The knowledge page's create and patch cores.
//
// A page is a tree node plus the mental model that answers it, and both the HTTP
// routes and the MCP tools must build that pair the same way. Each core here is
// the one implementation of its operation; the route and the tool are thin
// callers, so the resolved trigger, the budget default, the queued refresh and
// the move checks cannot drift between the two surfaces.

import (
	"context"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mentalmodels"
	"github.com/studyzy/wenyuan/internal/storage"
)

// pageBuilder is the create core this handler set shares with the MCP tools and
// the administration plane's project seeder. See knowledge_page_builder.go.
func (h *Handlers) pageBuilder() *PageBuilder {
	return &PageBuilder{
		Store:           h.Store,
		Embeddings:      h.Embeddings,
		ProcessDefaults: h.ProcessDefaults,
	}
}

// CreateKnowledgePageNode stores a page and its backing mental model and queues
// the page's first refresh. It is the one implementation of "create a knowledge
// page": the HTTP route and the MCP tool both call it, so the model, the
// resolved trigger, the budget default and the queued refresh cannot differ
// between the two surfaces.
func (h *Handlers) CreateKnowledgePageNode(ctx context.Context, tenantOverrides map[string]any, bankID string, parentID *string, name string, params CreateKnowledgePageParams) (CreateKnowledgePageResult, *httpx.HTTPError) {
	return h.pageBuilder().BuildPage(ctx, tenantOverrides, bankID, parentID, name, params)
}

// knowledgePageTrigger resolves the refresh policy a new page is created with.
// A bank's own `knowledge_page_default_trigger` is read through the store, so
// resolving it here and in the builder cannot differ.
func (h *Handlers) knowledgePageTrigger(ctx context.Context, r *http.Request, bankID string, supplied map[string]any) (map[string]any, *httpx.ValidationFailure) {
	return h.pageBuilder().knowledgePageTrigger(ctx, bankID, httpx.TenantOverridesFrom(r), supplied)
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
	bankID := PathBankID(r)
	// The identifier is read exactly as it arrived: the reference declares the path
	// parameter as a plain string and binds it straight into the query, so a value
	// with surrounding whitespace names no page (and its not-found detail echoes
	// the whitespace back), and `%2F` stays part of the identifier rather than
	// becoming a path separator.
	pageID := pathID(r, "page_id")
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
	bankID := PathBankID(r)
	nodeID := pathID(r, "node_id")
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
	result, httpErr := h.UpdateKnowledgeNodeRecord(r.Context(), bankID, nodeID, KnowledgeNodeParams{
		Name:        body.Name,
		Move:        moving,
		NewParent:   newParent,
		SourceQuery: body.SourceQuery,
		Tags:        body.Tags,
		MaxTokens:   body.MaxTokens,
		Trigger:     trigger,
		TriggerSet:  triggerSupplied,
	})
	if httpErr != nil {
		httpx.WriteErrorFor(w, r, httpErr)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, projectKnowledgeNode(result.Node, modelPtr(result.Model, result.HasModel)))
}

// KnowledgeNodeParams are the changes one node update applies. A nil member is
// "not supplied"; Move distinguishes an absent `parent_id` (leave the parent
// alone) from a supplied null (move to the root), which NewParent then carries.
type KnowledgeNodeParams struct {
	Name        *string
	Move        bool
	NewParent   *string
	SourceQuery *string
	Tags        *[]string
	MaxTokens   *int
	// Trigger is the validated trigger patch the caller stated; TriggerSet says
	// whether it stated one at all, since an empty patch and no patch differ.
	Trigger    map[string]any
	TriggerSet bool
}

// KnowledgeNodeResult is the patched node and the model backing it, re-read
// after the write so a rename carried onto the model is the one reported.
type KnowledgeNodeResult struct {
	Node     storage.KnowledgeNode
	Model    storage.MentalModel
	HasModel bool
}

// UpdateKnowledgeNodeRecord applies one node update and returns the result. It
// is the one implementation of "patch a knowledge node": the HTTP route and the
// MCP tool both call it, so the page options, the trigger merge, the move
// checks and the refresh-on-new-source-query behave the same on both surfaces.
func (h *Handlers) UpdateKnowledgeNodeRecord(ctx context.Context, bankID, nodeID string, params KnowledgeNodeParams) (KnowledgeNodeResult, *httpx.HTTPError) {
	node, err := h.Store.GetKnowledgeNode(ctx, bankID, nodeID)
	if err != nil {
		return KnowledgeNodeResult{}, knowledgeNotFound(err, knowledgeNodeNotFound(nodeID))
	}
	// Page options live on the backing mental model and take the opposite
	// convention: an absent member is "not changing this", while an empty value
	// is a real change (tags=[] clears them). A body that names nothing at all
	// changes nothing, and the caller refuses it before this point.
	pageOptions := params.SourceQuery != nil || params.Tags != nil || params.MaxTokens != nil || params.TriggerSet
	// Page options only exist on a page's backing model; asking for them on a
	// folder is the reference's "not found", not a silent no-op.
	if pageOptions && (node.Kind != knowledgeKindPage || node.MentalModelID == "") {
		return KnowledgeNodeResult{}, httpx.NewHTTPError(httpx.KindNotFound, knowledgeNodeNotFound(nodeID))
	}
	if params.Move && params.NewParent != nil {
		if httpErr := h.assertKnowledgeFolderParent(ctx, bankID, *params.NewParent); httpErr != nil {
			return KnowledgeNodeResult{}, httpErr
		}
		if httpErr := h.assertKnowledgeMove(ctx, bankID, nodeID, *params.NewParent); httpErr != nil {
			return KnowledgeNodeResult{}, httpErr
		}
	}
	patch := storage.KnowledgeNodePatch{Title: params.Name, Tags: params.Tags}
	if params.Move {
		parent := derefString(params.NewParent)
		patch.ParentID = &parent
	}
	// The node row and the model backing it commit as one change: the reference
	// runs the whole patch in one transaction, so a rename cannot survive the
	// option update that failed after it, and no reader sees the pair disagree.
	//
	// The model is read here only to merge a stated trigger over the page's
	// current one; the storage write re-reads it inside the transaction.
	current, pageHasModel := h.knowledgeModel(ctx, bankID, node)
	var modelPatch *storage.MentalModelPatch
	if pageHasModel && pageOptions {
		// Tags live on the backing model too, so a page's tags are one value
		// across the tree, search and read surfaces rather than two that drift.
		pending := storage.MentalModelPatch{
			SourceQuery: params.SourceQuery, Tags: params.Tags, MaxTokens: params.MaxTokens,
		}
		if params.TriggerSet {
			// The trigger patches: the members the client stated are updated and
			// the rest keep the page's current values, so moving a page onto a
			// schedule does not reset how it refreshes. The merge is the same one
			// a mental-model patch uses, so the reference's directional
			// exclusivity holds here too: a stated cron clears a stale
			// refresh_after_consolidation, while enabling consolidation leaves a
			// configured cron in place.
			merged := mentalmodels.MergeTrigger(params.Trigger, current.Trigger)
			pending.Trigger = &merged
		}
		modelPatch = &pending
	}
	node, err = h.Store.UpdateKnowledgeNodeWithModel(ctx, bankID, nodeID, patch, modelPatch)
	if err != nil {
		return KnowledgeNodeResult{}, asHTTPError(err)
	}
	// A rename carries the name onto the backing model, so the model is read
	// after the patch rather than rendered from the pre-patch copy.
	model, hasModel := h.knowledgeModel(ctx, bankID, node)
	// A new source query means the content is stale, so the page is rebuilt —
	// scheduled only once the patch has committed, so a refresh is never queued
	// for a change that rolled back.
	if params.SourceQuery != nil && hasModel {
		if _, err := h.pageBuilder().enqueuePageRefresh(ctx, bankID, model.ID); err != nil {
			return KnowledgeNodeResult{}, asHTTPError(err)
		}
	}
	return KnowledgeNodeResult{Node: node, Model: model, HasModel: hasModel}, nil
}
