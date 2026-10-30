package reporting

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
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
	var requestedTokens int
	if body.MaxTokens != nil {
		requestedTokens = *body.MaxTokens
	}
	result, serviceErr := h.CreateKnowledgePageNode(r.Context(), httpx.TenantOverridesFrom(r), bankID, parentID, name,
		CreateKnowledgePageParams{
			SourceQuery: derefString(body.SourceQuery),
			Tags:        derefStrings(body.Tags),
			MaxTokens:   requestedTokens,
			Trigger:     body.Trigger,
		})
	if serviceErr != nil {
		httpx.WriteErrorFor(w, r, serviceErr)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, CreateKnowledgePageResponse{
		PageID: result.Node.ID, MentalModelID: result.ModelID, OperationID: result.OperationID,
	})
}

// DeleteKnowledgeNode removes one node and its descendants. The bank is not
// pre-checked: an unknown bank answers the node's own not-found.
func (h *Handlers) DeleteKnowledgeNode(w http.ResponseWriter, r *http.Request) {
	bankID := PathBankID(r)
	nodeID := pathID(r, "node_id")
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
