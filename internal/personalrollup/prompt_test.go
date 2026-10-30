package personalrollup

import (
	"strings"
	"testing"
)

// promptText joins both messages so a test can assert on what the model is shown.
func promptText(previous []PreviousFact, fresh []SourceFact, topics []string, opts PromptOptions) string {
	messages := Prompt(previous, fresh, topics, opts)
	var out strings.Builder
	for _, message := range messages {
		out.WriteString(message.Content)
		out.WriteString("\n")
	}
	return out.String()
}

// TestPromptCarriesEveryInputFact pins decision 14 on this plane: the previous
// content and every new fact are in the prompt. A fact the model cannot see is a
// fact it will treat as nonexistent and drop.
func TestPromptCarriesEveryInputFact(t *testing.T) {
	previous := []PreviousFact{{Text: "deploy uses port 15432"}}
	fresh := []SourceFact{
		{BankID: "alice::orders", UnitID: "u1", Text: "orders calls billing"},
		{BankID: "alice::billing", UnitID: "u2", Text: "billing keeps amounts in cents"},
	}
	text := promptText(previous, fresh, nil, PromptOptions{})
	for _, want := range []string{
		"deploy uses port 15432",
		"orders calls billing",
		"billing keeps amounts in cents",
		"bank alice::orders",
		"bank alice::billing",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, text)
		}
	}
}

// TestPromptNumbersFreshInputs pins the contract ParseAnswer enforces: sources are
// F indexes, so every fresh fact must be numbered and no previous fact may be.
func TestPromptNumbersFreshInputs(t *testing.T) {
	previous := []PreviousFact{{Text: "old"}}
	fresh := []SourceFact{
		{BankID: "alice::a", UnitID: "u1", Text: "first"},
		{BankID: "alice::a", UnitID: "u2", Text: "second"},
	}
	text := promptText(previous, fresh, nil, PromptOptions{})
	for _, want := range []string{"[F0] first", "[F1] second", "[P0] old"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, text)
		}
	}
}

// TestPromptKeepsTheConcreteValues is the golden case the design asks for: the
// aggregate's value is that it stays checkable, so the prompt has to forbid
// abstracting values away.
func TestPromptKeepsTheConcreteValues(t *testing.T) {
	text := promptText(nil, nil, nil, PromptOptions{})
	for _, want := range []string{"ports", "paths", "versions", "configuration keys", "Never abstract"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt does not demand %q:\n%s", want, text)
		}
	}
}

func TestPromptListsKnownTopicsUpToTheWindow(t *testing.T) {
	topics := []string{"deploy", "postgres", "billing", "auth", "ci"}
	text := promptText(nil, nil, topics, PromptOptions{TopicWindow: 3})
	for _, want := range []string{"- deploy", "- postgres", "- billing"} {
		if !strings.Contains(text, want) {
			t.Fatalf("prompt does not contain %q:\n%s", want, text)
		}
	}
	for _, unwanted := range []string{"- auth", "- ci"} {
		if strings.Contains(text, unwanted) {
			t.Fatalf("prompt lists beyond the window: %q:\n%s", unwanted, text)
		}
	}
}

func TestPromptSaysWhenThereIsNothing(t *testing.T) {
	text := promptText(nil, nil, nil, PromptOptions{})
	if !strings.Contains(text, "Current memory:\n(none yet)") {
		t.Fatalf("empty aggregate is not stated:\n%s", text)
	}
	if !strings.Contains(text, "New facts from this person's banks:\n(none)") {
		t.Fatalf("empty input is not stated:\n%s", text)
	}
}

// TestPromptNeverNamesMembers pins that the aggregate's prompt does not inherit
// the project merge's "member" framing: the aggregate is one person's memory, and
// naming a source to the model invites it to write about who said what.
func TestPromptNeverNamesMembers(t *testing.T) {
	text := promptText(nil, []SourceFact{{BankID: "bob::orders", Text: "x"}}, nil, PromptOptions{})
	if strings.Contains(strings.ToLower(text), "member") && strings.Contains(text, "team") {
		t.Fatalf("prompt carries the team framing:\n%s", text)
	}
	if !strings.Contains(text, "person") {
		t.Fatalf("prompt does not describe a personal memory:\n%s", text)
	}
}
