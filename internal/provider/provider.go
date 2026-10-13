package provider

import (
	"context"
	"encoding/json"
)

// Provider is the model-provider boundary. A concrete provider is either the
// no-model selection or a vendor adapter; callers never reach a vendor type
// directly.
type Provider interface {
	// Name is the registered provider name.
	Name() string
	// Complete runs one text completion, free-form or structured.
	Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error)
	// Embed generates one vector per input text, in input order.
	Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResult, error)
	// VerifyConnection issues a small probe and reports whether the provider
	// is reachable. The no-model selection succeeds as a no-op.
	VerifyConnection(ctx context.Context) error
	// Dimensions is the width this provider guarantees for every vector.
	Dimensions() int
}

// Schema is a caller-supplied structured-output schema. The boundary validates
// it before any call and the adapter serializes it into the shape the
// configured backend accepts.
type Schema struct {
	// Type is the schema's root type; only object is supported.
	Type string
	// Properties maps each property name to its subschema.
	Properties map[string]*Schema
	// Required lists properties the model must supply.
	Required []string
	// Items is the element schema for an array property.
	Items *Schema
	// Enum restricts a string property to a fixed set of values.
	Enum []string
	// Description documents the property for the model.
	Description string
	// AdditionalProperties controls whether unknown properties are allowed.
	AdditionalProperties *bool
}

// UnmarshalJSON accepts the JSON-schema form a caller supplies. Only the
// subset the boundary supports is read; anything else is ignored so an
// unsupported keyword is rejected by validation rather than silently honoured.
func (s *Schema) UnmarshalJSON(data []byte) error {
	type raw struct {
		Type                 string             `json:"type"`
		Properties           map[string]*Schema `json:"properties"`
		Required             []string           `json:"required"`
		Items                *Schema            `json:"items"`
		Enum                 []string           `json:"enum"`
		Description          string             `json:"description"`
		AdditionalProperties *bool              `json:"additionalProperties"`
	}
	var r raw
	if err := json.Unmarshal(data, &r); err != nil {
		return err
	}
	s.Type = r.Type
	s.Properties = r.Properties
	s.Required = r.Required
	s.Items = r.Items
	s.Enum = r.Enum
	s.Description = r.Description
	s.AdditionalProperties = r.AdditionalProperties
	return nil
}

// SupportedPropertyTypes returns the allowlist of property types the boundary
// validates. A property outside it is rejected before any model call, so
// adapter tests and error messages read the same list.
func SupportedPropertyTypes() []string {
	return []string{"string", "number", "integer", "boolean", "array", "object"}
}
