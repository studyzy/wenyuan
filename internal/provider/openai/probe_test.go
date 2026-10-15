package openai

import (
	"context"
	"encoding/json"
	"io"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// probeBody is a minimal successful chat response for a probe test.
func probeBody() string {
	return `{"model":"test-model","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`
}

// The probe sends the recognisable literal, with the verification budget, on
// the verification scope. A deployment that watches its provider's traffic, and
// an operator reading a request log, both identify the probe by that text, so it
// is not a free-form greeting.
func TestVerifyConnectionSendsTheProbeLiteral(t *testing.T) {
	c, doer := testClient(jsonResponse(probeBody()))
	if err := c.VerifyConnection(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(doer.requests) != 1 {
		t.Fatalf("probe made %d requests, want 1", len(doer.requests))
	}
	raw, err := io.ReadAll(doer.requests[0].Body)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
		MaxTokens int `json:"max_tokens"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 1 || body.Messages[0].Content != provider.VerificationPrompt {
		t.Fatalf("probe messages = %+v", body.Messages)
	}
	if body.Messages[0].Role != string(provider.RoleUser) {
		t.Fatalf("probe role = %q", body.Messages[0].Role)
	}
	if body.MaxTokens != provider.VerificationMaxTokens {
		t.Fatalf("probe budget = %d, want %d", body.MaxTokens, provider.VerificationMaxTokens)
	}
}

// A probe against an unreachable provider reports the failure naming the
// provider and the model, so startup says which endpoint was checked.
func TestVerifyConnectionReportsTheProviderAndModel(t *testing.T) {
	c, _ := testClient(doubleResponse{status: 503, body: `{"error":{"message":"down"}}`})
	err := c.VerifyConnection(context.Background())
	if err == nil {
		t.Fatal("an unreachable provider must fail the probe")
	}
	if !provider.IsTransient(err) {
		t.Fatalf("probe error is %T, want transient", err)
	}
}
