// Package reflect implements the bounded agentic reflection loop: the model
// decides which retrieval tool to call next, and the loop answers only from the
// evidence it gathered.
package reflect

import (
	"encoding/json"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Tool names the loop exposes to the model, in the order the prompt presents
// them. The order is the priority the loop expects: a reusable perspective is
// cheaper than raw facts, and raw facts are cheaper than expanding a memory to
// its source document.
const (
	ToolSearchMentalModels = "search_mental_models"
	ToolSearchObservations = "search_observations"
	ToolRecall             = "recall"
	ToolReadMentalModels   = "read_mental_models"
	ToolExpand             = "expand"
	ToolDone               = "done"
)

// ToolNames returns every tool name in presentation order.
func ToolNames() []string {
	return []string{
		ToolSearchMentalModels,
		ToolSearchObservations,
		ToolRecall,
		ToolReadMentalModels,
		ToolExpand,
		ToolDone,
	}
}

// CleanupTools are the tools that retrieve nothing and are therefore not
// counted against the evidence budget.
func CleanupTools() []string {
	return []string{ToolDone}
}

// IsKnownTool reports whether a name is one the loop exposes. A provider may
// prefix or suffix a tool name, so the comparison normalises first.
func IsKnownTool(name string) bool {
	normalized := NormalizeToolName(name)
	for _, known := range ToolNames() {
		if known == normalized {
			return true
		}
	}
	return false
}

// NormalizeToolName strips a provider-added namespace prefix or call suffix so
// an equivalent name resolves to the same tool.
func NormalizeToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	for _, prefix := range toolPrefixes {
		if strings.HasPrefix(lower, prefix) {
			trimmed = trimmed[len(prefix):]
			lower = strings.ToLower(trimmed)
			break
		}
	}
	for _, suffix := range toolSuffixes {
		if strings.HasSuffix(lower, suffix) {
			trimmed = trimmed[:len(trimmed)-len(suffix)]
			break
		}
	}
	return trimmed
}

// toolPrefixes and toolSuffixes are the decorations a provider may add to a
// tool name before the model sees it.
var (
	toolPrefixes = []string{"functions.", "tools.", "tool."}
	toolSuffixes = []string{"_tool", "_call"}
)

// ToolDefinitions is the tool set as the provider boundary expects it.
func ToolDefinitions() []provider.ToolDefinition {
	names := ToolNames()
	defs := make([]provider.ToolDefinition, 0, len(names))
	for _, name := range names {
		defs = append(defs, ToolDefinition(name))
	}
	return defs
}

// ToolDefinition is one tool as the provider boundary expects it.
func ToolDefinition(name string) provider.ToolDefinition {
	return provider.ToolDefinition{
		Name:        name,
		Description: toolDescription(name),
		Parameters:  toolParameters(name),
	}
}

// toolDescription explains one tool to the model.
func toolDescription(name string) string {
	switch name {
	case ToolSearchMentalModels:
		return "Search the bank's reusable perspectives for one that already answers the question. Prefer this over reading raw memories when a perspective covers the question."
	case ToolSearchObservations:
		return "Search consolidated observations, which are conclusions drawn from many memories."
	case ToolRecall:
		return "Search raw stored memories for facts relevant to a question."
	case ToolReadMentalModels:
		return "Read specific perspectives in full, by the identifiers a previous search returned."
	case ToolExpand:
		return "Get more surrounding context for one or more memories."
	case ToolDone:
		return "Finish. Call this once the gathered evidence answers the question."
	default:
		return ""
	}
}

// toolParameters declares one tool's arguments as a JSON-schema object.
//
// The schema is emitted as JSON because that is what the provider boundary
// forwards verbatim; building it as a value and re-encoding it would only add a
// step that could disagree with what the tool actually reads.
func toolParameters(name string) json.RawMessage {
	query := `{"type":"string","description":"Search query string"}`
	reason := `{"type":"string","description":"Brief explanation of why this call is being made"}`
	switch name {
	case ToolSearchMentalModels:
		return json.RawMessage(`{"type":"object","properties":{` +
			`"reason":` + reason + `,` +
			`"query":` + query + `,` +
			`"max_results":{"type":"integer","description":"Maximum perspectives to return (default 5)"}` +
			`},"required":["reason","query"]}`)
	case ToolSearchObservations:
		return json.RawMessage(`{"type":"object","properties":{` +
			`"reason":` + reason + `,` +
			`"query":` + query + `,` +
			`"max_tokens":{"type":"integer","description":"Maximum tokens of results to return"}` +
			`},"required":["reason","query"]}`)
	case ToolRecall:
		return json.RawMessage(`{"type":"object","properties":{` +
			`"reason":` + reason + `,` +
			`"query":` + query + `,` +
			`"max_tokens":{"type":"integer","description":"Maximum tokens of results to return"},` +
			`"max_chunk_tokens":{"type":"integer","description":"Maximum tokens of source chunk text to attach"}` +
			`},"required":["reason","query"]}`)
	case ToolReadMentalModels:
		return json.RawMessage(`{"type":"object","properties":{` +
			`"reason":` + reason + `,` +
			`"mental_model_ids":{"type":"array","items":{"type":"string"},"description":"Identifiers a previous search returned"},` +
			`"ids":{"type":"array","items":{"type":"string"},"description":"Deprecated alias for mental_model_ids"},` +
			`"max_tokens":{"type":"integer","description":"Maximum tokens of model content to return (default 6000)"}` +
			`},"required":["reason"]}`)
	case ToolExpand:
		return json.RawMessage(`{"type":"object","properties":{` +
			`"reason":` + reason + `,` +
			`"memory_ids":{"type":"array","items":{"type":"string"},"description":"Identifiers of the memories to expand (from recall results)"},` +
			`"ids":{"type":"array","items":{"type":"string"},"description":"Deprecated alias for memory_ids"},` +
			`"depth":{"type":"string","enum":["chunk","document"],"description":"Whether to expand to the chunk or the whole document"}` +
			`},"required":["reason","memory_ids","depth"]}`)
	case ToolDone:
		return json.RawMessage(`{"type":"object","properties":{` + `"reason":` + reason + `,` +
			`"answer":{"type":"string","description":"The final synthesized answer as markdown"},` +
			`"document":{"type":"object","description":"The answer as a structured document: an object with a 'sections' array, each section carrying heading, level and an array of markdown-string blocks"}` +
			`},"required":["reason"]}`)
	default:
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
}

// ToolCall is one call the model asked for.
type ToolCall struct {
	ID    string
	Name  string
	Input map[string]any
}

// sortStrings returns a sorted copy, so a rendered tool result is stable.
func sortStrings(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}
