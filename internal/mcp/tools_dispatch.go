package mcp

import (
	"context"
	"fmt"
	"time"

	"github.com/studyzy/wenyuan/internal/audit"
)

// auditableMCPTools is the set of MCP tools whose calls are recorded, mirroring
// the reference's _AUDITABLE_MCP_TOOLS roster.
var auditableMCPTools = map[string]bool{
	"retain":                  true,
	"recall":                  true,
	"reflect":                 true,
	"create_bank":             true,
	"update_bank":             true,
	"delete_bank":             true,
	"clear_memories":          true,
	"create_mental_model":     true,
	"update_mental_model":     true,
	"delete_mental_model":     true,
	"refresh_mental_model":    true,
	"clear_mental_model":      true,
	"create_directive":        true,
	"delete_directive":        true,
	"delete_document":         true,
	"cancel_operation":        true,
	"create_knowledge_folder": true,
	"create_knowledge_page":   true,
	"update_knowledge_node":   true,
	"delete_knowledge_node":   true,
}

// invokeAudited runs one tool, recording an auditable call on the trail. A tool
// that is not auditable, or whose bank has auditing off, runs unchanged.
func (s *Server) invokeAudited(ctx context.Context, name string, args map[string]any) (any, error) {
	if s.auditor == nil || !auditableMCPTools[name] {
		return s.invoke(ctx, name, args)
	}
	bankID := ""
	if bank, ok := args["bank_id"].(string); ok {
		bankID = bank
	}
	if bankID == "" {
		bankID = defaultBank(ctx)
	}
	if !s.auditor.ShouldAudit(ctx, name, bankID) {
		return s.invoke(ctx, name, args)
	}

	entry := audit.Entry{
		Action:    name,
		Transport: audit.TransportMCP,
		BankID:    bankID,
		StartedAt: time.Now().UTC(),
		Request:   args,
	}
	payload, err := s.invoke(ctx, name, args)
	entry.EndedAt = time.Now().UTC()
	if err != nil {
		entry.Error = err.Error()
	} else {
		entry.Response = mcpAuditResponse(payload)
	}
	s.auditor.Record(context.WithoutCancel(ctx), entry)
	return payload, err
}

// mcpAuditResponse shapes a tool result for the trail, wrapping a list or a
// scalar the way the reference's MCP audit wrapper does.
func mcpAuditResponse(payload any) map[string]any {
	switch typed := payload.(type) {
	case nil:
		return nil
	case map[string]any:
		return typed
	case []any:
		return map[string]any{"items": typed}
	case string:
		return map[string]any{"text": typed}
	default:
		return map[string]any{"result": typed}
	}
}

// invoke runs one tool against the store.
func (s *Server) invoke(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "retain":
		return s.retain(ctx, args)
	case "sync_retain":
		return s.syncRetain(ctx, args)
	case "recall":
		return s.recall(ctx, args)
	case "reflect":
		return s.reflect(ctx, args)
	case "list_banks":
		return s.listBanks(ctx, args)
	case "create_bank":
		return s.createBank(ctx, args)
	case "get_bank":
		return s.getBank(ctx, args)
	case "update_bank":
		return s.updateBank(ctx, args)
	case "delete_bank":
		return s.deleteBank(ctx, args)
	case "get_bank_stats":
		return s.getBankStats(ctx, args)
	case "clear_memories":
		return s.clearMemories(ctx, args)
	case "list_memories":
		return s.listMemories(ctx, args)
	case "get_memory":
		return s.getMemory(ctx, args)
	case "update_memory":
		return s.updateMemory(ctx, args)
	case "invalidate_memory":
		return s.invalidateMemory(ctx, args)
	case "list_mental_models":
		return s.listMentalModels(ctx, args)
	case "get_mental_model":
		return s.getMentalModel(ctx, args)
	case "create_mental_model":
		return s.createMentalModel(ctx, args)
	case "update_mental_model":
		return s.updateMentalModel(ctx, args)
	case "delete_mental_model":
		return s.deleteMentalModel(ctx, args)
	case "refresh_mental_model":
		return s.refreshMentalModel(ctx, args)
	case "clear_mental_model":
		return s.clearMentalModel(ctx, args)
	case "list_directives":
		return s.listDirectives(ctx, args)
	case "create_directive":
		return s.createDirective(ctx, args)
	case "delete_directive":
		return s.deleteDirective(ctx, args)
	case "list_documents":
		return s.listDocuments(ctx, args)
	case "get_document":
		return s.getDocument(ctx, args)
	case "delete_document":
		return s.deleteDocument(ctx, args)
	case "list_operations":
		return s.listOperations(ctx, args)
	case "get_operation":
		return s.getOperation(ctx, args)
	case "cancel_operation":
		return s.cancelOperation(ctx, args)
	case "list_tags":
		return s.listTags(ctx, args)
	case "get_knowledge_base_tree":
		return s.getKnowledgeBaseTree(ctx, args)
	case "search_knowledge_base":
		return s.searchKnowledgeBase(ctx, args)
	case "get_knowledge_page":
		return s.getKnowledgePage(ctx, args)
	case "create_knowledge_folder":
		return s.createKnowledgeFolder(ctx, args)
	case "create_knowledge_page":
		return s.createKnowledgePage(ctx, args)
	case "update_knowledge_node":
		return s.updateKnowledgeNode(ctx, args)
	case "delete_knowledge_node":
		return s.deleteKnowledgeNode(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

// bankArgument resolves the bank a tool acts on: an explicit bank_id argument,
// or the single-bank server's fixed bank when the argument is absent.
func (s *Server) bankArgument(ctx context.Context, args map[string]any, tool string) (string, error) {
	if bank := stringArgument(args, "bank_id"); bank != "" {
		return bank, nil
	}
	if bank := defaultBank(ctx); bank != "" {
		return bank, nil
	}
	return "", fmt.Errorf("%s requires bank_id", tool)
}
