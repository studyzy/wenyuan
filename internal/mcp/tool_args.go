package mcp

import (
	"context"
	"encoding/json"
	"strings"
)

// normalizeToolArguments prepares the arguments of one tools/call the way the
// reference's transport does before a tool sees them (api/mcp.py `_make_tools_tolerant`).
//
// Two habits of LLM clients are repaired here rather than rejected: a call that
// carries an argument the tool does not declare (`explanation`, `reasoning`) has
// those keys stripped, and a list or object argument that arrived as a JSON
// string (`tags: "[\"a\"]"`) is parsed back into the value its schema declares.
// Everything that is not one of those two cases is left exactly as sent, so the
// tool's own validation still refuses a value that really is wrong.
func normalizeToolArguments(schema map[string]any, arguments map[string]any) map[string]any {
	if arguments == nil {
		return nil
	}
	properties, _ := schema["properties"].(map[string]any)
	allowed := arguments
	if properties != nil {
		stripped := make(map[string]any, len(arguments))
		for name, value := range arguments {
			if _, declared := properties[name]; declared {
				stripped[name] = value
			}
		}
		allowed = stripped
	}
	if len(allowed) == 0 {
		return allowed
	}
	arrays, objects := coercibleArguments(properties)
	var coerced map[string]any
	replace := func(name string, value any) {
		if coerced == nil {
			coerced = copyArguments(allowed)
		}
		coerced[name] = value
	}
	for name := range arrays {
		if value, replaced := coerceJSONString(allowed[name], func(parsed any) bool {
			_, isList := parsed.([]any)
			return isList
		}); replaced {
			replace(name, value)
		}
	}
	for name := range objects {
		current := allowed[name]
		if coerced != nil {
			current = coerced[name]
		}
		if value, replaced := coerceJSONString(current, func(parsed any) bool {
			_, isObject := parsed.(map[string]any)
			return isObject
		}); replaced {
			replace(name, value)
		}
	}
	if coerced == nil {
		return allowed
	}
	return coerced
}

// coercibleArguments lists the members a schema declares as an array or an
// object, reading straight and `anyOf`/`oneOf` forms alike — how a nullable
// list (`list[str] | None`) reaches the schema.
func coercibleArguments(properties map[string]any) (arrays, objects map[string]bool) {
	arrays = map[string]bool{}
	objects = map[string]bool{}
	for name, raw := range properties {
		property, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		switch property["type"] {
		case "array":
			arrays[name] = true
			continue
		case "object":
			objects[name] = true
			continue
		}
		for _, union := range []string{"anyOf", "oneOf"} {
			branches, ok := property[union].([]any)
			if !ok {
				continue
			}
			for _, branch := range branches {
				entry, ok := branch.(map[string]any)
				if !ok {
					continue
				}
				if entry["type"] == "array" {
					arrays[name] = true
				}
				if entry["type"] == "object" {
					objects[name] = true
				}
			}
		}
	}
	return arrays, objects
}

// coerceJSONString parses one argument that arrived as a JSON string, when it
// decodes to a value of the kind the schema declares. Anything else — a native
// value, a string that is not JSON, JSON of the wrong kind — is left untouched.
func coerceJSONString(raw any, wanted func(any) bool) (any, bool) {
	text, isString := raw.(string)
	if !isString {
		return raw, false
	}
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || (trimmed[0] != '[' && trimmed[0] != '{') {
		return raw, false
	}
	var parsed any
	decoder := json.NewDecoder(strings.NewReader(trimmed))
	// Arguments are numbers the tools read as integers or floats, which is what
	// the transport's decoder produced for the rest of the payload.
	decoder.UseNumber()
	if err := decoder.Decode(&parsed); err != nil {
		return raw, false
	}
	if !wanted(parsed) {
		return raw, false
	}
	return normalJSONNumbers(parsed), true
}

// normalJSONNumbers renders the decoder's json.Number values the way the rest of
// a decoded payload carries them, so a coerced argument is indistinguishable
// from one that arrived natively.
func normalJSONNumbers(value any) any {
	switch typed := value.(type) {
	case json.Number:
		if integer, err := typed.Int64(); err == nil {
			return float64(integer)
		}
		if number, err := typed.Float64(); err == nil {
			return number
		}
		return typed.String()
	case []any:
		for index, entry := range typed {
			typed[index] = normalJSONNumbers(entry)
		}
	case map[string]any:
		for key, entry := range typed {
			typed[key] = normalJSONNumbers(entry)
		}
	}
	return value
}

// copyArguments duplicates an argument map so a coercion never mutates the
// caller's own object.
func copyArguments(arguments map[string]any) map[string]any {
	out := make(map[string]any, len(arguments))
	for name, value := range arguments {
		out[name] = value
	}
	return out
}

// toolArgumentSchema returns the input schema of one advertised tool, which is
// what the transport reads to decide which arguments to keep and which to
// coerce.
func (s *Server) toolArgumentSchema(ctx context.Context, name string) map[string]any {
	for _, def := range s.advertisedTools(mountBank(ctx) == "") {
		if def.Name == name {
			return def.InputSchema
		}
	}
	return nil
}
