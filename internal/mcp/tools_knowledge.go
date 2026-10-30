package mcp

import (
	"context"
	"errors"
	"strings"

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
	node, err := s.store.PutKnowledgeNode(ctx, storage.KnowledgeNode{
		ID:       newID(),
		BankID:   bankID,
		ParentID: stringArgument(args, "parent_id"),
		Kind:     storage.KnowledgeKindPage,
		Title:    name,
		Tags:     stringListArgument(args, "tags"),
		Managed:  true,
	})
	if err != nil {
		return nil, err
	}
	view, err := s.knowledgeNodePayload(ctx, node)
	if err != nil {
		return nil, err
	}
	return view, nil
}

func (s *Server) updateKnowledgeNode(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	nodeID := stringArgument(args, "node_id")
	if nodeID == "" {
		return nil, invalidArgument("update_knowledge_node requires node_id")
	}
	patch := storage.KnowledgeNodePatch{}
	if name := stringArgument(args, "name"); name != "" {
		patch.Title = &name
	}
	if tags := stringListArgument(args, "tags"); tags != nil {
		patch.Tags = &tags
	}
	if raw, ok := args["parent_id"]; ok {
		parent, _ := raw.(string)
		// The tool cannot express an explicit null, so the literal "root" is the
		// caller's way to move a node to the top level.
		if parent == knowledgeRootParent {
			parent = ""
		}
		patch.ParentID = &parent
	}
	node, err := s.store.UpdateKnowledgeNode(ctx, bankID, nodeID, patch)
	if errors.Is(err, storage.ErrKnowledgeNodeNotFound) {
		return notFoundAnswer("Knowledge node '" + nodeID + "' not found in bank '" + bankID + "'"), nil
	}
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
