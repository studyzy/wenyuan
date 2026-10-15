package app

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/mock"
)

// captureProvider records the requests it is asked to run.
type captureProvider struct {
	provider.Provider
	requests []provider.CompletionRequest
}

func (p *captureProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	p.requests = append(p.requests, req)
	return p.Provider.Complete(ctx, req)
}

func (p *captureProvider) Name() string { return p.Provider.Name() }

// The directive lands on the system message, after its existing content.
func TestOutputLanguageAppliedToSystemMessage(t *testing.T) {
	inner := &captureProvider{Provider: &mock.Provider{DimensionsValue: 4}}
	wrapped := WithOutputLanguage(inner, "French")
	_, err := wrapped.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: provider.RoleSystem, Content: "You answer questions."},
			{Role: provider.RoleUser, Content: "who is Alice?"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inner.requests) != 1 {
		t.Fatalf("inner saw %d requests", len(inner.requests))
	}
	system := inner.requests[0].Messages[0]
	if !strings.HasPrefix(system.Content, "You answer questions.") {
		t.Fatalf("existing system content was replaced: %q", system.Content)
	}
	if !strings.Contains(system.Content, "Respond exclusively in French") {
		t.Fatalf("directive missing: %q", system.Content)
	}
	// The user turn is untouched.
	if inner.requests[0].Messages[1].Content != "who is Alice?" {
		t.Fatalf("user message changed: %q", inner.requests[0].Messages[1].Content)
	}
}

// A request with no system message gains one, so the directive still reaches
// the model.
func TestOutputLanguageInsertsSystemMessage(t *testing.T) {
	inner := &captureProvider{Provider: &mock.Provider{DimensionsValue: 4}}
	wrapped := WithOutputLanguage(inner, "Japanese")
	if _, err := wrapped.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
	}); err != nil {
		t.Fatal(err)
	}
	messages := inner.requests[0].Messages
	if len(messages) != 2 || messages[0].Role != provider.RoleSystem {
		t.Fatalf("messages = %+v, want a leading system message", messages)
	}
	if !strings.Contains(messages[0].Content, "Respond exclusively in Japanese") {
		t.Fatalf("directive missing: %q", messages[0].Content)
	}
}

// With no language configured the provider is returned unwrapped and the
// request is unchanged.
func TestOutputLanguageUnsetLeavesRequestAlone(t *testing.T) {
	inner := &captureProvider{Provider: &mock.Provider{DimensionsValue: 4}}
	if got := WithOutputLanguage(inner, "  "); got != provider.Provider(inner) {
		t.Fatal("a blank language must leave the provider unwrapped")
	}
	if got := OutputLanguageDirective(""); got != "" {
		t.Fatalf("directive = %q, want empty", got)
	}
}
