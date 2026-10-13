package openai

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

func chatBody(content, finish string) string {
	encoded, _ := json.Marshal(content)
	return `{"model":"test-model","choices":[{"message":{"content":` + string(encoded) +
		`},"finish_reason":"` + finish + `"}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}`
}

func TestCompleteFreeFormReturnsContentAndUsage(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody("hello there", "stop")))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hi"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello there" {
		t.Fatalf("content = %q", result.Content)
	}
	if result.Usage.InputTokens != 10 || result.Usage.OutputTokens != 5 || result.Usage.TotalTokens != 15 {
		t.Fatalf("usage = %+v", result.Usage)
	}
	if result.Usage.CachedInputTokens != 2 || result.Usage.ReasoningTokens != 1 {
		t.Fatalf("detailed usage = %+v", result.Usage)
	}
	if result.Model != "test-model" || result.FinishReason != "stop" {
		t.Fatalf("model=%q finish=%q", result.Model, result.FinishReason)
	}
}

// test-malformed-success-response: a malformed success body raises a typed
// malformed-response error.
func TestMalformedSuccessBodyRaisesTypedError(t *testing.T) {
	c, _ := testClient(jsonResponse(`{"not":"a chat response"}`))
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if !provider.IsMalformedResponse(err) {
		t.Fatalf("error is %T, want malformed-response", err)
	}
}

// test-output-too-long: hitting the token limit raises a distinct
// output-too-long error.
func TestOutputTooLongRaisesDistinctError(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody("partial", "length")))
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if !provider.IsOutputTooLong(err) {
		t.Fatalf("error is %T, want output-too-long", err)
	}
}

// test-freeform-strips-scaffolding: reasoning and scaffolding tags are
// stripped from free-form content.
func TestFreeFormStripsScaffolding(t *testing.T) {
	c, _ := testClient(jsonResponse(chatBody("<thinking>let me think</thinking>The answer is 42.", "stop")))
	result, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "The answer is 42." {
		t.Fatalf("content = %q, want the scaffolding stripped", result.Content)
	}
}

// test-content-policy-permanent: a content-policy refusal is raised without
// transport retry.
func TestContentPolicyRaisedWithoutRetry(t *testing.T) {
	c, doer := testClient(doubleResponse{
		status: http.StatusBadRequest,
		body:   `{"error":{"message":"rejected by content policy"}}`,
	})
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if !provider.IsContentPolicy(err) {
		t.Fatalf("error is %T, want content-policy", err)
	}
	if doer.calls != 1 {
		t.Fatalf("made %d attempts, want exactly 1 (no retry)", doer.calls)
	}
}

// A 401 is classified as a missing credential, not retried.
func TestUnauthorizedClassifiedAsMissingCredential(t *testing.T) {
	c, doer := testClient(doubleResponse{status: http.StatusUnauthorized, body: `{"error":{"message":"no key"}}`})
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if !provider.IsMissingCredential(err) {
		t.Fatalf("error is %T, want missing-credential", err)
	}
	if doer.calls != 1 {
		t.Fatalf("401 should not be retried, made %d attempts", doer.calls)
	}
}

// A malformed method that survives several 400s is not retried forever.
func TestBadRequestNotRetriedIndefinitely(t *testing.T) {
	c, doer := testClient(doubleResponse{status: http.StatusBadRequest, body: `{"error":{"message":"bad request"}}`})
	_, err := c.Complete(context.Background(), provider.CompletionRequest{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if doer.calls > 3 {
		t.Fatalf("made %d attempts, want a bounded budget", doer.calls)
	}
}

// The Authorization header carries the configured credential.
func TestCredentialSentInHeader(t *testing.T) {
	c, doer := testClient(jsonResponse(chatBody("ok", "stop")))
	if _, err := c.Complete(context.Background(), provider.CompletionRequest{}); err != nil {
		t.Fatal(err)
	}
	got := doer.requests[0].Header.Get("Authorization")
	if got != "Bearer test-key" {
		t.Fatalf("authorization = %q", got)
	}
}
