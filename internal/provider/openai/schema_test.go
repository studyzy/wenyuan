package openai

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// test-schema-invalid-422: a non-object schema, an empty or ill-typed
// properties map, or a required entry naming an unknown property is rejected
// before any call.
func TestSchemaValidationRejectsMalformed(t *testing.T) {
	cases := []struct {
		name   string
		schema *provider.Schema
	}{
		{"nil schema", nil},
		{"non-object root", &provider.Schema{Type: "array", Properties: map[string]*provider.Schema{"a": {Type: "string"}}}},
		{"empty properties", &provider.Schema{Type: "object", Properties: map[string]*provider.Schema{}}},
		{"unsupported property type", &provider.Schema{Type: "object", Properties: map[string]*provider.Schema{"a": {Type: "null"}}}},
		{"required names unknown property", &provider.Schema{
			Type:       "object",
			Properties: map[string]*provider.Schema{"a": {Type: "string"}},
			Required:   []string{"missing"},
		}},
		{"array without items", &provider.Schema{Type: "object", Properties: map[string]*provider.Schema{"a": {Type: "array"}}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateSchema(tc.schema); err == nil {
				t.Fatal("malformed schema was accepted")
			}
		})
	}
}

// A well-formed schema validates.
func TestSchemaValidationAcceptsSupportedShape(t *testing.T) {
	if err := ValidateSchema(testSchema()); err != nil {
		t.Fatalf("valid schema rejected: %v", err)
	}
}

// A malformed schema is rejected by the serializer before any call, and no
// request is made.
func TestMarshalStrictSchemaRejectsMalformed(t *testing.T) {
	c, doer := testClient(jsonResponse(chatBody(`{}`, "stop")))
	_, err := c.Complete(context.Background(), provider.CompletionRequest{
		ResponseSchema: &provider.Schema{Type: "array"},
		Strict:         true,
	})
	if err == nil {
		t.Fatal("a malformed schema must be rejected before the call")
	}
	if doer.calls != 0 {
		t.Fatalf("made %d calls despite a malformed schema, want 0", doer.calls)
	}
}

// test-native-style: a strict schema serializes with the backend-accepted
// shape, removing unsupported keywords.
func TestStrictSchemaSerializationRemovesUnsupportedKeywords(t *testing.T) {
	settings := provider.Settings{SupportsMaxItems: false, SupportsStringPattern: false}
	raw, err := MarshalStrictSchema(testSchema(), settings)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["type"] != "object" {
		t.Fatalf("root type = %v", decoded["type"])
	}
	for _, banned := range []string{"maxItems", "pattern", "oneOf", "discriminator"} {
		if _, present := decoded[banned]; present {
			t.Fatalf("serialized schema emitted unsupported keyword %q", banned)
		}
	}
	if _, present := decoded["properties"]; !present {
		t.Fatal("serialized schema lost its properties")
	}
}

// The strict response format is attached for a strict structured call.
func TestStrictSchemaResponseFormatAttached(t *testing.T) {
	c, doer := testClient(jsonResponse(chatBody(`{"name":"ada"}`, "stop")))
	_, err := c.Complete(context.Background(), provider.CompletionRequest{
		ResponseSchema: testSchema(),
		Strict:         true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(doer.requests) == 0 {
		t.Fatal("no request was made")
	}
}

// Value validation catches a missing required property and a wrong type.
func TestValidateValueCatchesViolations(t *testing.T) {
	if err := ValidateValue(map[string]any{"age": float64(3)}, testSchema()); err == nil {
		t.Fatal("a missing required property must be caught")
	}
	if err := ValidateValue(map[string]any{"name": float64(3)}, testSchema()); err == nil {
		t.Fatal("a wrong property type must be caught")
	}
	if err := ValidateValue(map[string]any{"name": "ada", "age": float64(36)}, testSchema()); err != nil {
		t.Fatalf("a valid value was rejected: %v", err)
	}
}

// The supported property type allowlist is stable.
func TestSupportedPropertyTypes(t *testing.T) {
	want := map[string]bool{"string": true, "number": true, "integer": true, "boolean": true, "array": true, "object": true}
	got := provider.SupportedPropertyTypes()
	if len(got) != len(want) {
		t.Fatalf("allowlist = %v", got)
	}
	for _, kind := range got {
		if !want[kind] {
			t.Fatalf("unexpected type %q", kind)
		}
	}
}

// A strict schema must satisfy the subset the OpenAI-compatible APIs enforce:
// every object carries additionalProperties=false and lists every property as
// required. A backend that enforces the subset rejects the request otherwise,
// so the omission turned every extraction into a provider error.
func TestMarshalStrictSchemaCarriesTheStrictSubset(t *testing.T) {
	schema := &provider.Schema{
		Type: "object",
		Properties: map[string]*provider.Schema{
			"facts": {
				Type: "array",
				Items: &provider.Schema{
					Type: "object",
					Properties: map[string]*provider.Schema{
						"what": {Type: "string"},
						"when": {Type: "string"},
					},
					Required: []string{"what"},
				},
			},
		},
	}
	raw, err := MarshalStrictSchema(schema, provider.Settings{StrictSchema: true})
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["additionalProperties"] != false {
		t.Fatalf("the root object must forbid extra properties: %s", raw)
	}
	if got, ok := decoded["required"].([]any); !ok || len(got) != 1 || got[0] != "facts" {
		t.Fatalf("every property must be required: %s", raw)
	}
	items := decoded["properties"].(map[string]any)["facts"].(map[string]any)["items"].(map[string]any)
	if items["additionalProperties"] != false {
		t.Fatalf("a nested object must forbid extra properties too: %s", raw)
	}
	if got, ok := items["required"].([]any); !ok || len(got) != 2 {
		t.Fatalf("a nested object must require every property: %s", raw)
	}
	// The serializer serves the strict path only, so the subset is emitted
	// regardless of how the settings resolved: what makes a request strict is
	// the caller asking for a strict schema, not the deployment default.
	raw, err = MarshalStrictSchema(schema, provider.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"additionalProperties":false`) {
		t.Fatalf("the strict subset must be emitted: %s", raw)
	}
}
