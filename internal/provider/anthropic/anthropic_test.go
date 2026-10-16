package anthropic

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/cacheaffinity"
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

// The system prompt is lifted into the API's own field as a block carrying the
// cache breakpoint, and the remaining turns keep their order.
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
	if len(sent.System) != 1 || sent.System[0].Text != "be terse" {
		t.Errorf("system = %+v, want the system message as one block", sent.System)
	}
	if sent.System[0].CacheControl == nil || sent.System[0].CacheControl.Type != "ephemeral" {
		t.Errorf("system block = %+v, want the ephemeral cache breakpoint", sent.System[0])
	}
	if len(sent.Messages) != 1 || sent.Messages[0].Role != "user" {
		t.Errorf("messages = %+v, want only the user turn", sent.Messages)
	}
	if sent.MaxTokens == 0 {
		t.Error("max_tokens is required by this API and must be sent")
	}
}

// The one-shot body is byte-for-byte the shape this API receives: the system
// prompt is a marked block list, and no other breakpoint is sent.
func TestOneShotBodyCarriesSystemCacheMarker(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "be terse"},
			{Role: provider.RoleUser, Content: "hello"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"claude-test","messages":[{"role":"user","content":"hello"}],` +
		`"system":[{"type":"text","text":"be terse","cache_control":{"type":"ephemeral"}}],"max_tokens":4096}`
	if got := string(doer.lastN); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
}

// Several system turns fold into the one block the API accepts, and the marker
// still rides it.
func TestSystemTurnsFoldIntoOneMarkedBlock(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "first"},
			{Role: provider.RoleSystem, Content: "second"},
			{Role: provider.RoleUser, Content: "hello"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	var sent messagesRequest
	if err := json.Unmarshal(doer.lastN, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.System) != 1 || sent.System[0].Text != "first\n\nsecond" {
		t.Fatalf("system = %+v, want the turns folded with a blank line", sent.System)
	}
	if sent.System[0].CacheControl == nil {
		t.Fatal("the folded system block must carry the cache breakpoint")
	}
}

// A request with no system prompt sends no system field at all.
func TestAbsentSystemPromptSendsNoField(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doer.lastN), `"system"`) {
		t.Fatalf("body = %s, want no system field", doer.lastN)
	}
}

// The tool loop's body marks both breakpoints: the stable system prefix and the
// conversation's last turn, which the next iteration reads back.
func TestToolLoopBodyMarksSystemAndLastTurn(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "be terse"},
			{Role: provider.RoleUser, Content: "who?"},
		},
		Tools:      []provider.ToolDefinition{{Name: "keyword_search", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &provider.ToolSelectionPolicy{Mode: "auto"},
	}); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"claude-test","messages":[{"role":"user","content":[{"type":"text","text":"who?",` +
		`"cache_control":{"type":"ephemeral"}}]}],` +
		`"system":[{"type":"text","text":"be terse","cache_control":{"type":"ephemeral"}}],"max_tokens":4096,` +
		`"tools":[{"name":"keyword_search","input_schema":{"type":"object"}}],"tool_choice":{"type":"auto"}}`
	if got := string(doer.lastN); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
}

// The structured-output path shares the tool wire shape but is a one-shot call,
// so only the system prefix is marked — matching the reference, whose
// end-marker belongs to the multi-turn loop.
func TestStructuredPathMarksOnlySystem(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"tool_use","name":"structured_response","input":{"a":1}}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "be terse"},
			{Role: provider.RoleUser, Content: "extract"},
		},
		ResponseSchema: &provider.Schema{Type: "object"},
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(doer.lastN), `"content":[{"type":"text"`) {
		t.Fatalf("body = %s, want the one-shot turn left unmarked", doer.lastN)
	}
	if !strings.Contains(string(doer.lastN), `"system":[{"type":"text","text":"be terse","cache_control":{"type":"ephemeral"}}]`) {
		t.Fatalf("body = %s, want the marked system block", doer.lastN)
	}
}

// An empty final turn keeps its plain string form: this API rejects empty text
// blocks, so marking it would turn a benign turn into a request error.
func TestEmptyFinalTurnIsNotMarked(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleUser, Content: "hi"},
			{Role: provider.RoleTool, Content: "   "},
		},
		Tools: []provider.ToolDefinition{{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
	}); err != nil {
		t.Fatal(err)
	}
	var sent messagesRequest
	if err := json.Unmarshal(doer.lastN, &sent); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent.Messages[1].Content.(string); !ok {
		t.Fatalf("content = %#v, want the empty turn kept as a string", sent.Messages[1].Content)
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

// A caller that supplies tools without a response schema — the reflection
// loop's shape — must have those tools and the pinned choice put on the wire,
// and the model's tool_use blocks decoded into tool calls. Before this the
// adapter dropped both, so a reflection answered with no evidence.
func TestCallerToolsAreForwardedAndDecoded(t *testing.T) {
	body := `{"content":[
		{"type":"tool_use","id":"toolu_1","name":"keyword_search","input":{"query":"ada lovelace"}},
		{"type":"text","text":"final answer"}],
		"stop_reason":"tool_use"}`
	c, doer := testClient(body)
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "who?"}},
		Tools: []provider.ToolDefinition{
			{Name: "keyword_search", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		ToolChoice: &provider.ToolSelectionPolicy{Mode: "required", Name: "keyword_search"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent messagesRequest
	if err := json.Unmarshal(doer.lastN, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Name != "keyword_search" {
		t.Fatalf("tools = %+v, want the caller's tool forwarded", sent.Tools)
	}
	if string(sent.Tools[0].InputSchema) != `{"type":"object"}` {
		t.Fatalf("input_schema = %s, want the caller's parameter schema", sent.Tools[0].InputSchema)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "tool" || sent.ToolChoice.Name != "keyword_search" {
		t.Fatalf("tool_choice = %+v, want the pinned tool", sent.ToolChoice)
	}
	if len(result.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v, want the tool_use decoded", result.ToolCalls)
	}
	call := result.ToolCalls[0]
	if call.ID != "toolu_1" || call.Name != "keyword_search" {
		t.Fatalf("tool call = %+v", call)
	}
	var args map[string]any
	if err := json.Unmarshal(call.Arguments, &args); err != nil {
		t.Fatalf("arguments = %s", call.Arguments)
	}
	if args["query"] != "ada lovelace" {
		t.Fatalf("arguments = %s, want the tool input", call.Arguments)
	}
	if result.Content != "final answer" {
		t.Fatalf("content = %q, want the text block kept", result.Content)
	}
}

// An autonomous tool choice maps onto the API's auto form with no pinned name.
func TestAutoToolChoiceMapsToAuto(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages:   []provider.Message{{Role: provider.RoleUser, Content: "q"}},
		Tools:      []provider.ToolDefinition{{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &provider.ToolSelectionPolicy{Mode: "auto"},
	}); err != nil {
		t.Fatal(err)
	}
	var sent messagesRequest
	if err := json.Unmarshal(doer.lastN, &sent); err != nil {
		t.Fatal(err)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "auto" || sent.ToolChoice.Name != "" {
		t.Fatalf("tool_choice = %+v, want auto with no name", sent.ToolChoice)
	}
}

// The structured-output path still forces its own tool and keeps the caller's
// tools off the wire, so adding tool forwarding did not disturb it.
func TestStructuredPathIgnoresCallerTools(t *testing.T) {
	body := `{"content":[{"type":"tool_use","id":"toolu_s","name":"structured_response","input":{"a":1}}]}`
	c, doer := testClient(body)
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages:       []provider.Message{{Role: provider.RoleUser, Content: "extract"}},
		ResponseSchema: &provider.Schema{Type: "object"},
		Tools:          []provider.ToolDefinition{{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice:     &provider.ToolSelectionPolicy{Mode: "auto"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var sent messagesRequest
	if err := json.Unmarshal(doer.lastN, &sent); err != nil {
		t.Fatal(err)
	}
	if len(sent.Tools) != 1 || sent.Tools[0].Name != structuredToolName {
		t.Fatalf("tools = %+v, want only the forced structured tool", sent.Tools)
	}
	if sent.ToolChoice == nil || sent.ToolChoice.Type != "tool" || sent.ToolChoice.Name != structuredToolName {
		t.Fatalf("tool_choice = %+v, want the forced structured tool", sent.ToolChoice)
	}
	object, ok := result.Structured.(map[string]any)
	if !ok || object["a"] != float64(1) {
		t.Fatalf("structured = %+v, want the forced tool's value", result.Structured)
	}
	if len(result.ToolCalls) != 0 {
		t.Fatalf("tool calls = %+v, want the structured tool kept out of them", result.ToolCalls)
	}
}

// sessionID hashes the documented fingerprint shape independently of the
// shared package, so the wire assertion is not the implementation played back.
func sessionID(role, content string) string {
	roleJSON, _ := json.Marshal(role)
	contentJSON, _ := json.Marshal(content)
	sum := sha256.Sum256([]byte(`{"content":` + string(contentJSON) + `,"role":` + string(roleJSON) + `}`))
	return hex.EncodeToString(sum[:])[:32]
}

// An OpenCode Go endpoint rejects a request that omits its session header, so
// every call that targets one carries the conversation id — on the one-shot and
// the tool-calling path alike, since both reset the connection per request.
func TestOpenCodeSessionHeaderIsSent(t *testing.T) {
	for _, tc := range []struct {
		name string
		req  provider.CompletionRequest
	}{
		{
			name: "one shot",
			req: provider.CompletionRequest{
				Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
			},
		},
		{
			name: "tool call",
			req: provider.CompletionRequest{
				Messages:   []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
				Tools:      []provider.ToolDefinition{{Name: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
				ToolChoice: &provider.ToolSelectionPolicy{Mode: "auto"},
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
			c.Settings.BaseURL = "https://opencode.ai/zen/go/v1"
			if _, err := c.Complete(context.Background(), tc.req); err != nil {
				t.Fatal(err)
			}
			want := sessionID("user", "hi")
			if got := doer.last.Header.Get(cacheaffinity.OpenCodeSessionHeader); got != want {
				t.Fatalf("%s = %q, want %q", cacheaffinity.OpenCodeSessionHeader, got, want)
			}
		})
	}
}

// Any other endpoint gets no session header: it is a requirement of one host,
// not a hint to broadcast.
func TestNoSessionHeaderForAnotherHost(t *testing.T) {
	for _, baseURL := range []string{"https://api.anthropic.com/v1", "https://x.ai.evil.example/v1", ""} {
		c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
		c.Settings.BaseURL = baseURL
		if _, err := c.Complete(context.Background(), provider.CompletionRequest{
			Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatal(err)
		}
		if got := doer.last.Header.Get(cacheaffinity.OpenCodeSessionHeader); got != "" {
			t.Fatalf("base URL %q: %s = %q, want none", baseURL, cacheaffinity.OpenCodeSessionHeader, got)
		}
	}
}

// A value the operator configured wins over the derived session id.
func TestOperatorSessionHeaderWins(t *testing.T) {
	c, doer := testClient(`{"content":[{"type":"text","text":"ok"}]}`)
	c.Settings.BaseURL = "https://opencode.ai/zen/go/v1"
	c.Settings.Headers = map[string]string{cacheaffinity.OpenCodeSessionHeader: "operator-id"}
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if got := doer.last.Header.Get(cacheaffinity.OpenCodeSessionHeader); got != "operator-id" {
		t.Fatalf("%s = %q, want the operator's value", cacheaffinity.OpenCodeSessionHeader, got)
	}
}
