// Package cacheaffinity holds the provider-neutral half of server-side prompt
// cache affinity.
//
// Prompt caching only pays off when the same conversation reaches the same
// backend cache, and OpenAI-compatible backends expose different mechanisms for
// that: OpenAI accepts a prompt_cache_key request field that improves its own
// cache routing, xAI stores cache entries per backend server and pins a request
// carrying x-grok-conv-id to one server, and OpenCode Go groups a conversation
// by x-opencode-session. The affinity is resolved from the provider name and the
// endpoint host, and an unknown backend receives no unfamiliar field: the auto
// mode is an allowlist rather than a best-effort probe.
//
// What lives here is what every adapter needs; what a given adapter does with it
// — OpenAI's request field, Anthropic's header-only requirement — stays in that
// adapter.
package cacheaffinity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Cache-affinity modes.
const (
	// None sends no hint.
	None = "none"
	// XAIConvID pins a request to one xAI backend server.
	XAIConvID = "xai_conv_id"
	// PromptCacheKey sends OpenAI's cache-routing field.
	PromptCacheKey = "openai_prompt_cache_key"
	// Auto resolves the mode from the provider and the host.
	Auto = "auto"
)

// Wire names.
const (
	// XAIConvIDHeader is xAI's documented cache-pinning header.
	XAIConvIDHeader = "x-grok-conv-id"
	// PromptCacheKeyParam is OpenAI's cache-routing request field.
	PromptCacheKeyParam = "prompt_cache_key"
	// OpenCodeSessionHeader is OpenCode Go's conversation-grouping header.
	OpenCodeSessionHeader = "x-opencode-session"
)

// promptCacheKeyProvider is the provider name whose endpoints accept OpenAI's
// own cache-routing field.
const promptCacheKeyProvider = "openai"

// Host allowlists. A host is matched as itself or a subdomain, never as a
// substring: a bare "x.ai" test would also match "vertex.ai".
var (
	xaiHosts      = []string{"x.ai", "grok.com"}
	openAIHosts   = []string{"openai.com"}
	openCodeHosts = []string{"opencode.ai"}
)

// Parse validates a configured mode. An unset value means the reference's
// shipped default, auto; an unrecognized value is an error, because the whole
// point of the setting is that its effect is invisible in the response, so a
// typo must not silently disable it.
func Parse(value string) (string, error) {
	mode := strings.ToLower(strings.TrimSpace(value))
	if mode == "" {
		return Auto, nil
	}
	switch mode {
	case None, XAIConvID, PromptCacheKey, Auto:
		return mode, nil
	}
	return "", fmt.Errorf("invalid cache affinity %q: must be one of %s, %s, %s, %s",
		value, None, XAIConvID, PromptCacheKey, Auto)
}

// Resolve turns auto into a concrete mode from the provider name and the
// endpoint host, and returns any other mode unchanged. The xAI check is
// host-only and provider-independent, because the documented setup for an xAI
// endpoint is an x.ai base URL under another provider name.
func Resolve(mode, providerName, baseURL string) string {
	if mode != Auto {
		return mode
	}
	host := hostOf(baseURL)
	if hostMatchesAny(host, xaiHosts) {
		return XAIConvID
	}
	if strings.EqualFold(providerName, promptCacheKeyProvider) {
		if host == "" || hostMatchesAny(host, openAIHosts) {
			return PromptCacheKey
		}
	}
	return None
}

// ID returns the affinity id for one conversation, or empty to send no hint at
// all.
//
// The reference prefers the run's own id — one uuid per retain, reflect or
// consolidation run, hashed to 32 hex characters — and falls back to a hash of
// the conversation's first message when no run context is bound. This boundary
// carries no per-run id, so the fallback is the available source. It has the
// property cache pinning needs: the first message is the stable system prompt of
// a conversation, so the id stays constant as the message list grows through an
// agent loop, while separate conversations get separate ids. It is derived from
// the request the caller made, so every adapter of one request mints the same id.
func ID(messages []provider.Message) string {
	if len(messages) == 0 {
		return ""
	}
	first := messages[0]
	canonical, err := json.Marshal(fingerprint{Content: first.Content, Role: string(first.Role)})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])[:32]
}

// fingerprint is the canonical shape one message is hashed as. Field order is
// alphabetical so the encoding is stable.
type fingerprint struct {
	Content string `json:"content"`
	Role    string `json:"role"`
}

// IsOpenCodeHost reports whether baseURL targets an OpenCode Go host.
func IsOpenCodeHost(baseURL string) bool {
	return hostMatchesAny(hostOf(baseURL), openCodeHosts)
}

// OpenCodeSession returns the header a request to this endpoint must carry, or
// nil when the endpoint does not require one.
//
// OpenCode Go asks clients to send x-opencode-session so the requests of one
// conversation are grouped, and rejects a request that omits it. That makes it a
// protocol requirement rather than a cache optimization: it is applied whenever
// the endpoint is on an OpenCode Go host, regardless of any configured cache
// mode.
func OpenCodeSession(baseURL string, messages []provider.Message) map[string]string {
	if !IsOpenCodeHost(baseURL) {
		return nil
	}
	id := ID(messages)
	if id == "" {
		return nil
	}
	return map[string]string{OpenCodeSessionHeader: id}
}

// hostOf returns the endpoint's hostname, or empty when the URL is unusable.
func hostOf(baseURL string) string {
	if strings.TrimSpace(baseURL) == "" {
		return ""
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return ""
	}
	return strings.ToLower(parsed.Hostname())
}

// hostMatches reports whether host is the domain itself or a subdomain of it.
func hostMatches(host, domain string) bool {
	host = strings.ToLower(host)
	return host == domain || strings.HasSuffix(host, "."+domain)
}

// hostMatchesAny reports whether the host matches any of the domains.
func hostMatchesAny(host string, domains []string) bool {
	if host == "" {
		return false
	}
	for _, domain := range domains {
		if hostMatches(host, domain) {
			return true
		}
	}
	return false
}
