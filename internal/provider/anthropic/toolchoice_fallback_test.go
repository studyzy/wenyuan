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

// scriptedResponses answers each call with the next scripted status and body,
// repeating the last one, and records every request body so a test can assert
// what the fallback actually sent.
type scriptedResponses struct {
	statuses []int
	bodies   []string
	calls    int
	sent     [][]byte
}

func (s *scriptedResponses) Do(req *http.Request) (*http.Response, error) {
	index := s.calls
	if index >= len(s.statuses) {
		index = len(s.statuses) - 1
	}
	s.calls++
	raw, _ := io.ReadAll(req.Body)
	s.sent = append(s.sent, raw)
	status := s.statuses[index]
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(s.bodies[index])),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

func fallbackClient(t *testing.T, statuses []int, bodies []string) (*Client, *scriptedResponses) {
	t.Helper()
	doer := &scriptedResponses{statuses: statuses, bodies: bodies}
	c := NewClient(provider.Settings{
		Provider: NameAnthropic,
		APIKey:   "test-key",
		BaseURL:  "https://example.test/v1",
		Model:    "claude-test",
		Timeout:  5 * time.Second,
	}, doer)
	c.Sleep = func(time.Duration) {}
	return c, doer
}

const toolChoiceRejection = `{"type":"error","error":{"type":"invalid_request_error","message":"tool_choice: type \"tool\" and \"any\" are not supported for this model"}}`

const okToolReply = `{"content":[{"type":"tool_use","id":"t1","name":"recall","input":{"query":"q"}}],"stop_reason":"tool_use","usage":{"input_tokens":3,"output_tokens":2}}`

// test-forced-tool-choice-falls-back-to-auto: a model that answers a forced
// tool_choice with the 400 that rejects it is resent once with tool_choice auto,
// and the call succeeds — reflect keeps working on models that reject the pin
// (the reference's #5738 fallback).
func TestForcedToolChoiceFallsBackToAuto(t *testing.T) {
	c, doer := fallbackClient(t,
		[]int{http.StatusBadRequest, http.StatusOK},
		[]string{toolChoiceRejection, okToolReply},
	)
	result, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages:   []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		Tools:      []provider.ToolDefinition{{Name: "recall", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &provider.ToolSelectionPolicy{Mode: "required", Name: "recall"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.ToolCalls) != 1 || result.ToolCalls[0].Name != "recall" {
		t.Fatalf("result = %+v, want the resent call's tool use", result)
	}
	if doer.calls != 2 {
		t.Fatalf("made %d attempts, want the forced request resent once", doer.calls)
	}
	var resent messagesRequest
	if err := json.Unmarshal(doer.sent[1], &resent); err != nil {
		t.Fatal(err)
	}
	if resent.ToolChoice == nil || resent.ToolChoice.Type != "auto" {
		t.Fatalf("resent tool_choice = %+v, want auto", resent.ToolChoice)
	}
}

// The fallback is sticky: once the model rejected a forced choice, later calls
// ask with auto up front instead of paying the 400 each time.
func TestForcedToolChoiceFallbackIsSticky(t *testing.T) {
	c, doer := fallbackClient(t,
		[]int{http.StatusBadRequest, http.StatusOK, http.StatusOK},
		[]string{toolChoiceRejection, okToolReply, okToolReply},
	)
	request := provider.CompletionRequest{
		Messages:   []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		Tools:      []provider.ToolDefinition{{Name: "recall", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &provider.ToolSelectionPolicy{Mode: "required", Name: "recall"},
	}
	if _, err := c.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Complete(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if doer.calls != 3 {
		t.Fatalf("made %d attempts, want the second call to send auto up front", doer.calls)
	}
	var second messagesRequest
	if err := json.Unmarshal(doer.sent[2], &second); err != nil {
		t.Fatal(err)
	}
	if second.ToolChoice == nil || second.ToolChoice.Type != "auto" {
		t.Fatalf("second call tool_choice = %+v, want auto", second.ToolChoice)
	}
}

// The detection is narrow: another 400 is not worth a resend, and the request
// fails as it did before.
func TestOtherBadRequestsAreNotResent(t *testing.T) {
	c, doer := fallbackClient(t,
		[]int{http.StatusBadRequest},
		[]string{`{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: field required"}}`},
	)
	_, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages:   []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		Tools:      []provider.ToolDefinition{{Name: "recall", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
		ToolChoice: &provider.ToolSelectionPolicy{Mode: "required", Name: "recall"},
	})
	if err == nil {
		t.Fatal("expected the request to fail")
	}
	if doer.calls != 1 {
		t.Fatalf("made %d attempts, want no resend for an unrelated 400", doer.calls)
	}
}

// A request that forces nothing is not resent either: the fallback exists for
// the forced choice the model rejected, not for every tool_choice error.
func TestUnforcedRequestIsNotResent(t *testing.T) {
	c, doer := fallbackClient(t,
		[]int{http.StatusBadRequest},
		[]string{toolChoiceRejection},
	)
	_, err := c.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		Tools:    []provider.ToolDefinition{{Name: "recall", Description: "search", Parameters: json.RawMessage(`{"type":"object"}`)}},
	})
	if err == nil {
		t.Fatal("expected the request to fail")
	}
	if doer.calls != 1 {
		t.Fatalf("made %d attempts, want no resend without a forced choice", doer.calls)
	}
}
