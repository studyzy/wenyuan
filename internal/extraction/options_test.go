package extraction

import (
	"context"
	"strings"
	"testing"
)

// The bank's mission is carried in the user message as the FOCUS preamble — not
// in the system prompt, which stays bank-agnostic — and a custom-mode bank's
// instructions are substituted into the system prompt in place of the shipped
// guidelines.
func TestPromptCarriesMissionAndInstructions(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{}}}
	opts := Options{
		Mode:         ModeCustom,
		Mission:      "Track everything about the Apollo programme.",
		Instructions: "Prefer dates over prose.",
	}
	if _, err := New(stub).ExtractWith(context.Background(), "text", opts); err != nil {
		t.Fatal(err)
	}
	system := stub.lastReq.Messages[0].Content
	if !strings.Contains(system, "Prefer dates over prose.") {
		t.Fatalf("custom instructions missing from the system prompt:\n%s", system)
	}
	if strings.Contains(system, "Track everything about the Apollo programme.") {
		t.Fatalf("the mission must not be in the system prompt:\n%s", system)
	}
	user := stub.lastReq.Messages[1].Content
	for _, want := range []string{
		"FOCUS — What to retain for this bank (takes priority over the general guidelines)",
		"Track everything about the Apollo programme.",
		"Extract facts from the following chunk.",
	} {
		if !strings.Contains(user, want) {
			t.Fatalf("user message missing %q:\n%s", want, user)
		}
	}
}

// With nothing configured the system prompt is exactly the shipped prompt, so
// an unwired caller sends the same request as before.
func TestDefaultPromptIsUnchanged(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{}}}
	if _, err := New(stub).Extract(context.Background(), "text"); err != nil {
		t.Fatal(err)
	}
	if got := stub.lastReq.Messages[0].Content; got != factExtractionPrompt {
		t.Fatalf("unconfigured prompt drifted:\n%s", got)
	}
}

// A blank mission is skipped rather than rendering an empty FOCUS block.
func TestEmptyMissionIsSkipped(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{}}}
	if _, err := New(stub).ExtractWith(context.Background(), "text", Options{Mission: "   "}); err != nil {
		t.Fatal(err)
	}
	user := stub.lastReq.Messages[1].Content
	if strings.Contains(user, "FOCUS —") {
		t.Fatalf("a blank mission must not render a FOCUS block:\n%s", user)
	}
}
