package openai

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

const toolChoiceRejection = `{"error":{"message":"The tool_choice parameter does not support being set to required or object in thinking mode","type":"invalid_request_error"}}`

// readBody reads a recorded request's body, which the fake transport leaves
// unread for the test to inspect.
func readRequestBody(t *testing.T, req *http.Request) []byte {
	t.Helper()
	raw, err := io.ReadAll(req.Body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func toolLoopRequest() provider.CompletionRequest {
	return provider.CompletionRequest{
		Messages:   []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		Tools:      []provider.ToolDefinition{{Name: "recall", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &provider.ToolSelectionPolicy{Mode: "required", Name: "recall"},
	}
}

// test-forced-tool-choice-falls-back-to-auto: an endpoint that answers a forced
// tool_choice with the 400 that rejects it is resent once with the field
// dropped — omitted tool_choice is auto — and the call succeeds.
func TestForcedToolChoiceFallsBackToAuto(t *testing.T) {
	c, doer := testClient(
		doubleResponse{status: http.StatusBadRequest, body: toolChoiceRejection},
		jsonResponse(chatBody("ignored", "stop")),
	)
	result, err := c.Complete(context.Background(), toolLoopRequest())
	if err != nil {
		t.Fatal(err)
	}
	if doer.calls != 2 {
		t.Fatalf("made %d attempts, want the forced request resent once", doer.calls)
	}
	var resent chatRequest
	if err := json.Unmarshal(readRequestBody(t, doer.requests[1]), &resent); err != nil {
		t.Fatal(err)
	}
	if resent.ToolChoice != nil {
		t.Fatalf("resent tool_choice = %v, want the field dropped (auto)", resent.ToolChoice)
	}
	if len(resent.Tools) == 0 {
		t.Fatal("the resent request must keep the tool list")
	}
	_ = result
}

// The fallback is sticky: once the endpoint rejected a forced choice, later
// calls ask with auto up front instead of paying the 400 each time.
func TestForcedToolChoiceFallbackIsSticky(t *testing.T) {
	c, doer := testClient(
		doubleResponse{status: http.StatusBadRequest, body: toolChoiceRejection},
		jsonResponse(chatBody("ignored", "stop")),
		jsonResponse(chatBody("ignored", "stop")),
	)
	if _, err := c.Complete(context.Background(), toolLoopRequest()); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), toolLoopRequest()); err != nil {
		t.Fatal(err)
	}
	if doer.calls != 3 {
		t.Fatalf("made %d attempts, want the second call to send auto up front", doer.calls)
	}
	var second chatRequest
	if err := json.Unmarshal(readRequestBody(t, doer.requests[2]), &second); err != nil {
		t.Fatal(err)
	}
	if second.ToolChoice != nil {
		t.Fatalf("second call tool_choice = %v, want the field dropped (auto)", second.ToolChoice)
	}
}

// The detection is narrow: another 400 is not worth a resend, and the request
// fails as it did before.
func TestOtherBadRequestsAreNotResent(t *testing.T) {
	c, doer := testClient(doubleResponse{status: http.StatusBadRequest, body: `{"error":{"message":"max_tokens is too large"}}`})
	if _, err := c.Complete(context.Background(), toolLoopRequest()); err == nil {
		t.Fatal("expected the request to fail")
	}
	if doer.calls != 1 {
		t.Fatalf("made %d attempts, want no resend for an unrelated 400", doer.calls)
	}
	if !strings.Contains(doer.requests[0].URL.Path, "chat/completions") {
		t.Fatalf("request path = %q", doer.requests[0].URL.Path)
	}
}

// A request that forces nothing is not resent either: the fallback exists for
// the forced choice the endpoint rejected, not for every tool_choice error.
func TestUnforcedRequestIsNotResent(t *testing.T) {
	c, doer := testClient(doubleResponse{status: http.StatusBadRequest, body: toolChoiceRejection})
	request := toolLoopRequest()
	request.ToolChoice = nil
	if _, err := c.Complete(context.Background(), request); err == nil {
		t.Fatal("expected the request to fail")
	}
	if doer.calls != 1 {
		t.Fatalf("made %d attempts, want no resend without a forced choice", doer.calls)
	}
}
