package reflect

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// The length-rewrite second pass asks with the reference's prompts verbatim:
// the shorten instruction as the system message, and the target budget plus the
// over-budget text as the user message.
func TestRewritePromptsMatchReference(t *testing.T) {
	long := strings.TrimSpace(strings.Repeat("this is a long answer. ", 200))
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(toolCall(ToolSearchObservations, map[string]any{"query": "q"}), toolCall(ToolDone, nil)),
		provider.CompletionResult{Content: long},
		provider.CompletionResult{Content: "short"},
	)}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "fact"}}}
	if _, err := NewLoop(p, retrieval, Config{Question: "q", MaxTokens: 20}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(p.requests) != 4 {
		t.Fatalf("provider saw %d calls, want the extra rewrite call", len(p.requests))
	}
	rewrite := p.requests[3]
	if len(rewrite.Messages) != 2 {
		t.Fatalf("rewrite messages = %#v", rewrite.Messages)
	}
	if rewrite.Messages[0].Role != provider.RoleSystem || rewrite.Messages[0].Content != rewriteSystem {
		t.Fatalf("rewrite system prompt = %q", rewrite.Messages[0].Content)
	}
	wantUser := "Target budget: 20 tokens.\n\nText to rewrite:\n" + long
	if rewrite.Messages[1].Content != wantUser {
		t.Fatalf("rewrite user prompt = %q, want %q", rewrite.Messages[1].Content, wantUser)
	}
}
