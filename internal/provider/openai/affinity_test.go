package openai

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/cacheaffinity"
)

// fingerprintID is the affinity id the test expects for a first message, hashed
// independently of the adapter so the assertion is not the implementation's own
// arithmetic played back.
func fingerprintID(role, content string) string {
	canonical := `{"content":` + string(mustJSON(content)) + `,"role":` + string(mustJSON(role)) + `}`
	sum := sha256.Sum256([]byte(canonical))
	return hex.EncodeToString(sum[:])[:32]
}

func mustJSON(value string) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}

// A bare OpenAI endpoint with no configured affinity gets OpenAI's own
// cache-routing field, carrying a key derived from the conversation's stable
// first message.
func TestAutoAffinitySendsPromptCacheKeyToOpenAI(t *testing.T) {
	c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
	c.Settings.Provider = NameOpenAI
	c.Settings.BaseURL = "https://api.openai.com/v1"
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	want := `{"model":"test-model","messages":[{"role":"user","content":"hi"}],` +
		`"prompt_cache_key":"` + fingerprintID("user", "hi") + `"}`
	if got := string(doer.lastBody(t)); got != want {
		t.Fatalf("body = %s\nwant   %s", got, want)
	}
}

// The key is derived from the first message alone, so it stays constant as an
// agent loop grows the conversation — which is the property cache pinning
// needs — while a different conversation gets a different key.
func TestPromptCacheKeyIsStableAcrossAGrowingConversation(t *testing.T) {
	c, _ := testClient(
		jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`),
		jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`),
	)
	c.Settings.Provider = NameOpenAI
	c.Settings.BaseURL = "https://api.openai.com/v1"
	first := []provider.Message{
		{Role: provider.RoleSystem, Content: "be terse"},
		{Role: provider.RoleUser, Content: "who?"},
	}
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{Messages: first}); err != nil {
		t.Fatal(err)
	}
	grown := append(append([]provider.Message{}, first...),
		provider.Message{Role: provider.RoleAssistant, Content: "checking"},
		provider.Message{Role: provider.RoleTool, Content: "result"},
	)
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{Messages: grown}); err != nil {
		t.Fatal(err)
	}
	keys := c.promptKeys(t)
	if len(keys) != 2 || keys[0] == "" || keys[0] != keys[1] {
		t.Fatalf("keys = %v, want the same non-empty key for both turns of one loop", keys)
	}
	other := []provider.Message{{Role: provider.RoleSystem, Content: "different"}}
	if got := cacheaffinity.ID(other); got == keys[0] {
		t.Fatalf("id = %q, want a different key for a different conversation", got)
	}
}

// An unknown OpenAI-compatible backend receives no unfamiliar field: the auto
// mode is an allowlist, so a gateway that never documented the parameter is
// never sent it.
func TestAutoAffinitySendsNothingToUnknownBackends(t *testing.T) {
	for _, settings := range []struct {
		name     string
		provider string
		baseURL  string
	}{
		{name: "groq", provider: "groq", baseURL: "https://api.groq.com/openai/v1"},
		{name: "azure", provider: "openai", baseURL: "https://my-deployment.openai.azure.com/v1"},
		{name: "vertex-ish", provider: "openai", baseURL: "https://vertex.ai/v1"},
		{name: "loopback", provider: "ollama", baseURL: "http://localhost:11434/v1"},
	} {
		t.Run(settings.name, func(t *testing.T) {
			c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
			c.Settings.Provider = settings.provider
			c.Settings.BaseURL = settings.baseURL
			if _, err := c.Complete(context.Background(), provider.CompletionRequest{
				Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
			}); err != nil {
				t.Fatal(err)
			}
			if body := string(doer.lastBody(t)); strings.Contains(body, cacheaffinity.PromptCacheKeyParam) {
				t.Fatalf("body = %s, want no affinity field", body)
			}
			req := doer.requests[0]
			if got := req.Header.Get(cacheaffinity.XAIConvIDHeader); got != "" {
				t.Fatalf("%s = %q, want no affinity header", cacheaffinity.XAIConvIDHeader, got)
			}
			if got := req.Header.Get(cacheaffinity.OpenCodeSessionHeader); got != "" {
				t.Fatalf("%s = %q, want no affinity header", cacheaffinity.OpenCodeSessionHeader, got)
			}
		})
	}
}

// An xAI host is pinned with xAI's documented conversation header rather than
// OpenAI's request field, whichever provider name reaches it.
func TestXAIConvIDHeaderPinsAnXAIHost(t *testing.T) {
	c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
	c.Settings.Provider = "grok"
	c.Settings.BaseURL = "https://api.x.ai/v1"
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	want := fingerprintID("user", "hi")
	if got := doer.requests[0].Header.Get(cacheaffinity.XAIConvIDHeader); got != want {
		t.Fatalf("%s = %q, want %q", cacheaffinity.XAIConvIDHeader, got, want)
	}
	if body := string(doer.lastBody(t)); strings.Contains(body, cacheaffinity.PromptCacheKeyParam) {
		t.Fatalf("body = %s, want no prompt_cache_key on an xAI host", body)
	}
}

// OpenCode Go requires its session header of every request its host serves, so
// it is sent even when cache pinning is switched off entirely.
func TestOpenCodeSessionHeaderIsSentRegardlessOfMode(t *testing.T) {
	for _, mode := range []string{"", cacheaffinity.None} {
		c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
		c.Settings.Provider = "opencode-go"
		c.Settings.BaseURL = "https://opencode.ai/zen/go/v1"
		c.Settings.CacheAffinity = mode
		if _, err := c.Complete(context.Background(), provider.CompletionRequest{
			Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatal(err)
		}
		if got := doer.requests[0].Header.Get(cacheaffinity.OpenCodeSessionHeader); got != fingerprintID("user", "hi") {
			t.Fatalf("mode %q: %s = %q, want the conversation id", mode, cacheaffinity.OpenCodeSessionHeader, got)
		}
	}
}

// An explicit none mode disables cache pinning without disabling a header the
// backend requires for other reasons.
func TestNoneModeSendsNoCacheHint(t *testing.T) {
	c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
	c.Settings.Provider = NameOpenAI
	c.Settings.BaseURL = "https://api.openai.com/v1"
	c.Settings.CacheAffinity = cacheaffinity.None
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if body := string(doer.lastBody(t)); strings.Contains(body, cacheaffinity.PromptCacheKeyParam) {
		t.Fatalf("body = %s, want no prompt_cache_key under none", body)
	}
}

// A value the operator configured wins over the derived hint: the header is
// kept, and an operator prompt_cache_key suppresses ours rather than being
// sent alongside it.
func TestOperatorValuesWinOverDerivedHints(t *testing.T) {
	t.Run("header", func(t *testing.T) {
		c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
		c.Settings.Provider = "grok"
		c.Settings.BaseURL = "https://api.x.ai/v1"
		c.Settings.Headers = map[string]string{cacheaffinity.XAIConvIDHeader: "operator-id"}
		if _, err := c.Complete(context.Background(), provider.CompletionRequest{
			Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatal(err)
		}
		if got := doer.requests[0].Header.Get(cacheaffinity.XAIConvIDHeader); got != "operator-id" {
			t.Fatalf("%s = %q, want the operator's value", cacheaffinity.XAIConvIDHeader, got)
		}
	})

	t.Run("extra body", func(t *testing.T) {
		c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
		c.Settings.Provider = NameOpenAI
		c.Settings.BaseURL = "https://api.openai.com/v1"
		c.Settings.ExtraBody = map[string]any{cacheaffinity.PromptCacheKeyParam: "operator-key"}
		if _, err := c.Complete(context.Background(), provider.CompletionRequest{
			Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
		}); err != nil {
			t.Fatal(err)
		}
		body := string(doer.lastBody(t))
		if !strings.Contains(body, `"`+cacheaffinity.PromptCacheKeyParam+`":"operator-key"`) {
			t.Fatalf("body = %s, want the operator's key", body)
		}
		if strings.Contains(body, fingerprintID("user", "hi")) {
			t.Fatalf("body = %s, want no derived key alongside the operator's", body)
		}
	})
}

// A call with no conversation has no first message to derive an id from, so it
// goes out with no hint rather than a fabricated one.
func TestNoMessagesYieldsNoAffinityHint(t *testing.T) {
	c, doer := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
	c.Settings.Provider = NameOpenAI
	c.Settings.BaseURL = "https://api.openai.com/v1"
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	if body := string(doer.lastBody(t)); strings.Contains(body, cacheaffinity.PromptCacheKeyParam) {
		t.Fatalf("body = %s, want no hint without a conversation", body)
	}
}

// A retried call carries the same affinity hint on every attempt.
func TestAffinityHintRidesEveryRetry(t *testing.T) {
	c, doer := testClient(
		doubleResponse{status: 500, body: `{"error":{"message":"boom"}}`},
		jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`),
	)
	c.Settings.Provider = NameOpenAI
	c.Settings.BaseURL = "https://api.openai.com/v1"
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	}); err != nil {
		t.Fatal(err)
	}
	if doer.calls != 2 {
		t.Fatalf("calls = %d, want the transient failure retried", doer.calls)
	}
	want := fingerprintID("user", "hi")
	for i, req := range doer.requests {
		body := requestBody(t, req)
		if !strings.Contains(string(body), `"`+cacheaffinity.PromptCacheKeyParam+`":"`+want+`"`) {
			t.Fatalf("attempt %d body = %s, want the same affinity key", i, body)
		}
	}
}

// A typo in the configured mode is refused at provider construction, where the
// operator sees it, rather than silently disabling an invisible optimization.
func TestRegisterRefusesAnUnrecognizedAffinityMode(t *testing.T) {
	reg := provider.NewRegistry()
	Register(reg)
	entry, ok := reg.Lookup(NameOpenAI)
	if !ok {
		t.Fatal("openai is not registered")
	}
	if _, err := entry.Build(provider.Settings{CacheAffinity: "prefix"}); err == nil {
		t.Fatal("Build must refuse an unrecognized cache affinity")
	}
	built, err := entry.Build(provider.Settings{CacheAffinity: "auto"})
	if err != nil {
		t.Fatal(err)
	}
	if built.Name() != NameOpenAI {
		t.Fatalf("name = %q", built.Name())
	}
}

// lastBody returns the most recent request body the transport double saw.
func (d *transportDouble) lastBody(t *testing.T) []byte {
	t.Helper()
	if len(d.requests) == 0 {
		t.Fatal("no request reached the transport double")
	}
	return requestBody(t, d.requests[len(d.requests)-1])
}

// requestBody reads a request body the transport double recorded. The double
// never reads one itself, so it is still intact here.
func requestBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	body, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// promptKeys returns the prompt_cache_key of every recorded request.
func (c *Client) promptKeys(t *testing.T) []string {
	t.Helper()
	doer, ok := c.HTTP.(*transportDouble)
	if !ok {
		t.Fatal("the test client is not backed by the transport double")
	}
	keys := make([]string, 0, len(doer.requests))
	for _, req := range doer.requests {
		body := requestBody(t, req)
		var decoded struct {
			Key string `json:"prompt_cache_key"`
		}
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatal(err)
		}
		keys = append(keys, decoded.Key)
	}
	return keys
}
