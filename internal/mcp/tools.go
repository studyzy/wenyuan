package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/bankapi"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// serverVersion is the reference release reported in serverInfo.
const serverVersion = "1.0.0"

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

// withInstructions appends the deployment's extra instructions to the retain
// and recall descriptions, which is where the reference puts them: the two
// tools whose behaviour the operator is steering.
func withInstructions(defs []tool, instructions string) []tool {
	if instructions == "" {
		return defs
	}
	suffix := "\n\nAdditional instructions: " + instructions
	out := make([]tool, 0, len(defs))
	for _, def := range defs {
		if def.Name == "retain" || def.Name == "recall" {
			def.Description += suffix
		}
		out = append(out, def)
	}
	return out
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

// retain queues one memory write and reports the receipt the caller polls. The
// reference's retain is asynchronous — it submits a batch operation and returns
// its identifier — so the tool does not wait for extraction.
func (s *Server) retain(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	content := stringArgument(args, "content")
	if content == "" {
		return nil, errors.New("retain requires content")
	}
	item := retainItem(args, content)
	if item.Context == "" {
		item.Context = "general"
	}
	submission, err := s.store.SubmitAsyncRetain(ctx, storage.RetainRequest{
		BankID: bankID,
		Items:  []storage.RetainItem{item},
		Async:  true,
	}, storage.DefaultRetainSubBatchTokens)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"status":       "accepted",
		"message":      "Memory storage initiated",
		"operation_id": submission.OperationID,
	}, nil
}

// syncRetain persists one memory write and reports the stored unit identifiers,
// waiting for the write to land rather than queueing it.
func (s *Server) syncRetain(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
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
	// One entry per stored unit, flattened across the submitted items, which is
	// the identifier list the reference's synchronous retain reports.
	memoryIDs := make([]string, 0)
	for _, batch := range outcome.ItemUnitIDs {
		memoryIDs = append(memoryIDs, batch...)
	}
	return map[string]any{
		"status":     "completed",
		"message":    "Memory stored successfully",
		"memory_ids": memoryIDs,
	}, nil
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

// recall searches a bank's memories and returns the recall response envelope,
// the same body the HTTP route serves. The compound tag filter, query anchor,
// score floors and temporal window the schema declares are passed through to
// retrieval rather than accepted and ignored.
func (s *Server) recall(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	request, err := recallRequest(args)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(request.Query) == "" {
		return nil, errors.New("recall requires query")
	}
	query, err := buildRecallQuery(bankID, request)
	if err != nil {
		return nil, err
	}
	result, err := s.store.Recall(ctx, query)
	if err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(memoryops.BuildRecallResponse(result, request))
	if err != nil {
		return nil, err
	}
	return toolText(encoded), nil
}

// reflect answers a question from a bank's memories. The payload is the
// reflection response with the trace members removed, which is what the
// reference returns unless the caller opts into them.
func (s *Server) reflect(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	query := stringArgument(args, "query")
	if query == "" {
		return nil, errors.New("reflect requires query")
	}
	outcome, err := s.store.Reflect(ctx, storage.ReflectQuery{BankID: bankID, Query: query})
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"text":                    outcome.Answer,
		"structured_output":       structuredOutput(outcome.StructuredOutput),
		"structured_output_error": structuredOutputError(outcome.StructuredOutputError),
		"usage": map[string]any{
			"input_tokens":    outcome.Usage.InputTokens,
			"output_tokens":   outcome.Usage.OutputTokens,
			"total_tokens":    outcome.Usage.TotalTokens,
			"cached_tokens":   outcome.Usage.CachedTokens,
			"thoughts_tokens": outcome.Usage.ThoughtsTokens,
		},
	}
	encoded, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return toolText(encoded), nil
}

// structuredOutput renders the reshaped answer, reporting null when the request
// asked for no reshape or the model produced none.
func structuredOutput(value map[string]any) any {
	if value == nil {
		return nil
	}
	return value
}

// structuredOutputError renders why an asked-for reshape produced nothing, or
// null when no error was recorded.
func structuredOutputError(message string) any {
	if message == "" {
		return nil
	}
	return message
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
	bankID := s.bankArgument(ctx, args)
	bank, err := s.store.GetBank(ctx, bankID)
	if err != nil {
		return nil, err
	}
	return bankProfile(bank), nil
}

// updateBank applies a partial bank update.
func (s *Server) updateBank(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
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
	bankID := s.bankArgument(ctx, args)
	if _, err := s.store.DeleteBank(ctx, bankID); err != nil {
		return nil, err
	}
	return map[string]any{"success": true}, nil
}

// clearMemories removes a bank's memory units.
func (s *Server) clearMemories(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
	removed, err := s.store.ClearBankMemories(ctx, bankID, stringArgument(args, "type"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted_count": removed}, nil
}

// listMemories lists a bank's memory units.
func (s *Server) listMemories(ctx context.Context, args map[string]any) (any, error) {
	bankID := s.bankArgument(ctx, args)
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
	bankID := s.bankArgument(ctx, args)
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
	bankID := s.bankArgument(ctx, args)
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
	bankID := s.bankArgument(ctx, args)
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
	bankID := s.bankArgument(ctx, args)
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
	bankID := s.bankArgument(ctx, args)
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

// toolText marks a payload that is already the JSON text a str-returning tool
// produces, so the envelope wraps it rather than re-encoding it.
type toolText []byte

// objectToolResult wraps an object payload the way the reference returns a tool
// whose Python signature returns a dict: the payload's compact JSON as the text
// content, mirrored as structured content.
func objectToolResult(payload any) map[string]any {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return toolError(err.Error())
	}
	return map[string]any{
		"content":           []map[string]any{{"type": "text", "text": string(encoded)}},
		"structuredContent": payload,
		"isError":           false,
	}
}

// textToolResult wraps a text payload the way the reference returns a tool whose
// Python signature returns a string: the SDK wraps it in a `result` object, so
// the structured content is {"result": <text>} and the result carries the
// wrap-result marker the client reads.
func textToolResult(text string) map[string]any {
	return map[string]any{
		"_meta":             map[string]any{"fastmcp": map[string]any{"wrap_result": true}},
		"content":           []map[string]any{{"type": "text", "text": text}},
		"structuredContent": map[string]any{"result": text},
		"isError":           false,
	}
}

// readOnlyMCPTools are the tools the reference annotates as safe reads, so a
// client can auto-approve them. destructiveMCPTools are the ones that delete or
// clear memory; every other tool writes without destroying.
var (
	readOnlyMCPTools = map[string]bool{
		"recall":                  true,
		"reflect":                 true,
		"list_banks":              true,
		"get_bank":                true,
		"get_bank_stats":          true,
		"list_mental_models":      true,
		"get_mental_model":        true,
		"list_directives":         true,
		"list_memories":           true,
		"get_memory":              true,
		"list_documents":          true,
		"get_document":            true,
		"list_operations":         true,
		"get_operation":           true,
		"list_tags":               true,
		"get_knowledge_base_tree": true,
		"search_knowledge_base":   true,
		"get_knowledge_page":      true,
	}
	destructiveMCPTools = map[string]bool{
		"delete_bank":           true,
		"clear_memories":        true,
		"clear_mental_model":    true,
		"delete_mental_model":   true,
		"delete_directive":      true,
		"delete_document":       true,
		"invalidate_memory":     true,
		"delete_knowledge_node": true,
	}
)

// toolAnnotations is the hint set the reference publishes for a tool: a read
// hint on the safe reads, a destructive hint on the deleting tools, and both
// hints set false on every other write. No tool touches the open world.
func toolAnnotations(name string) map[string]any {
	switch {
	case readOnlyMCPTools[name]:
		return map[string]any{"readOnlyHint": true, "openWorldHint": false}
	case destructiveMCPTools[name]:
		return map[string]any{"readOnlyHint": false, "destructiveHint": true, "openWorldHint": false}
	default:
		return map[string]any{"readOnlyHint": false, "destructiveHint": false, "openWorldHint": false}
	}
}

// toolOutputSchema is the structured-output schema the reference publishes. The
// two object-returning tools advertise an open object; the text-returning tools
// advertise the SDK's wrap-result envelope, except recall, whose result may be a
// string or an object.
func toolOutputSchema(name string) map[string]any {
	switch name {
	case "retain", "sync_retain":
		return map[string]any{"additionalProperties": true, "type": "object"}
	case "recall":
		return map[string]any{
			"properties": map[string]any{
				"result": map[string]any{"anyOf": []any{
					map[string]any{"type": "string"},
					map[string]any{"additionalProperties": true, "type": "object"},
				}},
			},
			"required":              []any{"result"},
			"type":                  "object",
			"x-fastmcp-wrap-result": true,
		}
	default:
		return map[string]any{
			"properties":            map[string]any{"result": map[string]any{"type": "string"}},
			"required":              []any{"result"},
			"type":                  "object",
			"x-fastmcp-wrap-result": true,
		}
	}
}

// toolEntries renders the roster the tools/list response carries: each
// definition plus the annotations, structured-output schema and metadata the
// reference publishes for it.
func toolEntries(defs []tool) []map[string]any {
	entries := make([]map[string]any, 0, len(defs))
	for _, def := range defs {
		entries = append(entries, map[string]any{
			"name":         def.Name,
			"description":  def.Description,
			"inputSchema":  def.InputSchema,
			"outputSchema": toolOutputSchema(def.Name),
			"annotations":  toolAnnotations(def.Name),
			"_meta":        map[string]any{"fastmcp": map[string]any{"tags": []any{}}},
		})
	}
	return entries
}

// toolResult wraps a tool payload in the envelope its return kind calls for. The
// two memory-write tools return objects; every other tool returns text.
func toolResult(name string, payload any) map[string]any {
	if name == "retain" || name == "sync_retain" {
		return objectToolResult(payload)
	}
	if text, ok := payload.(toolText); ok {
		return textToolResult(string(text))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return toolError(err.Error())
	}
	return textToolResult(string(encoded))
}

// toolError reports a tool failure as a result carrying the message. The
// reference returns an error payload with no structured content and no
// wrap-result marker, so neither is set here.
func toolError(message string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": message}},
		"isError": true,
	}
}
