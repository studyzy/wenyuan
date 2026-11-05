package reflect

import (
	"encoding/json"
	"strings"
)

// decodeToolArguments parses a tool call's argument payload into a map. A
// provider that could not parse the arguments itself may keep the raw string
// under a single `_raw` member, or hand back a JSON object cut short by the
// completion cap; both are decoded here rather than read as an empty call.
func decodeToolArguments(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return map[string]any{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err == nil && decoded != nil {
		return decodeRawWrapper(decoded)
	}
	if repaired := repairToolArguments(string(raw)); repaired != nil {
		return decodeRawWrapper(repaired)
	}
	return map[string]any{}
}

// decodeRawWrapper expands a payload a provider kept as {"_raw": "..."} into
// the object it was meant to be, decoding a second time when that string is
// itself JSON and repairing a payload cut off before its closing brackets.
func decodeRawWrapper(payload map[string]any) map[string]any {
	if len(payload) != 1 {
		return payload
	}
	text, ok := payload["_raw"].(string)
	if !ok {
		return payload
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(text), &decoded); err == nil && decoded != nil {
		return decoded
	}
	// A double-encoded payload is a JSON string again after one decode.
	var inner string
	if err := json.Unmarshal([]byte(text), &inner); err == nil && looksLikeJSON(inner) {
		if err := json.Unmarshal([]byte(inner), &decoded); err == nil && decoded != nil {
			return decoded
		}
	}
	if repaired := repairToolArguments(text); repaired != nil {
		return repaired
	}
	return payload
}

// repairToolArguments closes a JSON object cut short by the output cap — an
// unterminated string and its missing closing brackets — so a call whose
// arguments were truncated still yields the fields that were written. It is a
// best effort: a payload truncated in the middle of a value cannot be guessed
// back into shape and stays undecoded.
func repairToolArguments(text string) map[string]any {
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") {
		return nil
	}
	var stack []byte
	inString, escaped := false, false
	for i := 0; i < len(trimmed); i++ {
		c := trimmed[i]
		if inString {
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case '{':
			stack = append(stack, '}')
		case '[':
			stack = append(stack, ']')
		case '}', ']':
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}
	var suffix strings.Builder
	if inString {
		suffix.WriteByte('"')
	}
	body := strings.TrimRight(trimmed, " \t\r\n")
	body = strings.TrimSuffix(body, ",")
	body = strings.TrimSuffix(body, ":")
	for i := len(stack) - 1; i >= 0; i-- {
		suffix.WriteByte(stack[i])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(body+suffix.String()), &decoded); err == nil && decoded != nil {
		return decoded
	}
	return nil
}

// looksLikeJSON reports whether text opens like a JSON object or array.
func looksLikeJSON(text string) bool {
	trimmed := strings.TrimSpace(text)
	return strings.HasPrefix(trimmed, "{") || strings.HasPrefix(trimmed, "[")
}
