package openai

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

func testSchema() *provider.Schema {
	strict := false
	return &provider.Schema{
		Type: "object",
		Properties: map[string]*provider.Schema{
			"name": {Type: "string"},
			"age":  {Type: "integer"},
		},
		Required:             []string{"name"},
		AdditionalProperties: &strict,
	}
}

// test-structured-valid: valid structured output parses and validates.
func TestStructuredValidOutput(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody(`{"name":"ada","age":36}`, "stop")))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{ResponseSchema: testSchema()})
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := result.Structured.(map[string]any)
	if !ok {
		t.Fatalf("structured value is %T", result.Structured)
	}
	if obj["name"] != "ada" {
		t.Fatalf("name = %v", obj["name"])
	}
}

// test-structured-fenced-and-controls: fenced output and raw control
// characters parse without losing content.
func TestStructuredFencedAndControlCharacters(t *testing.T) {
	fenced := chatBody("```json\n{\"name\":\"ada\"}\n```", "stop")
	c, _ := testClient(jsonResponse(fenced))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{ResponseSchema: testSchema()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Structured.(map[string]any)["name"] != "ada" {
		t.Fatalf("fenced content lost: %v", result.Structured)
	}

	raw := "{\"name\":\"line one\nline two\"}"
	c2, _ := testClient(jsonResponse(chatBody(raw, "stop")))
	result2, err := c2.Complete(context.Background(), provider.CompletionRequest{
		ResponseSchema: testSchema(),
		SkipValidation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := result2.Structured.(map[string]any)["name"]; got != "line one\nline two" {
		t.Fatalf("control character content = %q", got)
	}
}

// test-parse-failure-not-empty: a parse failure raises a typed error rather
// than returning an empty value.
func TestParseFailureIsNotAnEmptyValue(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody("not json at all", "stop")))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{ResponseSchema: testSchema()})
	if err == nil {
		t.Fatal("a parse failure must raise an error")
	}
	if !provider.IsMalformedResponse(err) {
		t.Fatalf("error is %T, want malformed-response", err)
	}
	if result.Structured != nil {
		t.Fatalf("structured value = %v, want nil on failure", result.Structured)
	}
}

// Usage is captured before validation so billed tokens are recorded even when
// validation fails.
func TestUsageCapturedBeforeValidationFailure(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody(`{"name":123}`, "stop")))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{ResponseSchema: testSchema()})
	if err == nil {
		t.Fatal("a type mismatch must fail validation")
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 4 || result.Usage.TotalTokens != 14 {
		t.Fatalf("usage was lost on failure: %+v", result.Usage)
	}
}

// test-structured-retry-then-repair: content that needs repair but reported
// completion is repaired and parsed.
func TestStructuredRepairForCompletedGeneration(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody("Here is the JSON:\n{\"name\":\"ada\"}\nHope that helps", "stop")))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{ResponseSchema: testSchema()})
	if err != nil {
		t.Fatal(err)
	}
	if result.Structured.(map[string]any)["name"] != "ada" {
		t.Fatalf("prose around the JSON was not tolerated: %v", result.Structured)
	}
}

// Structured output is not repaired for a generation that did not report
// completion: the output-limit error wins.
func TestStructuredNotRepairedWhenIncomplete(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody("{\"name\":\"ada", "length")))
	_, err := c.Complete(context.Background(), provider.CompletionRequest{ResponseSchema: testSchema()})
	if !provider.IsOutputTooLong(err) {
		t.Fatalf("error is %T, want output-too-long", err)
	}
}

// SkipValidation returns the raw parsed value without a schema check.
func TestSkipValidationReturnsRawValue(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody(`{"unknown":true}`, "stop")))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		ResponseSchema: testSchema(),
		SkipValidation: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Structured.(map[string]any)["unknown"] != true {
		t.Fatalf("raw value = %v", result.Structured)
	}
}
