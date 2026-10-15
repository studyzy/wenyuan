package consolidation

import (
	"strings"
	"testing"
)

// The parity suite identifies a pipeline step by an anchor phrase found anywhere
// in the call's text. Consolidation's calls must carry the consolidate anchor,
// and the user message must render the reference's MISSION / INPUT structure.
func TestConsolidationCarriesTheParityAnchorAndStructure(t *testing.T) {
	const anchor = "### Existing observations"
	if !strings.Contains(systemPrompt, anchor) {
		t.Fatalf("the system prompt is missing the consolidate anchor")
	}
	user := buildInput(
		[]Fact{{ID: "f1", Text: "Alice moved to Berlin in 2021"}},
		[]Observation{{ID: "o1", Text: "an existing conclusion"}},
		DefaultSettings(),
		-1,
	)
	if !strings.Contains(user, anchor) {
		t.Fatalf("the user message is missing the consolidate anchor:\n%s", user)
	}
	for _, want := range []string{"## MISSION", "## INPUT", "### New facts", "### Existing observations"} {
		if !strings.Contains(user, want) {
			t.Fatalf("user message is missing %q:\n%s", want, user)
		}
	}
	// The mission leads, the new facts come before the existing observations.
	if strings.Index(user, "## MISSION") > strings.Index(user, "### New facts") {
		t.Fatalf("the mission must lead the user message:\n%s", user)
	}
	if strings.Index(user, "### New facts") > strings.Index(user, "### Existing observations") {
		t.Fatalf("the new facts must precede the existing observations:\n%s", user)
	}
}

// An empty observation pool renders as an empty JSON array, and an empty facts
// batch likewise, matching the reference's json.dumps of the two lists.
func TestEmptyInputsRenderAsEmptyArrays(t *testing.T) {
	user := buildInput(nil, nil, DefaultSettings(), -1)
	if !strings.Contains(user, "### New facts\n\n[]") {
		t.Fatalf("empty facts must render as []:\n%s", user)
	}
	if !strings.Contains(user, "### Existing observations\n\n[]") {
		t.Fatalf("empty observations must render as []:\n%s", user)
	}
}

// The capacity constraint is written under its own heading and only when the
// observation cap is configured.
func TestCapacityConstraintHeading(t *testing.T) {
	settings := DefaultSettings()
	settings.MaxObservationsPerScope = 2
	user := buildInput([]Fact{{ID: "f1", Text: "one"}}, nil, settings, 0)
	if !strings.Contains(user, "## CAPACITY CONSTRAINT") {
		t.Fatalf("capacity constraint heading missing:\n%s", user)
	}
	without := buildInput([]Fact{{ID: "f1", Text: "one"}}, nil, DefaultSettings(), -1)
	if strings.Contains(without, "## CAPACITY CONSTRAINT") {
		t.Fatalf("no capacity note expected without a cap:\n%s", without)
	}
}
