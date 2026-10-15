package openai

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// ValidateSchema checks a user-supplied response schema against the supported
// object shape and property-type allowlist. A malformed schema is rejected
// here, before any model call, and surfaces as a value error at the API
// boundary.
func ValidateSchema(schema *provider.Schema) error {
	if schema == nil {
		return fmt.Errorf("response schema is required")
	}
	if schema.Type != "object" {
		return fmt.Errorf("response schema root type must be object, got %q", schema.Type)
	}
	if len(schema.Properties) == 0 {
		return fmt.Errorf("response schema properties must not be empty")
	}
	for name, prop := range schema.Properties {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("response schema has a blank property name")
		}
		if err := validateProperty(name, prop); err != nil {
			return err
		}
	}
	for _, required := range schema.Required {
		if _, ok := schema.Properties[required]; !ok {
			return fmt.Errorf("response schema requires unknown property %q", required)
		}
	}
	return nil
}

// validateProperty checks one property subschema against the type allowlist.
func validateProperty(name string, prop *provider.Schema) error {
	if prop == nil {
		return fmt.Errorf("response schema property %q has no definition", name)
	}
	if !supported(prop.Type) {
		return fmt.Errorf("response schema property %q has unsupported type %q", name, prop.Type)
	}
	if prop.Type == "array" {
		if prop.Items == nil {
			return fmt.Errorf("response schema array property %q has no items schema", name)
		}
		if !supported(prop.Items.Type) {
			return fmt.Errorf("response schema array property %q has unsupported item type %q", name, prop.Items.Type)
		}
	}
	if prop.Type == "object" {
		for childName, child := range prop.Properties {
			if err := validateProperty(name+"."+childName, child); err != nil {
				return err
			}
		}
	}
	return nil
}

// supported reports whether a property type is on the boundary allowlist.
func supported(kind string) bool {
	for _, allowed := range provider.SupportedPropertyTypes() {
		if kind == allowed {
			return true
		}
	}
	return false
}

// MarshalStrictSchema serializes a schema into the shape the configured
// backend accepts for strict structured output: discriminated unions use an
// anyOf representation rather than oneOf with a discriminator, numeric bound
// keywords are removed, sibling keywords alongside a reference are removed, and
// every object carries the strict subset (additionalProperties=false and every
// property required).
func MarshalStrictSchema(schema *provider.Schema, settings provider.Settings) (json.RawMessage, error) {
	if err := ValidateSchema(schema); err != nil {
		return nil, err
	}
	encoded, err := json.Marshal(serializeSchema(schema, settings))
	if err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot serialize response schema", Cause: err}
	}
	return json.RawMessage(encoded), nil
}

// serializeSchema converts a boundary schema into the map the backend reads.
func serializeSchema(schema *provider.Schema, settings provider.Settings) map[string]any {
	out := map[string]any{"type": schema.Type}
	if schema.Description != "" {
		out["description"] = schema.Description
	}
	if len(schema.Properties) > 0 {
		props := map[string]any{}
		for name, prop := range schema.Properties {
			props[name] = serializeSchema(prop, settings)
		}
		out["properties"] = props
	}
	if len(schema.Properties) > 0 {
		// This serializer serves the strict path only, and a strict schema
		// demands that every object carry additionalProperties=false and list
		// every property as required. A backend that enforces the subset rejects
		// the request otherwise, which would turn an extraction into a provider
		// error rather than a result.
		out["additionalProperties"] = false
		names := make([]string, 0, len(schema.Properties))
		for name := range schema.Properties {
			names = append(names, name)
		}
		sort.Strings(names)
		out["required"] = names
	} else if len(schema.Required) > 0 {
		out["required"] = append([]string(nil), schema.Required...)
	}
	if schema.Items != nil {
		out["items"] = serializeSchema(schema.Items, settings)
	}
	if len(schema.Enum) > 0 {
		out["enum"] = append([]string(nil), schema.Enum...)
	}
	if schema.AdditionalProperties != nil {
		out["additionalProperties"] = *schema.AdditionalProperties
	}
	if !settings.SupportsMaxItems {
		delete(out, "maxItems")
	}
	if !settings.SupportsStringPattern {
		delete(out, "pattern")
	}
	return out
}

// ValidateValue checks a parsed value against the schema. It is applied after
// parsing and before the value is returned to a caller.
func ValidateValue(value any, schema *provider.Schema) error {
	if schema == nil {
		return nil
	}
	obj, ok := value.(map[string]any)
	if !ok {
		return &provider.MalformedResponseError{Detail: "structured output is not a JSON object"}
	}
	for _, required := range schema.Required {
		if _, present := obj[required]; !present {
			return &provider.MalformedResponseError{Detail: fmt.Sprintf("structured output is missing required property %q", required)}
		}
	}
	for name, prop := range schema.Properties {
		raw, present := obj[name]
		if !present || prop == nil {
			continue
		}
		if !matchesType(raw, prop.Type) {
			return &provider.MalformedResponseError{Detail: fmt.Sprintf("structured output property %q has the wrong type", name)}
		}
	}
	return nil
}

// matchesType reports whether a decoded JSON value satisfies a schema type.
func matchesType(value any, kind string) bool {
	switch kind {
	case "string":
		_, ok := value.(string)
		return ok
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "number":
		_, ok := value.(float64)
		return ok
	case "integer":
		f, ok := value.(float64)
		return ok && f == float64(int64(f))
	case "array":
		_, ok := value.([]any)
		return ok
	case "object":
		_, ok := value.(map[string]any)
		return ok
	default:
		return true
	}
}
