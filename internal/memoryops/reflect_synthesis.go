package memoryops

import (
	"encoding/json"
	"errors"
	"strings"
)

// parseStructuredOutput extracts a structured-output object from the answer,
// validated against the schema. Extraction succeeds when the answer carries a
// JSON object satisfying the schema's required members; when it carries no
// JSON object at all, the result is an empty (nil) output with no error,
// distinguishing a nothing-found outcome from a failed extraction.
func parseStructuredOutput(schema map[string]any, answer string) (map[string]any, error) {
	document, ok := extractJSONObject(answer)
	if !ok {
		return nil, nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(document), &parsed); err != nil {
		return nil, errors.New("structured output was not valid JSON")
	}
	if err := validateAgainstSchema(schema, parsed); err != nil {
		return nil, err
	}
	return parsed, nil
}

// extractJSONObject returns the first balanced JSON object in a text, and
// whether one was found.
func extractJSONObject(text string) (string, bool) {
	start := strings.IndexByte(text, '{')
	if start < 0 {
		return "", false
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
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
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1], true
			}
		}
	}
	return "", false
}

// validateAgainstSchema checks a parsed document against the schema's declared
// required members and their primitive types.
func validateAgainstSchema(schema, document map[string]any) error {
	required, _ := schema["required"].([]any)
	for _, name := range required {
		key, ok := name.(string)
		if !ok {
			continue
		}
		if _, present := document[key]; !present {
			return errors.New("structured output is missing required field " + key)
		}
	}
	properties, _ := schema["properties"].(map[string]any)
	for key, spec := range properties {
		value, present := document[key]
		if !present {
			continue
		}
		typeName := schemaTypeName(spec)
		if typeName != "" && !matchesJSONType(value, typeName) {
			return errors.New("structured output field " + key + " must be a " + typeName)
		}
	}
	return nil
}

// schemaTypeName extracts the declared type name from a property spec.
func schemaTypeName(spec any) string {
	object, ok := spec.(map[string]any)
	if !ok {
		return ""
	}
	name, _ := object["type"].(string)
	return name
}

// matchesJSONType reports whether a decoded JSON value matches a schema type.
func matchesJSONType(value any, typeName string) bool {
	switch typeName {
	case "string":
		_, ok := value.(string)
		return ok
	case "number", "integer":
		_, ok := value.(float64)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	case "array":
		_, ok := value.([]any)
		return ok
	default:
		return true
	}
}

// defaultReflectSchemaValidator accepts any non-null object schema.
func defaultReflectSchemaValidator(schema map[string]any) error {
	if schema == nil {
		return errors.New("schema must be a JSON object")
	}
	return nil
}
