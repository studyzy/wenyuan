package mcp

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

func (s *Server) getMentalModel(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	modelID := stringArgument(args, "mental_model_id")
	if modelID == "" {
		return nil, errors.New("get_mental_model requires mental_model_id")
	}
	model, err := s.store.GetMentalModel(ctx, bankID, modelID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": model.ID, "name": model.Name, "source_query": model.SourceQuery, "content": model.Content}, nil
}

func (s *Server) createMentalModel(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	name := stringArgument(args, "name")
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name cannot be empty")
	}
	sourceQuery := stringArgument(args, "source_query")
	if strings.TrimSpace(sourceQuery) == "" {
		return nil, errors.New("source_query cannot be empty")
	}
	maxTokens := intArgument(args, "max_tokens", 2048)
	if maxTokens < 256 || maxTokens > 8192 {
		return nil, errors.New("max_tokens must be between 256 and 8192")
	}
	id := stringArgument(args, "mental_model_id")
	if id == "" {
		id = newID()
	}
	model, err := s.store.PutMentalModel(ctx, storage.MentalModel{
		ID:          id,
		BankID:      bankID,
		Subtype:     storage.SubtypePinned,
		Name:        name,
		SourceQuery: sourceQuery,
		Tags:        stringListArgument(args, "tags"),
		MaxTokens:   maxTokens,
		Trigger:     triggerPatch(args),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"mental_model_id": model.ID}, nil
}

func (s *Server) updateMentalModel(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	modelID := stringArgument(args, "mental_model_id")
	if modelID == "" {
		return nil, errors.New("update_mental_model requires mental_model_id")
	}
	patch := storage.MentalModelPatch{}
	if name := stringArgument(args, "name"); name != "" {
		patch.Name = &name
	}
	if sourceQuery := stringArgument(args, "source_query"); sourceQuery != "" {
		patch.SourceQuery = &sourceQuery
	}
	if tags := stringListArgument(args, "tags"); tags != nil {
		patch.Tags = &tags
	}
	if _, ok := args["max_tokens"]; ok {
		maxTokens := intArgument(args, "max_tokens", 0)
		if maxTokens < 256 || maxTokens > 8192 {
			return nil, errors.New("max_tokens must be between 256 and 8192")
		}
		patch.MaxTokens = &maxTokens
	}
	if trigger := triggerPatch(args); trigger != nil {
		patch.Trigger = &trigger
	}
	model, err := s.store.UpdateMentalModel(ctx, bankID, modelID, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"mental_model_id": model.ID}, nil
}

func (s *Server) deleteMentalModel(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	modelID := stringArgument(args, "mental_model_id")
	if modelID == "" {
		return nil, errors.New("delete_mental_model requires mental_model_id")
	}
	if err := s.store.DeleteMentalModel(ctx, bankID, modelID); err != nil {
		return nil, err
	}
	return map[string]any{"status": "deleted"}, nil
}

func (s *Server) refreshMentalModel(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	modelID := stringArgument(args, "mental_model_id")
	if modelID == "" {
		return nil, errors.New("refresh_mental_model requires mental_model_id")
	}
	_, err := s.store.EnqueueOperation(ctx, storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           bankID,
			OperationType:    storage.OperationTypeRefreshMentalModel,
			Payload:          map[string]any{storage.MentalModelIDPayloadKey: modelID},
			SerializationKey: storage.MentalModelSerializationKey(modelID),
		},
		DedupeByPayloadKey: storage.MentalModelIDPayloadKey,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"mental_model_id": modelID, "status": "queued"}, nil
}

func (s *Server) clearMentalModel(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	modelID := stringArgument(args, "mental_model_id")
	if modelID == "" {
		return nil, errors.New("clear_mental_model requires mental_model_id")
	}
	model, err := s.store.ClearMentalModel(ctx, bankID, modelID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"mental_model_id": model.ID, "status": "cleared"}, nil
}

func (s *Server) createDirective(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	name, content := stringArgument(args, "name"), stringArgument(args, "content")
	if name == "" || content == "" {
		return nil, errors.New("create_directive requires name and content")
	}
	record, err := s.store.PutDirective(ctx, storage.Directive{
		ID:       newID(),
		BankID:   bankID,
		Name:     name,
		Content:  content,
		Priority: intArgument(args, "priority", 0),
		IsActive: boolArgument(args, "is_active", true),
		Tags:     stringListArgument(args, "tags"),
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": record.ID, "name": record.Name}, nil
}

func (s *Server) deleteDirective(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	directiveID := stringArgument(args, "directive_id")
	if directiveID == "" {
		return nil, errors.New("delete_directive requires directive_id")
	}
	if err := s.store.DeleteDirective(ctx, bankID, directiveID); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

func (s *Server) listDocuments(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	page, err := s.store.ListDocuments(ctx, storage.DocumentQuery{
		BankID: bankID,
		Query:  stringArgument(args, "q"),
		Limit:  intArgument(args, "limit", 100),
		Offset: intArgument(args, "offset", 0),
	})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(page.Documents))
	for _, doc := range page.Documents {
		items = append(items, map[string]any{"id": doc.ID, "created_at": doc.CreatedAt})
	}
	return map[string]any{"items": items, "total": page.Total}, nil
}

func (s *Server) getDocument(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	docID := stringArgument(args, "document_id")
	if docID == "" {
		return nil, errors.New("get_document requires document_id")
	}
	doc, err := s.store.GetDocument(ctx, bankID, docID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": doc.ID, "original_text": doc.OriginalText, "tags": doc.Tags}, nil
}

func (s *Server) deleteDocument(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	docID := stringArgument(args, "document_id")
	if docID == "" {
		return nil, errors.New("delete_document requires document_id")
	}
	removed, err := s.store.DeleteDocument(ctx, bankID, docID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"document_id": docID, "memory_units_deleted": removed, "success": true}, nil
}

func (s *Server) listOperations(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	page, err := s.store.ListOperations(ctx, storage.OperationQuery{
		BankID: bankID,
		Status: stringArgument(args, "status"),
		Type:   stringArgument(args, "type"),
		Limit:  intArgument(args, "limit", 20),
		Offset: intArgument(args, "offset", 0),
	})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(page.Operations))
	for _, op := range page.Operations {
		items = append(items, map[string]any{"operation_id": op.OperationID, "operation_type": op.OperationType, "status": string(op.Status)})
	}
	return map[string]any{"items": items, "total": page.Total}, nil
}

func (s *Server) getOperation(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	opID := stringArgument(args, "operation_id")
	if opID == "" {
		return nil, errors.New("get_operation requires operation_id")
	}
	op, err := s.store.GetOperation(ctx, bankID, opID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": op.OperationID, "status": string(op.Status), "operation_type": op.OperationType}, nil
}

func (s *Server) cancelOperation(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	opID := stringArgument(args, "operation_id")
	if opID == "" {
		return nil, errors.New("cancel_operation requires operation_id")
	}
	op, err := s.store.CancelOperation(ctx, bankID, opID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"operation_id": op.OperationID, "status": string(op.Status)}, nil
}

func (s *Server) getBankStats(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	stats, err := s.store.BankStats(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"bank_id": stats.BankID, "total_nodes": stats.TotalNodes,
		"total_documents": stats.TotalDocuments, "total_observations": stats.TotalObservations,
	}, nil
}

func (s *Server) listTags(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	tags, err := s.store.ListTags(ctx, bankID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(tags))
	for _, tag := range tags {
		items = append(items, map[string]any{"tag": tag.Tag, "count": tag.Count})
	}
	return map[string]any{"items": items, "total": len(items)}, nil
}

// triggerPatch folds the trigger object and its legacy flat shorthand arguments
// into the patch a mental model or knowledge page stores.
func triggerPatch(args map[string]any) map[string]any {
	patch := map[string]any{}
	if trigger := objectArgument(args, "trigger"); trigger != nil {
		for key, value := range trigger {
			patch[key] = value
		}
	}
	if tagsMatch := stringArgument(args, "tags_match"); tagsMatch != "" {
		patch["tags_match"] = tagsMatch
	}
	if _, ok := args["trigger_refresh_after_consolidation"]; ok {
		patch["refresh_after_consolidation"] = boolArgument(args, "trigger_refresh_after_consolidation", false)
	}
	if _, ok := args["refresh_after_consolidation"]; ok {
		patch["refresh_after_consolidation"] = boolArgument(args, "refresh_after_consolidation", false)
	}
	if len(patch) == 0 {
		return nil
	}
	return patch
}

// stringArgument reads a string argument.
func stringArgument(args map[string]any, name string) string {
	value, _ := args[name].(string)
	return value
}

// intArgument reads an integer argument, defaulting when absent.
func intArgument(args map[string]any, name string, fallback int) int {
	switch value := args[name].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return fallback
	}
}

// boolArgument reads a boolean argument, defaulting when absent.
func boolArgument(args map[string]any, name string, fallback bool) bool {
	value, ok := args[name].(bool)
	if !ok {
		return fallback
	}
	return value
}

// stringListArgument reads a list-of-strings argument, returning nil when absent.
func stringListArgument(args map[string]any, name string) []string {
	raw, ok := args[name].([]any)
	if !ok {
		if values, ok := args[name].([]string); ok {
			return values
		}
		return nil
	}
	values := make([]string, 0, len(raw))
	for _, item := range raw {
		if text, ok := item.(string); ok {
			values = append(values, text)
		}
	}
	return values
}

// objectArgument reads an object argument, returning nil when absent.
func objectArgument(args map[string]any, name string) map[string]any {
	value, _ := args[name].(map[string]any)
	return value
}

// newID builds an opaque identifier for a record the tool creates.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("mcp-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
