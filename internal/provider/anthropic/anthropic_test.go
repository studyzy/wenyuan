package anthropic

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// transportDouble answers with a scripted body and status, recording the last
// request so a test can assert the wire shape.
type transportDouble struct {
	status int
	body   string
	last   *http.Request
	lastN  []byte
	calls  int
}

func (d *transportDouble) Do(req *http.Request) (*http.Response, error) {
	d.calls++
	d.last = req
	raw, _ := io.ReadAll(req.Body)
	d.lastN = raw
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func testClient(body string) (*Client, *transportDouble) {
	doer := &transportDouble{body: body}
	c := NewClient(provider.Settings{
		Provider: NameAnthropic,
		APIKey:   "test-key",
		BaseURL:  "https://example.test/v1",
		Model:    "claude-test",
		Timeout:  5 * time.Second,
		Retry:    provider.RetryPolicy{MaxRetries: 1, InitialBackoff: time.Millisecond},
	}, doer)
	c.Sleep = func(time.Duration) {}
	return c, doer
}

// The system prompt is lifted into the API's own field, and the remaining turns
// keep their order.
func TestSystemPromptIsLifted(t *testing.T) {
	body := `{"content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":1,"output_tokens":2}}`
	c, doer := testClient(body)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "be terse"},
			{Role: provider.RoleUser, Content: "hello"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var sent messagesRequest
	if err := json.Unmarshal(doer.lastN, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.System != "be terse" {
		t.Errorf("system = %q, want the system message", sent.System)
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Role != "user" {
		t.Errorf("messages = %+v, want only the user turn", sent.Messages)
	}
	if sent.MaxTokens == 0 {
		t.Error("max_tokens is required by this API and must be sent")
	}
}

// The API authenticates with its own header rather than a bearer token, and
// requires the version header.
func TestAuthHeaders(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := doer.last.Header.Get("X-Api-Key"); got != "test-key" {
		t.Errorf("X-Api-Key = %q, want the configured key", got)
	}
	if got := doer.last.Header.Get("Anthropic-Version"); got != AnthropicVersion {
		t.Errorf("Anthropic-Version = %q, want %q", got, AnthropicVersion)
	}
	if got := doer.last.Header.Get("Authorization"); got != "" {
		t.Errorf("Authorization must not be set on this API, got %q", got)
	}
}

// A structured-output call forces a single tool whose input schema is the
// caller's response schema, and the tool's arguments become the parsed value.
func TestStructuredOutputUsesForcedTool(t *testing.T) {
	body := `{"content":[{"type":"tool_use","name":"structured_response","input":{"facts":[{"text":"a"}]}}]}`
	c, doer := testClient(body)
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "extract"}},
		ResponseSchema: &provider.Schema{
			Type:       "object",
			Properties: map[string]*provider.Schema{"facts": {Type: "array"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent messagesRequest
	if err := json.Unmarshal(doer.lastN, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Name != structuredToolName {
		t.Fatalf("tools = %+v, want one forced structured tool", sent.Tools)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "tool" {
		t.Fatalf("tool_choice = %+v, want a forced tool", sent.ToolChoice)
	}
	object, ok := result.Structured.(map[string]any)
	if !ok {
		t.Fatalf("structured = %T, want an object", result.Structured)
	}
	if _, ok := object["facts"]; !ok {
		t.Fatalf("structured = %+v, want the tool arguments", object)
	}
}

// A model that answers a structured request with fenced text still yields a
// parsed value rather than a hard failure.
func TestStructuredOutputFallsBackToFencedText(t *testing.T) {
	body := "{\"content\":[{\"type\":\"text\",\"text\":\"```json\\n{\\\"a\\\":1}\\n```\"}]}"
	c, _ := testClient(body)
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages:       []provider.Message{{Role: provider.RoleUser, Content: "extract"}},
		ResponseSchema: &provider.Schema{Type: "object"},
	})
	if err != nil {
		t.Fatal(err)
	}
	object, ok := result.Structured.(map[string]any)
	if !ok {
		t.Fatalf("structured = %T, want an object parsed from the fenced text", result.Structured)
	}
	if object["a"] != float64(1) {
		t.Fatalf("structured = %+v, want a=1", object)
	}
}

// Text blocks are concatenated and usage is summed into the boundary's shape.
func TestTextAndUsageMapping(t *testing.T) {
	body := `{"content":[{"type":"text","text":"hello "},{"type":"text","text":"world"}],
	          "model":"claude-test","stop_reason":"end_turn",
	          "usage":{"input_tokens":7,"output_tokens":3}}`
	c, _ := testClient(body)
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello world" {
		t.Errorf("content = %q, want the blocks joined", result.Content)
	}
	if result.Usage.InputTokens != 7 || result.Usage.OutputTokens != 3 || result.Usage.TotalTokens != 10 {
		t.Errorf("usage = %+v, want 7/3/10", result.Usage)
	}
	if result.FinishReason != "end_turn" {
		t.Errorf("finish reason = %q", result.FinishReason)
	}
	if result.Model != "claude-test" {
		t.Errorf("model = %q", result.Model)
	}
}

// A missing credential is reported as such rather than retried.
func TestUnauthorizedIsMissingCredential(t *testing.T) {
	c, doer := testClient(`{"error":{"type":"authentication_error","message":"invalid key"}}`)
	doer.status = http.StatusUnauthorized
	_, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if !provider.IsMissingCredential(err) {
		t.Fatalf("err = %v, want a missing-credential error", err)
	}
	if doer.calls != 1 {
		t.Fatalf("calls = %d, want no retry on a credential failure", doer.calls)
	}
}

// This provider serves no embeddings, and says so rather than returning empty
// vectors.
func TestEmbedIsUnavailable(t *testing.T) {
	c, _ := testClient("")
	if _, err := c.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"a"}}); !provider.IsNotAvailable(err) {
		t.Fatalf("err = %v, want a not-available error", err)
	}
}

// The adapter registers under its own name with a credential requirement.
func TestRegister(t *testing.T) {
	reg := provider.NewRegistry()
	Register(reg)
	entry, ok := reg.Lookup(NameAnthropic)
	if !ok {
		t.Fatal("anthropic is not registered")
	}
	if !entry.RequiresCredential {
		t.Error("anthropic must require a credential")
	}
	built, err := entry.Build(provider.Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if built.Name() != NameAnthropic {
		t.Errorf("name = %q", built.Name())
	}
}
