package mcp

import (
	"context"
	"errors"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

func (s *Server) getKnowledgeBaseTree(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "get_knowledge_base_tree")
	if err != nil {
		return nil, err
	}
	nodes, err := s.store.ListKnowledgeNodes(ctx, bankID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(nodes))
	for _, node := range nodes {
		items = append(items, knowledgeNodeJSON(node))
	}
	return map[string]any{"roots": items}, nil
}

func (s *Server) searchKnowledgeBase(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "search_knowledge_base")
	if err != nil {
		return nil, err
	}
	query := stringArgument(args, "query")
	if query == "" {
		return nil, errors.New("search_knowledge_base requires query")
	}
	hits, err := s.store.SearchKnowledgeNodes(ctx, storage.KnowledgeSearchQuery{
		BankID: bankID,
		Query:  query,
		Limit:  intArgument(args, "limit", 10),
	})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(hits))
	for _, hit := range hits {
		items = append(items, map[string]any{"id": hit.Node.ID, "name": hit.Node.Title, "score": hit.Score})
	}
	return map[string]any{"results": items, "total": len(items)}, nil
}

func (s *Server) getKnowledgePage(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "get_knowledge_page")
	if err != nil {
		return nil, err
	}
	pageID := stringArgument(args, "page_id")
	if pageID == "" {
		return nil, errors.New("get_knowledge_page requires page_id")
	}
	node, err := s.store.GetKnowledgeNode(ctx, bankID, pageID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": node.ID, "name": node.Title, "body": node.Text}, nil
}

func (s *Server) createKnowledgeFolder(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "create_knowledge_folder")
	if err != nil {
		return nil, err
	}
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
	return knowledgeNodeJSON(node), nil
}

func (s *Server) createKnowledgePage(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "create_knowledge_page")
	if err != nil {
		return nil, err
	}
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
	return knowledgeNodeJSON(node), nil
}

func (s *Server) updateKnowledgeNode(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "update_knowledge_node")
	if err != nil {
		return nil, err
	}
	nodeID := stringArgument(args, "node_id")
	if nodeID == "" {
		return nil, errors.New("update_knowledge_node requires node_id")
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
	if err != nil {
		return nil, err
	}
	return knowledgeNodeJSON(node), nil
}

func (s *Server) deleteKnowledgeNode(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "delete_knowledge_node")
	if err != nil {
		return nil, err
	}
	nodeID := stringArgument(args, "node_id")
	if nodeID == "" {
		return nil, errors.New("delete_knowledge_node requires node_id")
	}
	if err := s.store.DeleteKnowledgeNode(ctx, bankID, nodeID); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted", "node_id": nodeID}, nil
}

// knowledgeRootParent is the literal a caller sends to move a node to the top
// level, since an MCP argument cannot express an explicit null.
const knowledgeRootParent = "root"

// knowledgeNodeJSON projects a stored node into the compact shape the tools return.
func knowledgeNodeJSON(node storage.KnowledgeNode) map[string]any {
	return map[string]any{
		"id": node.ID, "name": node.Title, "kind": string(node.Kind),
		"parent_id": node.ParentID, "tags": node.Tags,
	}
}
