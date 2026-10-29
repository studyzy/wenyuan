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

// The system prompt is the reference's v1.1.0 consolidation contract, copied
// verbatim: it is the LLM's instruction, so a paraphrase is a different
// contract. The golden pins every rule and field rule the parity work ports,
// word for word (engine/consolidation/prompts.py:49-61, 149-162, 174, 177).
func TestSystemPromptMatchesReferenceVerbatim(t *testing.T) {
	golden := []string{
		// Rule 5's extension: observations that only held because of the old
		// state are updated away too.
		"Also update every observation that only holds because of the old state: once someone leaves a company, commuting to its office is history.",
		// Rule 8's extension: a count is never written into a member's observation.
		"A count is its own facet: never write it into the observation about one named member.",
		// Rule 9's extension: near-miss names are different entities.
		"Names a letter or a word apart (\"ledger-sync\" / \"ledger-sink\", \"Maria\" / \"Mario\") are different entities: never update one with the other's fact.",
		// Rule 10: recency is mentioned_at, not arrival order.
		"10. RECENCY IS `mentioned_at`, NOT ARRIVAL ORDER: facts can arrive out of order. Before a fact changes an observation, compare their `mentioned_at`. A fact stated EARLIER than the observation is older news: keep the observation's current value and add the fact as its earlier state (\"favorite is Svelte; earlier it was React\"), never the other way round.",
		// Rule 11: ephemeral chatter is skipped.
		"11. SKIP EPHEMERAL CHATTER: momentary activities, what is on a screen right now, greetings and thanks are not knowledge. Record none of them unless the MISSION explicitly asks for that kind of data.",
		// Rule 12: an update replaces the whole text.
		"12. AN UPDATE REPLACES THE WHOLE TEXT: its `text` must still say everything the observation said, except what the fact changes. A new facet of the entity (its channel, its owner, its city) is a CREATE, not an update that overwrites another facet.",
		// Example 3 is an UPDATE with a merged delete, not a DELETE and a sibling CREATE.
		"### Example 3 — Two observations restate one fact: the change UPDATES one and merges the other into it (note `merged_into`)",
		`"updates": [{"observation_id": "33333333-3333-3333-3333-333333333333", "text": "Bob uses the product through open signup; the beta waitlist he was on was shut down in June 2025.", "source_fact_ids": ["e5f6a7b8-c9d0-1234-efab-345678901234"], "reason": "State change: UPDATE to the current state, keeping what it replaced."}]`,
		`"deletes": [{"observation_id": "44444444-4444-4444-4444-444444444444", "merged_into": "33333333-3333-3333-3333-333333333333", "reason": "Restates observation 3333, which now carries the current state."}]`,
		// The update's current_text field rule.
		"- `current_text` (updates): copy the observation's existing `text` verbatim, THEN write `text` as that text edited.",
		// The delete's merged_into field rule.
		"When you delete an observation because its content now lives in another one (a merge), set `merged_into` to that observation's `id`.",
	}
	for _, want := range golden {
		if !strings.Contains(systemPrompt, want) {
			t.Fatalf("the system prompt is missing the verbatim contract:\n%q", want)
		}
	}
	// The superseded Example 3 — DELETE the old, CREATE the replacement — must
	// be gone, or a model following the prompt would split merge decisions.
	if strings.Contains(systemPrompt, "A superseded observation is deleted") ||
		strings.Contains(systemPrompt, "no longer exists — this observation is superseded") {
		t.Fatal("the pre-v1.1.0 Example 3 must not survive the port")
	}
}

// A year-1 or year-9999 temporal value is an open bound, not a date: it renders
// as absent in the facts' suffix and the observations' mentioned_at, so the
// model never reads a placeholder as a timestamp.
func TestPlaceholderTemporalFieldsRenderAsAbsent(t *testing.T) {
	const forever = "0001-01-01T00:00:00Z"
	const ongoing = "9999-12-31T00:00:00Z"
	user := buildInput(
		[]Fact{{
			ID: "f1", Text: "Alice has kept the ledger since forever",
			OccurredStart: forever, OccurredEnd: ongoing, MentionedAt: "2025-06-02T00:00:00Z",
		}},
		[]Observation{{ID: "o1", Text: "the ledger exists", MentionedAt: ongoing}},
		DefaultSettings(),
		-1,
	)
	for _, phantom := range []string{"occurred_start=0001", "occurred_end=9999", "mentioned_at=9999"} {
		if strings.Contains(user, phantom) {
			t.Fatalf("the prompt must not render a placeholder: %s\nin:\n%s", phantom, user)
		}
	}
	if !strings.Contains(user, "mentioned_at=2025-06-02T00:00:00Z") {
		t.Fatalf("a known date must still render:\n%s", user)
	}
	// A fact whose every temporal field is a placeholder carries no suffix.
	if strings.Contains(user, "(occurred") {
		t.Fatalf("an all-placeholder fact must carry no temporal suffix:\n%s", user)
	}
}
