package openai

import (
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/cacheaffinity"
)

// affinityHint is the per-request cache-affinity hint: OpenAI's request field
// plus any headers the resolved mode asks for.
type affinityHint struct {
	// promptCacheKey is sent as the top-level request field; empty means none.
	promptCacheKey string
	// headers are per-request header hints, applied without displacing a value
	// the operator already configured.
	headers map[string]string
}

// cacheAffinityHint derives the hint for one conversation.
//
// The conventions live in the shared cache-affinity package; what is specific to
// this adapter is where the hint goes on the wire. Every branch is fail-open:
// with no id to derive, or a mode that asks for nothing, the request goes out
// byte-identical to one built before affinity existed.
func (c *Client) cacheAffinityHint(messages []provider.Message) affinityHint {
	mode, err := cacheaffinity.Parse(c.Settings.CacheAffinity)
	if err != nil {
		// Registration refuses a bad mode, so this is reachable only for a
		// settings value built by hand; the shipped default keeps the call
		// working rather than sending an unfamiliar field.
		mode = cacheaffinity.Auto
	}
	resolved := cacheaffinity.Resolve(mode, c.Name(), c.Settings.BaseURL)

	var hint affinityHint
	if id := cacheaffinity.ID(messages); id != "" {
		switch resolved {
		case cacheaffinity.XAIConvID:
			hint.headers = map[string]string{cacheaffinity.XAIConvIDHeader: id}
		case cacheaffinity.PromptCacheKey:
			// An operator value in the extra body reaches the same wire field,
			// so honour it and send nothing rather than sending both.
			if _, set := c.Settings.ExtraBody[cacheaffinity.PromptCacheKeyParam]; !set {
				hint.promptCacheKey = id
			}
		}
	}
	// An OpenCode Go endpoint requires its session header of every request it
	// serves, regardless of the configured cache mode, so it is merged rather
	// than folded into the switch above.
	for name, value := range cacheaffinity.OpenCodeSession(c.Settings.BaseURL, messages) {
		if hint.headers == nil {
			hint.headers = map[string]string{}
		}
		hint.headers[name] = value
	}
	return hint
}
