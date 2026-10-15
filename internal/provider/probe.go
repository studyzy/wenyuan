package provider

import "time"

// The provider connection probe, shared so every adapter probes the same way.
//
// A probe answers exactly one question — can this deployment reach its model —
// so it must be cheap, deterministic and identical across vendors. It is not a
// smoke test of answer quality, and it never varies with the model or the
// prompt under test.

// VerificationPrompt is the probe message the OpenAI-compatible and
// OpenAI-shaped adapters send.
const VerificationPrompt = "Say 'ok'"

// AnthropicVerificationPrompt is the probe message the Anthropic adapter sends.
// It is a different literal because the vendor's own probe is, and a probe that
// differs per vendor is still one question asked the same way.
const AnthropicVerificationPrompt = "test"

// VerificationMaxTokens is the probe's completion budget. It is deliberately
// generous: on a reasoning model the visible answer is preceded by scaffolding
// tokens, and a budget of one judges such a provider unreachable.
const VerificationMaxTokens = 512

// AnthropicVerificationMaxTokens is the Anthropic probe's budget.
const AnthropicVerificationMaxTokens = 10

// VerificationRetryPolicy is the retry policy the probe runs under. It is
// tighter than the default: startup should not wait out the operational retry
// ladder, and a second probe attempt is enough to tell a blip from an outage.
func VerificationRetryPolicy() RetryPolicy {
	return RetryPolicy{
		MaxRetries:     2,
		InitialBackoff: 500 * time.Millisecond,
		MaxBackoff:     2 * time.Second,
	}
}

// AnthropicVerificationRetryPolicy is the probe's policy on the Anthropic
// adapter, which does not retry its probe at all.
func AnthropicVerificationRetryPolicy() RetryPolicy {
	return RetryPolicy{MaxRetries: 0}
}
