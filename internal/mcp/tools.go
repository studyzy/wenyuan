package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/studyzy/wenyuan/internal/bankapi"
	"github.com/studyzy/wenyuan/internal/storage"
)

// serverVersion is the reference release reported in serverInfo.
const serverVersion = "0.10.3"

// tool is one entry of the tools/list response.
type tool struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

// roster assembles the reference tool roster in registration order.
func roster() []tool {
	defs := coreTools()
	defs = append(defs, mentalModelTools()...)
	defs = append(defs, memoryTools()...)
	defs = append(defs, adminTools()...)
	defs = append(defs, knowledgeTools()...)
	return defs
}

// singleBankOmitted is the bank-management subset the reference withholds when
// a server serves exactly one bank: the tools that would otherwise manage the
// fleet of banks rather than the memories inside one.
var singleBankOmitted = map[string]bool{
	"list_banks":     true,
	"create_bank":    true,
	"get_bank_stats": true,
}

// toolDefinitions is the roster the server advertises. A multi-bank server
// exposes every tool with an optional bank_id argument; a single-bank server
// (the reference's /mcp/{bank_id}) drops the bank-management tools and the
// bank_id argument, because the bank is fixed by the URL.
func toolDefinitions(multiBank bool) []tool {
	defs := roster()
	if multiBank {
		return defs
	}
	single := make([]tool, 0, len(defs))
	for _, def := range defs {
		if singleBankOmitted[def.Name] {
			continue
		}
		if properties, ok := def.InputSchema["properties"].(map[string]any); ok {
			delete(properties, "bank_id")
		}
		single = append(single, def)
	}
	return single
}

// retain persists one memory write.
func (s *Server) retain(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "retain")
	if err != nil {
		return nil, err
	}
	content := stringArgument(args, "content")
	if content == "" {
		return nil, errors.New("retain requires content")
	}
	item := retainItem(args, content)
	if item.Context == "" {
		item.Context = "general"
	}
	outcome, err := s.store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bankID,
		Items:  []storage.RetainItem{item},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "accepted", "bank_id": bankID, "unit_ids": outcome.ItemUnitIDs}, nil
}

// syncRetain persists one memory write and reports the stored unit identifiers.
func (s *Server) syncRetain(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "sync_retain")
	if err != nil {
		return nil, err
	}
	content := stringArgument(args, "content")
	if content == "" {
		return nil, errors.New("sync_retain requires content")
	}
	item := retainItem(args, content)
	if item.Context == "" {
		item.Context = "general"
	}
	outcome, err := s.store.RetainWrite(ctx, storage.RetainRequest{
		BankID: bankID,
		Items:  []storage.RetainItem{item},
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"status": "stored", "bank_id": bankID, "unit_ids": outcome.ItemUnitIDs, "async": false}, nil
}

// retainItem maps the retain tool's arguments onto one stored item.
func retainItem(args map[string]any, content string) storage.RetainItem {
	item := storage.RetainItem{
		Content:    content,
		Context:    stringArgument(args, "context"),
		DocumentID: stringArgument(args, "document_id"),
		Tags:       stringListArgument(args, "tags"),
		Metadata:   objectArgument(args, "metadata"),
		Strategy:   stringArgument(args, "strategy"),
		UpdateMode: stringArgument(args, "update_mode"),
	}
	if raw := stringArgument(args, "timestamp"); raw != "" {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			item.Timestamp, item.HasTimestamp = &parsed, true
		}
	}
	return item
}

// recall searches a bank's memories.
func (s *Server) recall(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "recall")
	if err != nil {
		return nil, err
	}
	query := stringArgument(args, "query")
	if query == "" {
		return nil, errors.New("recall requires query")
	}
	result, err := s.store.Recall(ctx, storage.RecallQuery{
		BankID:             bankID,
		Query:              query,
		Types:              stringListArgument(args, "types"),
		PreferObservations: boolArgument(args, "prefer_observations", false),
		Budget:             stringArgument(args, "budget"),
		MaxTokens:          intArgument(args, "max_tokens", 4096),
		Tags:               stringListArgument(args, "tags"),
		TagsMatch:          stringArgument(args, "tags_match"),
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
	bankID, err := s.bankArgument(ctx, args, "reflect")
	if err != nil {
		return nil, err
	}
	query := stringArgument(args, "query")
	if query == "" {
		return nil, errors.New("reflect requires query")
	}
	outcome, err := s.store.Reflect(ctx, storage.ReflectQuery{BankID: bankID, Query: query})
	if err != nil {
		return nil, err
	}
	return map[string]any{"bank_id": bankID, "answer": outcome.Answer}, nil
}

// listBanks lists the server's banks. The rows are the same public list shape
// the HTTP collection route serves, so a client reads one contract.
func (s *Server) listBanks(ctx context.Context, args map[string]any) (any, error) {
	page, err := s.store.ListBanks(ctx, storage.BankQuery{
		Search: stringArgument(args, "query"),
		Limit:  intArgument(args, "limit", 100),
		Offset: intArgument(args, "offset", 0),
	})
	if err != nil {
		return nil, err
	}
	rows := make([]bankapi.BankListRow, 0, len(page.Banks))
	for _, bank := range page.Banks {
		rows = append(rows, bankapi.ToListRow(bank, s.primaryAlias(ctx, bank.BankID), page.MatchedAliases[bank.BankID]))
	}
	return map[string]any{"banks": rows, "total": page.Total, "limit": page.Limit, "offset": page.Offset}, nil
}

// primaryAlias names the bank's promoted alias, or nil when it is presented
// under its own id. It is the request-free form of the HTTP list handler's
// displayAlias.
func (s *Server) primaryAlias(ctx context.Context, bankID string) *string {
	aliases, err := s.store.ListAliases(ctx, bankID)
	if err != nil {
		return nil
	}
	for _, alias := range aliases {
		if alias.Primary {
			value := alias.Alias
			return &value
		}
	}
	return nil
}

// bankProfile is the profile shape the reference's bank MCP tools return: the
// identity, the display name, the graded traits and the mission. The traits are
// normalized so a bank stored without them reads as the defaults, matching the
// HTTP profile.
func bankProfile(bank storage.Bank) map[string]any {
	return map[string]any{
		"bank_id":     bank.BankID,
		"name":        bank.Name,
		"disposition": bank.NormalizedDisposition(),
		"mission":     bank.Mission,
	}
}

// createBank creates a bank.
func (s *Server) createBank(ctx context.Context, args map[string]any) (any, error) {
	bankID := stringArgument(args, "bank_id")
	if bankID == "" {
		return nil, errors.New("create_bank requires bank_id")
	}
	bank, err := s.store.CreateBank(ctx, storage.Bank{
		BankID:  bankID,
		Name:    stringArgument(args, "name"),
		Mission: stringArgument(args, "mission"),
	})
	if err != nil {
		return nil, err
	}
	return bankProfile(bank), nil
}

// getBank reads one bank.
func (s *Server) getBank(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "get_bank")
	if err != nil {
		return nil, err
	}
	bank, err := s.store.GetBank(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return bankProfile(bank), nil
}

// updateBank applies a partial bank update.
func (s *Server) updateBank(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "update_bank")
	if err != nil {
		return nil, err
	}
	patch := storage.BankPatch{}
	if name := stringArgument(args, "name"); name != "" {
		patch.Name = &name
	}
	if mission := stringArgument(args, "mission"); mission != "" {
		patch.Mission = &mission
	}
	if config := objectArgument(args, "config_updates"); config != nil {
		patch.Config = &config
	}
	bank, err := s.store.UpdateBank(ctx, bankID, patch)
	if err != nil {
		return nil, err
	}
	return bankProfile(bank), nil
}

// deleteBank removes a bank and its data.
func (s *Server) deleteBank(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "delete_bank")
	if err != nil {
		return nil, err
	}
	if _, err := s.store.DeleteBank(ctx, bankID); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

// clearMemories removes a bank's memory units.
func (s *Server) clearMemories(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "clear_memories")
	if err != nil {
		return nil, err
	}
	removed, err := s.store.ClearBankMemories(ctx, bankID, stringArgument(args, "type"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted_count": removed}, nil
}

// listMemories lists a bank's memory units.
func (s *Server) listMemories(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "list_memories")
	if err != nil {
		return nil, err
	}
	page, err := s.store.ListMemories(ctx, storage.MemoryQuery{
		BankID:    bankID,
		Type:      stringArgument(args, "type"),
		Q:         stringArgument(args, "q"),
		Tags:      stringListArgument(args, "tags"),
		TagsMatch: stringArgument(args, "tags_match"),
		Limit:     intArgument(args, "limit", 100),
		Offset:    intArgument(args, "offset", 0),
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
	return map[string]any{"items": items, "total": page.Total}, nil
}

// getMemory reads one memory unit.
func (s *Server) getMemory(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "get_memory")
	if err != nil {
		return nil, err
	}
	memoryID := stringArgument(args, "memory_id")
	if memoryID == "" {
		return nil, errors.New("get_memory requires memory_id")
	}
	unit, err := s.store.GetMemory(ctx, bankID, memoryID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": unit.ID, "text": unit.Text, "type": string(unit.FactType)}, nil
}

// updateMemory applies a partial update to one memory unit.
func (s *Server) updateMemory(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "update_memory")
	if err != nil {
		return nil, err
	}
	memoryID := stringArgument(args, "memory_id")
	if memoryID == "" {
		return nil, errors.New("update_memory requires memory_id")
	}
	patch := storage.MemoryPatch{}
	if text := stringArgument(args, "text"); text != "" {
		patch.Text = &text
	}
	if tags := stringListArgument(args, "tags"); tags != nil {
		patch.Tags = &tags
	}
	unit, err := s.store.UpdateMemory(ctx, bankID, memoryID, patch)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": unit.ID, "text": unit.Text}, nil
}

// invalidateMemory clears a memory's observations.
func (s *Server) invalidateMemory(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "invalidate_memory")
	if err != nil {
		return nil, err
	}
	memoryID := stringArgument(args, "memory_id")
	if memoryID == "" {
		return nil, errors.New("invalidate_memory requires memory_id")
	}
	removed, err := s.store.DeleteObservations(ctx, bankID, memoryID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted_count": removed}, nil
}

// listMentalModels lists a bank's mental models.
func (s *Server) listMentalModels(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "list_mental_models")
	if err != nil {
		return nil, err
	}
	models, err := s.store.ListMentalModels(ctx, bankID, "")
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(models))
	for _, model := range models {
		items = append(items, map[string]any{"id": model.ID, "name": model.Name})
	}
	return map[string]any{"items": items}, nil
}

// listDirectives lists a bank's directives.
func (s *Server) listDirectives(ctx context.Context, args map[string]any) (any, error) {
	bankID, err := s.bankArgument(ctx, args, "list_directives")
	if err != nil {
		return nil, err
	}
	directives, err := s.store.ListDirectives(ctx, bankID)
	if err != nil {
		return nil, err
	}
	items := make([]map[string]any, 0, len(directives))
	for _, directive := range directives {
		items = append(items, map[string]any{"id": directive.ID, "name": directive.Name})
	}
	return map[string]any{"items": items}, nil
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
