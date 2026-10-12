package storage

import "strings"

// Retrieval tool-name normalization. A model may emit a tool name carrying a
// common provider prefix or suffix; the loop strips those before dispatch so an
// equivalent name resolves to the same tool. An unrecognized name is recovered
// from by falling back to no-ops rather than aborting the reflection.

// reflectToolPrefixes are the provider prefixes stripped before dispatch.
var reflectToolPrefixes = []string{"tool_", "tools/", "functions.", "function_", "call_"}

// reflectToolSuffixes are the provider suffixes stripped before dispatch.
var reflectToolSuffixes = []string{"_tool", "_function", "_call"}

// normalizeToolName strips a common provider prefix or suffix from a tool name
// so an equivalent name resolves to the same tool.
func normalizeToolName(name string) string {
	trimmed := strings.TrimSpace(name)
	lower := strings.ToLower(trimmed)
	for _, prefix := range reflectToolPrefixes {
		if strings.HasPrefix(lower, prefix) {
			trimmed = trimmed[len(prefix):]
			lower = strings.ToLower(trimmed)
			break
		}
	}
	for _, suffix := range reflectToolSuffixes {
		if strings.HasSuffix(lower, suffix) {
			trimmed = trimmed[:len(trimmed)-len(suffix)]
			break
		}
	}
	return trimmed
}

// isKnownTool reports whether a normalized name matches a known retrieval tool.
func isKnownTool(name string) bool {
	return containsString(reflectAllToolNames, normalizeToolName(name))
}

// uniqueCallID makes a duplicate tool-call identifier unique within one turn by
// appending a disambiguating ordinal, so each call keeps its own result.
func uniqueCallID(id string, seen map[string]int) string {
	seen[id]++
	if seen[id] == 1 {
		return id
	}
	return id + "#" + itoaSmall(seen[id])
}

// itoaSmall renders a small non-negative integer without importing strconv.
func itoaSmall(value int) string {
	if value < 10 {
		return string(rune('0' + value))
	}
	digits := []byte{}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

// applyLengthRewrite shortens an over-length answer to the visible-length
// budget. The budget is a soft directive: a zero budget leaves the answer
// whole, and a rewrite that yields nothing leaves the original answer standing.
func applyLengthRewrite(answer string, maxTokens int) string {
	if maxTokens <= 0 {
		return answer
	}
	if reflectTokenCount(answer) <= maxTokens {
		return answer
	}
	rewritten := shorten(answer, maxTokens)
	if strings.TrimSpace(rewritten) == "" {
		return answer
	}
	return rewritten
}
