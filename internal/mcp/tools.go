package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/studyzy/wenyuan/internal/storage"
)

// serverVersion is the reported server version.
const serverVersion = "0.1.0"

// tool is one entry of the tools/list response.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// toolDefinitions is the roster the server advertises. Each tool maps onto one
// storage call, so the protocol layer adds no behavior of its own.
func toolDefinitions() []tool {
	stringProperty := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	integerProperty := func(description string, fallback int) map[string]any {
		return map[string]any{"type": "integer", "description": description, "default": fallback}
	}
	object := func(required []string, properties map[string]any) map[string]any {
		schema := map[string]any{"type": "object", "properties": properties}
		if len(required) > 0 {
			schema["required"] = required
		}
		return schema
	}

	return []tool{
		{
			Name:        "retain",
			Description: "Persist a memory into a bank. Content becomes a document whose extracted facts are recallable.",
			InputSchema: object([]string{"bank_id", "content"}, map[string]any{
				"bank_id": stringProperty("the bank that owns the memory"),
				"content": stringProperty("the text to retain"),
				"context": stringProperty("optional surrounding context"),
			}),
		},
		{
			Name:        "recall",
			Description: "Search a bank's memories and return the best matches.",
			InputSchema: object([]string{"bank_id", "query"}, map[string]any{
				"bank_id": stringProperty("the bank to search"),
				"query":   stringProperty("the search query"),
				"limit":   integerProperty("how many matches to return", 10),
			}),
		},
		{
			Name:        "reflect",
			Description: "Answer a question from a bank's memories.",
			InputSchema: object([]string{"bank_id", "question"}, map[string]any{
				"bank_id":  stringProperty("the bank to reflect over"),
				"question": stringProperty("the question to answer"),
			}),
		},
		{
			Name:        "list_banks",
			Description: "List the banks this server holds.",
			InputSchema: object(nil, map[string]any{
				"limit": integerProperty("how many banks to return", 50),
			}),
		},
		{
			Name:        "get_bank",
			Description: "Read one bank's settings.",
			InputSchema: object([]string{"bank_id"}, map[string]any{
				"bank_id": stringProperty("the bank to read"),
			}),
		},
		{
			Name:        "list_memories",
			Description: "List a bank's memory units.",
			InputSchema: object([]string{"bank_id"}, map[string]any{
				"bank_id": stringProperty("the bank to list"),
				"limit":   integerProperty("how many units to return", 20),
			}),
		},
		{
			Name:        "get_memory",
			Description: "Read one memory unit by identifier.",
			InputSchema: object([]string{"bank_id", "memory_id"}, map[string]any{
				"bank_id":   stringProperty("the bank that owns the memory"),
				"memory_id": stringProperty("the memory identifier"),
			}),
		},
		{
			Name:        "list_mental_models",
			Description: "List a bank's mental models.",
			InputSchema: object([]string{"bank_id"}, map[string]any{
				"bank_id": stringProperty("the bank to list"),
			}),
		},
		{
			Name:        "list_directives",
			Description: "List a bank's directives.",
			InputSchema: object([]string{"bank_id"}, map[string]any{
				"bank_id": stringProperty("the bank to list"),
			}),
		},
	}
}

// invoke runs one tool against the store.
func (s *Server) invoke(ctx context.Context, name string, args map[string]any) (any, error) {
	switch name {
	case "retain":
		return s.retain(ctx, args)
	case "recall":
		return s.recall(ctx, args)
	case "reflect":
		return s.reflect(ctx, args)
	case "list_banks":
		return s.listBanks(ctx, args)
	case "get_bank":
		return s.getBank(ctx, args)
	case "list_memories":
		return s.listMemories(ctx, args)
	case "get_memory":
		return s.getMemory(ctx, args)
	case "list_mental_models":
		return s.listMentalModels(ctx, args)
	case "list_directives":
		return s.listDirectives(ctx, args)
	default:
		return nil, fmt.Errorf("unknown tool %q", name)
	}
}

// retain persists one memory write.
func (s *Server) retain(ctx context.Context, args map[string]any) (any, error) {
	bankID, content := stringArgument(args, "bank_id"), stringArgument(args, "content")
	if bankID == "" || content == "" {
		return nil, errors.New("retain requires bank_id and content")
	}
	outcome, err := s.store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bankID,
		Items:  []storage.RetainItem{{Content: content, Context: stringArgument(args, "context")}},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"bank_id": bankID, "unit_ids": outcome.ItemUnitIDs}, nil
}

// recall searches a bank's memories.
func (s *Server) recall(ctx context.Context, args map[string]any) (any, error) {
	bankID, query := stringArgument(args, "bank_id"), stringArgument(args, "query")
	if bankID == "" || query == "" {
		return nil, errors.New("recall requires bank_id and query")
	}
	result, err := s.store.Recall(ctx, storage.RecallQuery{
		BankID: bankID, Query: query, Limit: intArgument(args, "limit", 10),
	})
	if err != nil {
		return nil, err
	}
	matches := make([]map[string]any, 0, len(result.Matches))
	for _, match := range result.Matches {
		matches = append(matches, map[string]any{
			"id": match.ID, "text": match.Text, "type": match.Type, "document_id": match.DocumentID,
		})
	}
	return map[string]any{"bank_id": bankID, "matches": matches}, nil
}

// reflect answers a question from a bank's memories.
func (s *Server) reflect(ctx context.Context, args map[string]any) (any, error) {
	bankID, question := stringArgument(args, "bank_id"), stringArgument(args, "question")
	if bankID == "" || question == "" {
		return nil, errors.New("reflect requires bank_id and question")
	}
	outcome, err := s.store.Reflect(ctx, storage.ReflectQuery{BankID: bankID, Query: question})
	if err != nil {
		return nil, err
	}
	return map[string]any{"bank_id": bankID, "answer": outcome.Answer}, nil
}

// listBanks lists the server's banks.
func (s *Server) listBanks(ctx context.Context, args map[string]any) (any, error) {
	page, err := s.store.ListBanks(ctx, storage.BankQuery{Limit: intArgument(args, "limit", 50)})
	if err != nil {
		return nil, err
	}
	banks := make([]map[string]any, 0, len(page.Banks))
	for _, bank := range page.Banks {
		banks = append(banks, map[string]any{"id": bank.BankID, "name": bank.Name})
	}
	return map[string]any{"banks": banks, "total": page.Total}, nil
}

// getBank reads one bank.
func (s *Server) getBank(ctx context.Context, args map[string]any) (any, error) {
	bankID := stringArgument(args, "bank_id")
	if bankID == "" {
		return nil, errors.New("get_bank requires bank_id")
	}
	bank, err := s.store.GetBank(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": bank.BankID, "name": bank.Name}, nil
}

// listMemories lists a bank's memory units.
func (s *Server) listMemories(ctx context.Context, args map[string]any) (any, error) {
	bankID := stringArgument(args, "bank_id")
	if bankID == "" {
		return nil, errors.New("list_memories requires bank_id")
	}
	page, err := s.store.ListMemories(ctx, storage.MemoryQuery{
		BankID: bankID, Limit: intArgument(args, "limit", 20),
	})
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(page.Memories))
	for _, unit := range page.Memories {
		items = append(items, map[string]any{
			"id": unit.ID, "text": unit.Text, "type": string(unit.FactType),
		})
	}
	return map[string]any{"memories": items, "total": page.Total}, nil
}

// getMemory reads one memory unit.
func (s *Server) getMemory(ctx context.Context, args map[string]any) (any, error) {
	bankID, memoryID := stringArgument(args, "bank_id"), stringArgument(args, "memory_id")
	if bankID == "" || memoryID == "" {
		return nil, errors.New("get_memory requires bank_id and memory_id")
	}
	unit, err := s.store.GetMemory(ctx, bankID, memoryID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": unit.ID, "text": unit.Text, "type": string(unit.FactType)}, nil
}

// listMentalModels lists a bank's mental models.
func (s *Server) listMentalModels(ctx context.Context, args map[string]any) (any, error) {
	bankID := stringArgument(args, "bank_id")
	if bankID == "" {
		return nil, errors.New("list_mental_models requires bank_id")
	}
	models, err := s.store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(models))
	for _, model := range models {
		items = append(items, map[string]any{"id": model.ID, "name": model.Name})
	}
	return map[string]any{"mental_models": items}, nil
}

// listDirectives lists a bank's directives.
func (s *Server) listDirectives(ctx context.Context, args map[string]any) (any, error) {
	bankID := stringArgument(args, "bank_id")
	if bankID == "" {
		return nil, errors.New("list_directives requires bank_id")
	}
	directives, err := s.store.ListDirectives(ctx, bankID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(directives))
	for _, directive := range directives {
		items = append(items, map[string]any{"id": directive.ID, "name": directive.Name})
	}
	return map[string]any{"directives": items}, nil
}

// toolResult wraps a tool payload in the content shape the protocol returns.
func toolResult(payload any) map[string]any {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return toolError(err.Error())
	}
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": string(encoded)}},
		"isError": false,
	}
}

// toolError reports a tool failure as a result carrying the message.
func toolError(message string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": message}},
		"isError": true,
	}
}
