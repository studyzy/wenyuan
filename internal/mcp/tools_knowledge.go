package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/reporting"
	"github.com/studyzy/wenyuan/internal/storage"
)

// getKnowledgeBaseTree returns the bank's knowledge tree: the flat node list
// assembled into nested roots, each node projected the way the reference's MCP
// tools render one.
func (s *Server) getKnowledgeBaseTree(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	roots, err := s.knowledgeTreePayload(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"roots": roots}, nil
}

func (s *Server) searchKnowledgeBase(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	query := stringArgument(args, "query")
	if query == "" {
		return nil, invalidArgument("search_knowledge_base requires query")
	}
	// The limit is clamped rather than rejected: the HTTP route answers an
	// out-of-range limit with a 422, but "an agent that asked for 500 pages wants
	// results, not a validation round trip" (mcp_tools.py::_do_search_knowledge_base).
	limit := intArgument(args, "limit", 10)
	if limit < 1 {
		limit = 1
	}
	if limit > 50 {
		limit = 50
	}
	hits, err := s.store.SearchKnowledgeNodes(ctx, storage.KnowledgeSearchQuery{
		BankID: bankID,
		Query:  query,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	models, err := s.knowledgeModels(ctx, bankID)
	if err != nil {
		return nil, err
	}
	// The result is the same projection the HTTP search route serves — the page's
	// identity, the question its model answers, a snippet of its text and when it
	// last refreshed — because an agent picking a page needs more than a score.
	items := make([]reporting.KnowledgeSearchResult, 0, len(hits))
	for _, hit := range hits {
		model, hasModel := models[hit.Node.MentalModelID]
		items = append(items, reporting.ProjectKnowledgeSearchHit(hit, model, hasModel))
	}
	return map[string]any{"results": items, "total": len(items)}, nil
}

func (s *Server) getKnowledgePage(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	pageID := stringArgument(args, "page_id")
	if pageID == "" {
		return nil, invalidArgument("get_knowledge_page requires page_id")
	}
	node, err := s.store.GetKnowledgeNode(ctx, bankID, pageID)
	if errors.Is(err, storage.ErrKnowledgeNodeNotFound) {
		return notFoundAnswer("Knowledge page '" + pageID + "' not found in bank '" + bankID + "'"), nil
	}
	if err != nil {
		return nil, err
	}
	// A folder is not a page: the reference's page read joins on kind='page', so a
	// folder answers the same not-found rather than an empty document.
	if node.Kind != storage.KnowledgeKindPage {
		return notFoundAnswer("Knowledge page '" + pageID + "' not found in bank '" + bankID + "'"), nil
	}
	model, hasModel := s.knowledgeModel(ctx, bankID, node)
	// The body is the backing model's content, which is what the reference
	// renders (`page_markdown.render_document(node)` over the merged mentally-model
	// dict); the node row itself carries no content column.
	body := node.Text
	if hasModel {
		body = model.Content
	}
	// The rendered document already carries the body under its frontmatter block,
	// so it is returned once — the reference's `_do_get_knowledge_page` returns
	// the same seven members and no separate body.
	return reporting.RenderKnowledgePage(node, model, hasModel, &body), nil
}

func (s *Server) createKnowledgeFolder(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	name := stringArgument(args, "name")
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name cannot be empty")
	}
	node, err := s.store.PutKnowledgeNode(ctx, storage.KnowledgeNode{
		ID:       newID(),
		BankID:   bankID,
		ParentID: stringArgument(args, "parent_id"),
		Kind:     storage.KnowledgeKindFolder,
		Title:    name,
	})
	if err != nil {
		return nil, err
	}
	view, err := s.knowledgeNodePayload(ctx, node)
	if err != nil {
		return nil, err
	}
	if view.Kind == string(storage.KnowledgeKindFolder) {
		view.Children = []knowledgeNodeView{}
	}
	return view, nil
}

// createKnowledgePage creates a page through the same code the HTTP route runs,
// so the page gets a backing mental model, the resolved knowledge-page trigger,
// its content budget and a queued refresh — the four things a bare tree row
// lacks. The reference's tool then submits the refresh and reports the
// operation the caller polls (`mcp_tools.py::_do_create_knowledge_page`).
func (s *Server) createKnowledgePage(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	name := stringArgument(args, "name")
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name cannot be empty")
	}
	sourceQuery := stringArgument(args, "source_query")
	if strings.TrimSpace(sourceQuery) == "" {
		return nil, errors.New("source_query cannot be empty")
	}
	if message := validateTagsMatchArgument(args); message != "" {
		return nil, errors.New(message)
	}
	trigger, err := validatedTriggerPatch(args)
	if err != nil {
		return nil, err
	}
	parentID, err := s.knowledgeParentArgument(ctx, bankID, args)
	if err != nil {
		return nil, err
	}
	// A page name is unique within its folder, case-insensitively, so the create
	// answers "a page named '<name>' already exists in this folder" rather than
	// letting two nodes shadow each other — the reference's uniqueness index.
	if err := s.assertNoDuplicatePage(ctx, bankID, parentID, name); err != nil {
		return nil, err
	}
	maxTokens := intArgument(args, "max_tokens", 0)
	result, serviceErr := s.handlers.CreateKnowledgePageNode(ctx, nil, bankID, parentID, name,
		reporting.CreateKnowledgePageParams{
			SourceQuery: sourceQuery,
			Tags:        stringListArgument(args, "tags"),
			MaxTokens:   maxTokens,
			Trigger:     trigger,
		})
	if serviceErr != nil {
		if serviceErr.Status == httpx.KindConflict {
			return errorPayload(serviceErr.Detail), nil
		}
		return nil, errors.New(serviceErr.Detail)
	}
	return compactToolText(createKnowledgePageAck{
		PageID:        result.Node.ID,
		MentalModelID: result.ModelID,
		OperationID:   result.OperationID,
		Status:        "created",
		Message:       "Page '" + name + "' created. Content is being generated asynchronously.",
	}), nil
}

// updateKnowledgeNode patches a node through the same code the HTTP route runs:
// the page options reach the backing model, a stated trigger is merged over the
// page's current one, and a new source query schedules the rebuild.
func (s *Server) updateKnowledgeNode(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	nodeID := stringArgument(args, "node_id")
	if nodeID == "" {
		return nil, invalidArgument("update_knowledge_node requires node_id")
	}
	if message := validateTagsMatchArgument(args); message != "" {
		return nil, errors.New(message)
	}
	trigger, err := validatedTriggerPatch(args)
	if err != nil {
		return nil, err
	}
	params := reporting.KnowledgeNodeParams{Trigger: trigger, TriggerSet: trigger != nil}
	if raw, present := args["name"]; present && raw != nil {
		name := stringArgument(args, "name")
		params.Name = &name
	}
	if _, present := args["tags"]; present {
		tags := stringListArgument(args, "tags")
		if tags == nil {
			tags = []string{}
		}
		params.Tags = &tags
	}
	if raw, present := args["source_query"]; present && raw != nil {
		sourceQuery := stringArgument(args, "source_query")
		params.SourceQuery = &sourceQuery
	}
	if _, present := args["max_tokens"]; present {
		maxTokens := intArgument(args, "max_tokens", 0)
		params.MaxTokens = &maxTokens
	}
	if raw, present := args["parent_id"]; present && raw != nil {
		// An MCP argument cannot express an explicit null, so the literal "root"
		// is the caller's way to move a node to the top level.
		parent := stringArgument(args, "parent_id")
		if parent == knowledgeRootParent {
			parent = ""
		}
		params.Move = true
		params.NewParent = &parent
	}
	// The engine refuses a patch that names nothing too, because a no-op
	// authorizes no operation; kept here so an agent gets a sentence it can act
	// on rather than an exception. The reference words it for its own tool.
	if params.Name == nil && !params.Move && params.SourceQuery == nil &&
		params.Tags == nil && params.MaxTokens == nil && !params.TriggerSet {
		return errorPayload("Provide name, parent_id, source_query, tags, max_tokens, " +
			"trigger, and/or refresh_after_consolidation to update"), nil
	}
	result, serviceErr := s.handlers.UpdateKnowledgeNodeRecord(ctx, bankID, nodeID, params)
	if serviceErr != nil {
		if serviceErr.Status == httpx.KindNotFound {
			return errorPayload("Knowledge node '" + nodeID + "' not found in bank '" + bankID + "'"), nil
		}
		return nil, errors.New(serviceErr.Detail)
	}
	view, err := s.knowledgeNodePayload(ctx, result.Node)
	if err != nil {
		return nil, err
	}
	return view, nil
}

func (s *Server) deleteKnowledgeNode(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	nodeID := stringArgument(args, "node_id")
	if nodeID == "" {
		return nil, invalidArgument("delete_knowledge_node requires node_id")
	}
	if err := s.store.DeleteKnowledgeNode(ctx, bankID, nodeID); err != nil {
		if errors.Is(err, storage.ErrKnowledgeNodeNotFound) {
			return notFoundAnswer("Knowledge node '" + nodeID + "' not found in bank '" + bankID + "'"), nil
		}
		return nil, err
	}
	return compactToolText(deleteKnowledgeNodeAck{Status: "deleted", NodeID: nodeID}), nil
}

// knowledgeRootParent is the literal a caller sends to move a node to the top
// level, since an MCP argument cannot express an explicit null.
const knowledgeRootParent = "root"

// knowledgeModels returns the bank's mental models keyed by identifier, the
// lookup the knowledge tools use to render a page's model-derived fields.
func (s *Server) knowledgeModels(ctx context.Context, bankID string) (map[string]storage.MentalModel, error) {
	models, err := s.store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return nil, err
	}
	out := make(map[string]storage.MentalModel, len(models))
	for _, model := range models {
		out[model.ID] = model
	}
	return out, nil
}

// knowledgeModel returns the mental model backing a page, and whether it exists.
// A page whose model is gone has no description or timestamp to render, which is
// what the reference reports for it too.
func (s *Server) knowledgeModel(ctx context.Context, bankID string, node storage.KnowledgeNode) (storage.MentalModel, bool) {
	if node.Kind != storage.KnowledgeKindPage || node.MentalModelID == "" {
		return storage.MentalModel{}, false
	}
	model, err := s.store.GetMentalModel(ctx, bankID, node.MentalModelID)
	if err != nil {
		return storage.MentalModel{}, false
	}
	return model, true
}

// knowledgeParentArgument reads the `parent_id` of a create. It must name a
// folder in this bank, so the tree cannot be given a dangling reference or a
// page with children.
func (s *Server) knowledgeParentArgument(ctx context.Context, bankID string, args map[string]any) (*string, error) {
	raw, present := args["parent_id"]
	if !present || raw == nil {
		return nil, nil
	}
	parent := stringArgument(args, "parent_id")
	if strings.TrimSpace(parent) == "" {
		return nil, nil
	}
	node, err := s.store.GetKnowledgeNode(ctx, bankID, parent)
	if errors.Is(err, storage.ErrKnowledgeNodeNotFound) {
		return nil, errors.New("Parent folder '" + parent + "' not found")
	}
	if err != nil {
		return nil, err
	}
	if node.Kind != storage.KnowledgeKindFolder {
		return nil, errors.New("Parent '" + parent + "' is not a folder")
	}
	return &parent, nil
}

// assertNoDuplicatePage refuses a second page of the same name in one folder,
// which the reference reports as the create's "already exists" answer.
func (s *Server) assertNoDuplicatePage(ctx context.Context, bankID string, parentID *string, name string) error {
	nodes, err := s.store.ListKnowledgeNodes(ctx, bankID)
	if err != nil {
		return err
	}
	want := ""
	if parentID != nil {
		want = *parentID
	}
	for _, node := range nodes {
		if node.Kind == storage.KnowledgeKindPage && node.ParentID == want && strings.EqualFold(node.Title, name) {
			return errors.New("A page named '" + name + "' already exists in this folder")
		}
	}
	return nil
}

// createKnowledgePageAck is the answer to creating a knowledge page: the node, the
// mental model backing it, and the refresh operation the content comes from.
type createKnowledgePageAck struct {
	PageID        string `json:"page_id"`
	MentalModelID string `json:"mental_model_id"`
	OperationID   string `json:"operation_id"`
	Status        string `json:"status"`
	Message       string `json:"message"`
}
