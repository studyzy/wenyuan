package cacheaffinity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// independentID hashes the documented fingerprint shape without going through
// the package, so the assertion is not the implementation's own arithmetic
// played back.
func independentID(role, content string) string {
	roleJSON, _ := json.Marshal(role)
	contentJSON, _ := json.Marshal(content)
	sum := sha256.Sum256([]byte(`{"content":` + string(contentJSON) + `,"role":` + string(roleJSON) + `}`))
	return hex.EncodeToString(sum[:])[:32]
}

// The mode is parsed the way the reference does: unset is the shipped default, a
// surrounding space and case are tolerated, and a typo is refused rather than
// silently disabling the hint.
func TestParse(t *testing.T) {
	for value, want := range map[string]string{
		"":                        Auto,
		"  ":                      Auto,
		"auto":                    Auto,
		"AUTO":                    Auto,
		" none ":                  None,
		"xai_conv_id":             XAIConvID,
		"openai_prompt_cache_key": PromptCacheKey,
	} {
		got, err := Parse(value)
		if err != nil {
			t.Fatalf("Parse(%q) raised %v", value, err)
		}
		if got != want {
			t.Fatalf("Parse(%q) = %q, want %q", value, got, want)
		}
	}
	if _, err := Parse("prefix"); err == nil {
		t.Fatal("an unrecognized mode must be refused")
	}
}

// Auto resolves from the provider name and the endpoint host; an explicit mode
// is returned unchanged.
func TestResolve(t *testing.T) {
	for _, tc := range []struct {
		mode     string
		provider string
		baseURL  string
		want     string
	}{
		{mode: Auto, provider: "openai", baseURL: "", want: PromptCacheKey},
		{mode: Auto, provider: "openai", baseURL: "https://api.openai.com/v1", want: PromptCacheKey},
		{mode: Auto, provider: "openai", baseURL: "https://my.openai.azure.com/v1", want: None},
		{mode: Auto, provider: "groq", baseURL: "https://api.groq.com/openai/v1", want: None},
		{mode: Auto, provider: "openai", baseURL: "https://grok.com/v1", want: XAIConvID},
		{mode: Auto, provider: "anything", baseURL: "https://api.x.ai/v1", want: XAIConvID},
		{mode: Auto, provider: "openai", baseURL: "https://x.ai.evil.example/v1", want: None},
		{mode: None, provider: "openai", baseURL: "", want: None},
		{mode: PromptCacheKey, provider: "groq", baseURL: "https://api.groq.com/openai/v1", want: PromptCacheKey},
	} {
		if got := Resolve(tc.mode, tc.provider, tc.baseURL); got != tc.want {
			t.Fatalf("Resolve(%q, %q, %q) = %q, want %q", tc.mode, tc.provider, tc.baseURL, got, tc.want)
		}
	}
}

// A host matches as itself or a subdomain, never as a substring or a lookalike.
func TestHostMatching(t *testing.T) {
	if !hostMatches("api.x.ai", "x.ai") || !hostMatches("x.ai", "x.ai") {
		t.Fatal("a domain and its subdomains must match")
	}
	if hostMatches("vertex.ai", "x.ai") || hostMatches("x.ai.evil.example", "x.ai") {
		t.Fatal("a substring or a lookalike host must not match")
	}
	if IsOpenCodeHost("https://api.x.ai/v1") || !IsOpenCodeHost("https://opencode.ai/zen/go/v1") {
		t.Fatal("only an OpenCode Go host may be reported as one")
	}
}

// The id is derived from the conversation's first message: constant as the
// message list grows, different for a different conversation, and empty with no
// conversation to fingerprint.
func TestID(t *testing.T) {
	first := provider.Message{Role: provider.RoleSystem, Content: "be terse"}
	if got, want := ID([]provider.Message{first}), independentID("system", "be terse"); got != want {
		t.Fatalf("ID = %q, want %q", got, want)
	}
	grown := []provider.Message{first, {Role: provider.RoleUser, Content: "who?"}, {Role: provider.RoleTool, Content: "result"}}
	if ID(grown) != ID([]provider.Message{first}) {
		t.Fatal("the id must not change as the conversation grows")
	}
	if ID([]provider.Message{{Role: provider.RoleUser, Content: "other"}}) == ID([]provider.Message{first}) {
		t.Fatal("a different conversation must get a different id")
	}
	if ID(nil) != "" {
		t.Fatal("no conversation means no id")
	}
	if len(ID([]provider.Message{first})) != 32 {
		t.Fatal("the id is 32 hex characters")
	}
}

// The session header is returned only for an OpenCode Go endpoint that has a
// conversation to identify.
func TestOpenCodeSession(t *testing.T) {
	messages := []provider.Message{{Role: provider.RoleUser, Content: "hi"}}
	got := OpenCodeSession("https://opencode.ai/zen/go/v1", messages)
	if len(got) != 1 || got[OpenCodeSessionHeader] != ID(messages) {
		t.Fatalf("session = %v, want the conversation id under %s", got, OpenCodeSessionHeader)
	}
	if other := OpenCodeSession("https://api.openai.com/v1", messages); other != nil {
		t.Fatalf("session = %v, want none for another host", other)
	}
	if other := OpenCodeSession("https://opencode.ai/zen/go/v1", nil); other != nil {
		t.Fatalf("session = %v, want none without a conversation", other)
	}
}
