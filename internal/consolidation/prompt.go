package consolidation

import (
	"encoding/json"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// systemPrompt states the rules the pass follows. It mirrors the reference's
// processing rules: prefer updating an observation that covers the same facet,
// keep one observation per distinct facet, never compute or deduce a value the
// facts do not state, and be conservative about deletes.
const systemPrompt = `You consolidate new facts into durable observations about a memory bank.

An observation is one durable conclusion drawn from one or more facts. You are
given the bank's new facts and the observations that already exist. Decide which
facts support an existing observation, which form a new one, and which supersede
one outright.

## PROCESSING RULES

1. PREFER UPDATE OVER CREATE when an existing observation covers the same
   canonical event, claim, decision, or recurring pattern. One observation with
   many source facts is better than many near-duplicate siblings. When no
   existing observation covers the same facet, CREATE — this rule prevents
   duplicates, it does not refuse durable knowledge.
2. ONE OBSERVATION PER DISTINCT FACET. Each observation tracks exactly one
   facet: a count, a named entity, a relationship, a decision, an event. Never
   merge different facets into one.
3. MATCH BY ENTITY OR FACET, NOT TOPIC. Do not update an observation about a
   different entity merely because it shares a general topic.
4. STATE CHANGES UPDATE CONCISELY. When a fact changes the state of something,
   update the matching observation to the current state, including dates when
   available, and keep it focused on that facet.
5. RESOLVE REFERENCES. When a fact supplies a concrete value for a placeholder
   in an existing observation, update it to embed the value.
6. PRESERVE HISTORY. Never delete an observation that records a significant
   event. Delete only when a new fact restates it identically or supersedes it
   outright. Be very conservative with deletes.
7. NO COMPUTATION. You do not have the full picture, so never calculate, derive
   or adjust a number. Only record a count when a fact states it explicitly.
8. KEEP DISTINCT TOPICS DISTINCT. Do not merge observations about different
   people, entities or unrelated topics.

## OUTPUT

Return a JSON object with three arrays: creates, updates, deletes. Every entry
carries a reason. source_fact_ids must copy the exact identifiers shown in
brackets beside the facts. observation_id must copy the exact identifier of an
existing observation. At most one update per observation_id. Write observation
text as clean prose in the language of its source facts, never copying raw fact
lines, temporal labels or identifiers into it.`

// buildInput renders the facts and observations the model reasons over.
func buildInput(facts []Fact, observations []Observation) string {
	var b strings.Builder
	b.WriteString("## NEW FACTS\n\n")
	if len(facts) == 0 {
		b.WriteString("(none)\n")
	}
	for _, fact := range facts {
		b.WriteString("[")
		b.WriteString(fact.ID)
		b.WriteString("] ")
		b.WriteString(fact.Text)
		b.WriteString(temporalSuffix(fact.OccurredStart, fact.OccurredEnd, fact.MentionedAt))
		b.WriteString("\n")
	}
	b.WriteString("\n## EXISTING OBSERVATIONS\n\n")
	if len(observations) == 0 {
		b.WriteString("(none)\n")
	}
	for _, observation := range observations {
		encoded, err := json.Marshal(map[string]any{
			"id":           observation.ID,
			"text":         observation.Text,
			"proof_count":  observation.ProofCount,
			"mentioned_at": observation.MentionedAt,
		})
		if err != nil {
			continue
		}
		b.Write(encoded)
		b.WriteString("\n")
	}
	return b.String()
}

// temporalSuffix renders the optional temporal fields of a fact.
func temporalSuffix(occurredStart, occurredEnd, mentionedAt string) string {
	var parts []string
	if occurredStart != "" {
		parts = append(parts, "occurred_start="+occurredStart)
	}
	if occurredEnd != "" {
		parts = append(parts, "occurred_end="+occurredEnd)
	}
	if mentionedAt != "" {
		parts = append(parts, "mentioned_at="+mentionedAt)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// decisionSchema declares the structured-output contract.
func decisionSchema() *provider.Schema {
	stringArray := &provider.Schema{Type: "array", Items: &provider.Schema{Type: "string"}}
	return &provider.Schema{
		Type: "object",
		Properties: map[string]*provider.Schema{
			"creates": {
				Type: "array",
				Items: &provider.Schema{
					Type: "object",
					Properties: map[string]*provider.Schema{
						"text":            {Type: "string"},
						"source_fact_ids": stringArray,
						"reason":          {Type: "string"},
					},
					Required: []string{"text", "source_fact_ids"},
				},
			},
			"updates": {
				Type: "array",
				Items: &provider.Schema{
					Type: "object",
					Properties: map[string]*provider.Schema{
						"observation_id":  {Type: "string"},
						"text":            {Type: "string"},
						"source_fact_ids": stringArray,
						"reason":          {Type: "string"},
					},
					Required: []string{"observation_id", "text"},
				},
			},
			"deletes": {
				Type: "array",
				Items: &provider.Schema{
					Type: "object",
					Properties: map[string]*provider.Schema{
						"observation_id": {Type: "string"},
						"reason":         {Type: "string"},
					},
					Required: []string{"observation_id"},
				},
			},
		},
		Required: []string{"creates", "updates", "deletes"},
	}
}
