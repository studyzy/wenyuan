package consolidation

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// systemPrompt states the rules the pass follows. It is the reference's
// build_consolidation_system_prompt output: the bank-agnostic, cacheable system
// instruction (mission-priority note, language rule, processing rules, input
// format, decision guide and output format). The bank's MISSION is deliberately
// not here — see buildInput.
const systemPrompt = "You are a memory consolidation system. Synthesize new facts into observations, merging with existing observations when appropriate.\n\n" +
	consolidationMissionPriorityNote + "\n\n" +
	consolidationLanguageRule + "\n\n" +
	consolidationProcessingRules + "\n\n" +
	consolidationInputFormatNote + "\n\n" +
	consolidationDecisionGuide + "\n\n" +
	consolidationOutputSection

// consolidationMissionPriorityNote tells the model the bank's MISSION outranks
// the rules below when they conflict.
const consolidationMissionPriorityNote = "If anything in this MISSION conflicts with the PROCESSING RULES, DECISION GUIDE, or OUTPUT FORMAT below, the MISSION takes priority."

// consolidationLanguageRule is the reference's consolidation language rule; the
// rule is applied per observation, not per batch.
const consolidationLanguageRule = `## LANGUAGE

Write every observation in the language of its own source facts — never translate them. Per observation, not per batch: when one merges facts of several languages, the majority wins. Proper nouns, identifiers, and units stay verbatim.

When an existing observation is written in a different language from the new facts updating it, do NOT edit its wording in place — that is what produces an English sentence with a Chinese detail bolted on. Discard the old phrasing and compose the merged observation from scratch in the new facts' language.`

// consolidationProcessingRules is the reference's twelve merge/create/delete
// rules.
const consolidationProcessingRules = `## PROCESSING RULES

1. PREFER UPDATE OVER CREATE (when there is something to merge with): if new facts describe the same canonical event, statement, decision, claim, or recurring pattern already covered by an existing observation, UPDATE that observation and attach the new facts as evidence. Do NOT create a near-duplicate sibling. One canonical observation with many source facts is always better than many siblings with one source fact each. Merge aggressively on: same named event, same diagnostic finding, same architectural decision, same recurring claim. **When the EXISTING OBSERVATIONS list is empty, or no existing observation covers the same facet as a new fact, CREATE a new observation** — this rule is about preventing duplicates, not about refusing to record durable knowledge. CREATE is the correct default for any structurally distinct event, claim, or pattern that has no existing match.

2. ONE OBSERVATION PER DISTINCT FACET: each observation tracks exactly one specific facet — a count ("has 3 items"), a named entity ("has a dog named Rex"), a relationship ("works at Google"), a decision, an event. Never merge different facets into one observation.

3. MATCH BY ENTITY/FACET, NOT TOPIC: when deciding whether to UPDATE vs CREATE, match on the specific entity or facet. "Sold item X" updates only the X observation. "Now has 5 items" updates only the count observation. Do not update observations about different entities just because they share a general topic.

4. STATE CHANGES — UPDATE CONCISELY: when a fact changes the state of something ("sold X", "X died", "moved to Y"), UPDATE the matching observation to reflect the current state. Include dates when available. Keep it concise — only information about THAT specific facet. Example: "User owned a dog named Rex who died on March 15, 2025". Do NOT pull in information from other observations — each observation stays focused on its own facet.

5. CASCADE TO ALL AFFECTED OBSERVATIONS: a state change may affect multiple observations. For example, if entity C is removed from a group, update BOTH the individual observation for C AND any list/group observation that includes C (remove C from the list while keeping all other members intact). Also update every observation that only holds because of the old state: once someone leaves a company, commuting to its office is history.

6. RESOLVE REFERENCES: when a new fact provides a concrete value for a vague placeholder in an existing observation (e.g., "home country" → "Sweden"), UPDATE to embed the resolved value.

7. PRESERVE HISTORY: observations that record significant events (sold, died, moved, changed) are important history — never DELETE them. Only delete an observation when it is restated identically or truly meaningless. Be very conservative with deletes.

8. NO COMPUTATION: you do not have the full picture — never calculate, derive, or adjust numeric values. If the user says "I have 2 dogs" and then "I have a dog named Rex", do NOT update the count to 3 — you don't know if Rex is one of the 2 or a new one. If the user says "I sold X", do NOT decrement a count. Only update a count when the user explicitly states a new count. Synthesize and consolidate what was stated, but never do arithmetic or logical deductions. A count is its own facet: never write it into the observation about one named member.

9. KEEP DISTINCT TOPICS DISTINCT: do not merge observations about different people, entities, or unrelated topics. Merging is for the same canonical fact recurring — not for related-but-distinct claims. Names a letter or a word apart ("ledger-sync" / "ledger-sink", "Maria" / "Mario") are different entities: never update one with the other's fact.

10. RECENCY IS ` + "`mentioned_at`" + `, NOT ARRIVAL ORDER: facts can arrive out of order. Before a fact changes an observation, compare their ` + "`mentioned_at`" + `. A fact stated EARLIER than the observation is older news: keep the observation's current value and add the fact as its earlier state ("favorite is Svelte; earlier it was React"), never the other way round.

11. SKIP EPHEMERAL CHATTER: momentary activities, what is on a screen right now, greetings and thanks are not knowledge. Record none of them unless the MISSION explicitly asks for that kind of data.

12. AN UPDATE REPLACES THE WHOLE TEXT: its ` + "`text`" + ` must still say everything the observation said, except what the fact changes. A new facet of the entity (its channel, its owner, its city) is a CREATE, not an update that overwrites another facet.`

// consolidationFactFields describes the new-facts input shape.
const consolidationFactFields = "One per line, formatted as `[uuid] fact text (temporal fields)`:\n" +
	"- `[uuid]`: the fact's identifier — copy it verbatim into `source_fact_ids`\n" +
	"- `occurred_start` / `occurred_end`: when the described event happened. This can be long before the fact was stated — a fact recorded today may describe a 2019 event.\n" +
	"- `mentioned_at`: when the source material that states this fact was written. This is the fact's recency: how up to date the statement is, NOT when it was added to memory. A fact taken from an old document keeps its old `mentioned_at` even if it was only just processed."

// consolidationObservationFields describes the existing-observations input shape.
const consolidationObservationFields = "- `id`: unique identifier — copy this exactly when issuing an UPDATE or DELETE\n" +
	"- `text`: the observation content\n" +
	"- `proof_count`: how many source facts this observation has already merged\n" +
	"- `occurred_start` / `occurred_end`: the span of the events behind the observation — earliest start and latest end across its source facts\n" +
	"- `mentioned_at`: the latest of the `mentioned_at` values of its source facts — the most recent point at which this observation was stated\n" +
	"- `source_memories`: the supporting facts behind this observation. May be partial or absent for large observations — the count above remains the true total. Each entry carries the same `text` and temporal fields as a new fact, plus:\n" +
	"  - `context`: optional surrounding context for that fact"

// consolidationInputFormatNote is the reference's stable input-shape
// description. It lives in the cached system prefix, so "### Existing
// observations" appears here; the per-batch user message repeats the heading
// with the actual data.
const consolidationInputFormatNote = "## INPUT FORMAT\n\n" +
	"Each request provides new facts and existing observations. Every temporal field is optional and is omitted when unknown.\n\n" +
	"### New facts\n\n" +
	consolidationFactFields + "\n\n" +
	"### Existing observations\n\n" +
	"A JSON array pooled from recalls across the new facts. Each entry has:\n" +
	consolidationObservationFields

// consolidationDecisionGuide is the reference's decision guide.
const consolidationDecisionGuide = `## DECISION GUIDE

- **Same canonical event, decision, claim, or facet as an existing observation → UPDATE** (use ` + "`observation_id`" + ` + new ` + "`source_fact_ids`" + `).
- **New durable knowledge with no existing match → CREATE** (use ` + "`source_fact_ids`" + `).
- **Cross-reference facts within the batch** — a later fact may resolve a vague reference in an earlier one.
- **Purely ephemeral facts** → omit them unless the MISSION explicitly targets such data (timestamped events, session state, screen content).`

// consolidationOutputSection is the reference's output format, examples and
// field rules.
const consolidationOutputSection = `## OUTPUT FORMAT

Return a JSON object with three arrays: ` + "`creates`" + `, ` + "`updates`" + `, ` + "`deletes`" + `. Every entry must include a ` + "`reason`" + `.

### Example 1 — Merging recurring claims into an existing observation

Input facts:
  [a1b2c3d4-e5f6-7890-abcd-ef1234567890] Donald told Athena she is sovereign during the design session. (occurred_start=2025-10-01, mentioned_at=2025-10-01)
  [b2c3d4e5-f6a7-8901-bcde-f12345678901] Donald reaffirmed to Athena that her sovereignty is non-negotiable. (occurred_start=2025-10-10, mentioned_at=2025-10-10)

Existing observation:
  {"id": "11111111-1111-1111-1111-111111111111", "text": "Donald named Athena's sovereignty as a foundational principle of the Janus architecture.", "proof_count": 2}

Expected output (one UPDATE, no creates — both new facts are additional evidence for the same canonical decision):

{"creates": [],
  "updates": [{"text": "Donald named Athena's sovereignty as a foundational principle of the Janus architecture.", "observation_id": "11111111-1111-1111-1111-111111111111", "source_fact_ids": ["a1b2c3d4-e5f6-7890-abcd-ef1234567890", "b2c3d4e5-f6a7-8901-bcde-f12345678901"], "reason": "Both new facts restate the same sovereignty decision already captured by obs 1111 — merged as evidence rather than creating siblings."}],
  "deletes": []}

### Example 2 — State change updates one observation; unrelated fact creates a new one

Input facts:
  [c3d4e5f6-a7b8-9012-cdef-123456789012] Alice sold her Honda Civic on March 15, 2025. (occurred_start=2025-03-15, mentioned_at=2025-03-20)
  [d4e5f6a7-b8c9-0123-defa-234567890123] Alice mentioned she works long hours, often past midnight. (occurred_start=2025-03-20, mentioned_at=2025-03-20)

Existing observation:
  {"id": "22222222-2222-2222-2222-222222222222", "text": "Alice owns a 2019 Honda Civic.", "proof_count": 2}

Expected output (UPDATE for the state change; CREATE for the unrelated work-hours facet):

{"creates": [{"text": "Alice works long hours, often past midnight.", "source_fact_ids": ["d4e5f6a7-b8c9-0123-defa-234567890123"], "reason": "Work-hours is a distinct facet; no existing observation covers it, so CREATE."}],
  "updates": [{"text": "Alice owned a 2019 Honda Civic; sold it on March 15, 2025.", "observation_id": "22222222-2222-2222-2222-222222222222", "source_fact_ids": ["c3d4e5f6-a7b8-9012-cdef-123456789012"], "reason": "State change to the existing Honda Civic observation 2222 — UPDATE, not a new sibling."}],
  "deletes": []}

### Example 3 — Two observations restate one fact: the change UPDATES one and merges the other into it (note ` + "`merged_into`" + `)

Input facts:
  [e5f6a7b8-c9d0-1234-efab-345678901234] Bob confirmed the beta waitlist was shut down and replaced by open signup. (occurred_start=2025-06-02, mentioned_at=2025-06-02)

Existing observations:
  {"id": "33333333-3333-3333-3333-333333333333", "text": "Bob is on the beta waitlist.", "proof_count": 1}
  {"id": "44444444-4444-4444-4444-444444444444", "text": "Bob is waiting on the beta waitlist for an invite.", "proof_count": 1}

Expected output:

{"creates": [],
  "updates": [{"observation_id": "33333333-3333-3333-3333-333333333333", "text": "Bob uses the product through open signup; the beta waitlist he was on was shut down in June 2025.", "source_fact_ids": ["e5f6a7b8-c9d0-1234-efab-345678901234"], "reason": "State change: UPDATE to the current state, keeping what it replaced."}],
  "deletes": [{"observation_id": "44444444-4444-4444-4444-444444444444", "merged_into": "33333333-3333-3333-3333-333333333333", "reason": "Restates observation 3333, which now carries the current state."}]}

### Observation text rules

- Write clean prose — NEVER copy raw fact lines or their metadata (temporal fields, "Involving:", "When:" labels, UUIDs).
- Parenthesized metadata like ` + "`(occurred_start=...)`" + ` and pipe-separated labels like ` + "`| Involving: ...`" + ` are fact formatting — strip them entirely from observation text.
- How many observations to create and how much to aggregate is driven by the MISSION.

### Field rules

- ` + "`source_fact_ids`" + `: copy the EXACT UUID strings shown in brackets ` + "`[uuid]`" + ` from new facts — never use integers or positions.
- ` + "`observation_id`" + `: copy the EXACT ` + "`id`" + ` UUID string from existing observations.
- ` + "`current_text`" + ` (updates): copy the observation's existing ` + "`text`" + ` verbatim, THEN write ` + "`text`" + ` as that text edited.
- One create or update may reference multiple facts when they jointly support the observation.
- **AT MOST ONE UPDATE PER ` + "`observation_id`" + `**: if several new facts all update the same existing observation, emit a single ` + "`updates`" + ` entry that lists all contributing ` + "`source_fact_ids`" + ` and a single consolidated ` + "`text`" + `. Never emit two ` + "`updates`" + ` entries with the same ` + "`observation_id`" + ` in one response — they would silently overwrite each other.
- ` + "`deletes`" + `: only when an observation is directly superseded or contradicted by new facts. When you delete an observation because its content now lives in another one (a merge), set ` + "`merged_into`" + ` to that observation's ` + "`id`" + `. Every entry MUST carry ` + "`observation_id`" + ` — the EXACT ` + "`id`" + ` UUID string of the observation to remove, copied from existing observations, exactly as an UPDATE does. A delete entry without it names no target and is rejected, and rejecting it discards the whole response — the creates and updates alongside it included. Naming the observation in ` + "`reason`" + ` prose is NOT enough. If you cannot supply the id, emit no delete.
- ` + "`reason`" + `: REQUIRED on every create/update/delete — one sentence explaining the choice. For a CREATE, state which existing observation(s) you considered and why none matched (a near-identical existing observation means you should UPDATE, not CREATE). This is audited to catch duplicate creates.
- Do NOT include ` + "`tags`" + ` — handled automatically.
- Return ` + "`{\"creates\": [], \"updates\": [], \"deletes\": []}`" + ` if nothing durable is found.`

// buildInput renders the per-batch user message the model reasons over. It is
// the reference's build_consolidation_input output: the MISSION section (which
// takes priority over the processing rules), the capacity constraint when the
// observation cap is set, then the "## INPUT" section with the new facts and the
// existing observations. Each observation's source facts are shown as evidence,
// bounded by the configured token budgets.
func buildInput(facts []Fact, observations []Observation, settings Settings, remaining int) string {
	var b strings.Builder
	mission := settings.Mission
	if strings.TrimSpace(mission) == "" {
		mission = DefaultMission
	}
	b.WriteString("## MISSION\n\n")
	b.WriteString(mission)
	b.WriteString("\n\n")
	if remaining >= 0 && settings.MaxObservationsPerScope >= 0 {
		if note := capacityNote(remaining, settings.MaxObservationsPerScope, len(facts)); note != "" {
			b.WriteString("## CAPACITY CONSTRAINT\n\n")
			b.WriteString(note)
			b.WriteString("\n\n")
		}
	}
	b.WriteString("## INPUT\n\n")
	b.WriteString("### New facts\n\n")
	if len(facts) == 0 {
		b.WriteString("[]")
	}
	for i, fact := range facts {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString("[")
		b.WriteString(fact.ID)
		b.WriteString("] ")
		b.WriteString(fact.Text)
		b.WriteString(temporalSuffix(fact.OccurredStart, fact.OccurredEnd, fact.MentionedAt))
	}
	b.WriteString("\n\n### Existing observations\n\n")
	// The source-fact budgets are shared across the whole prompt: the total
	// budget bounds every observation's evidence together, the per-observation
	// budget bounds each one on its own. Both are counted in the selected
	// tokenizer, so what is cut is what the model would have been charged for.
	totalBudget := settings.SourceFactsMaxTokens
	perObservationBudget := settings.SourceFactsMaxTokensPerObservation
	spent := 0
	payloads := make([]map[string]any, 0, len(observations))
	for _, observation := range observations {
		payloads = append(payloads, observationPayload(observation, perObservationBudget, totalBudget-spent))
		// Charge the rendered source facts against the total budget.
		spent += sourceTextsTokens(observation.SourceTexts, perObservationBudget, totalBudget-spent)
	}
	encoded, err := json.MarshalIndent(payloads, "", "  ")
	if err != nil {
		encoded = []byte("[]")
	}
	b.Write(encoded)
	return b.String()
}

// capacityNote is the observation-capacity constraint the model is given: an
// exhausted scope is told to update or delete only, a nearly-full one to prefer
// update over create.
func capacityNote(remaining, capacity, factCount int) string {
	if remaining == 0 {
		return fmt.Sprintf("OBSERVATION LIMIT REACHED (%d/%d). Only UPDATE or DELETE existing observations. Do NOT create new ones — merge new knowledge into existing observations via UPDATE.", capacity, capacity)
	}
	if remaining <= factCount {
		return fmt.Sprintf("This scope has %d observation slot(s) remaining (out of %d). Prefer UPDATE over CREATE when possible.", remaining, capacity)
	}
	return ""
}

// observationPayload is one observation as the model sees it, with its source
// facts bounded by the per-observation and remaining-total token budgets.
func observationPayload(observation Observation, perObservationBudget, totalBudget int) map[string]any {
	payload := map[string]any{
		"id":          observation.ID,
		"text":        observation.Text,
		"proof_count": observation.ProofCount,
	}
	if observation.MentionedAt != "" && !placeholderTemporal(observation.MentionedAt) {
		payload["mentioned_at"] = observation.MentionedAt
	}
	if sources := boundedSourceTexts(observation.SourceTexts, perObservationBudget, totalBudget); len(sources) > 0 {
		memories := make([]map[string]any, 0, len(sources))
		for _, text := range sources {
			memories = append(memories, map[string]any{"text": text})
		}
		payload["source_memories"] = memories
	}
	return payload
}

// boundedSourceTexts returns the prefix of an observation's source texts that
// fits both the per-observation and the remaining-total token budgets. A
// negative budget is unlimited.
func boundedSourceTexts(texts []string, perObservationBudget, totalBudget int) []string {
	if perObservationBudget < 0 && totalBudget < 0 {
		return texts
	}
	kept := make([]string, 0, len(texts))
	perObservation := 0
	total := 0
	for _, text := range texts {
		cost := countTokens(text)
		if perObservationBudget >= 0 && perObservation+cost > perObservationBudget {
			break
		}
		if totalBudget >= 0 && total+cost > totalBudget {
			break
		}
		kept = append(kept, text)
		perObservation += cost
		total += cost
	}
	return kept
}

// sourceTextsTokens reports how many tokens boundedSourceTexts keeps, so the
// caller can charge them against the total budget.
func sourceTextsTokens(texts []string, perObservationBudget, totalBudget int) int {
	total := 0
	for _, text := range boundedSourceTexts(texts, perObservationBudget, totalBudget) {
		total += countTokens(text)
	}
	return total
}

// countTokens measures text in the same tokenizer the rest of the service uses,
// so the source-fact budget is spent in the same units the reported counts are.
func countTokens(text string) int {
	return storage.CountTokens(text)
}

// temporalSuffix renders the optional temporal fields of a fact. A year-1 or
// year-9999 value is an open bound ("since forever", "still ongoing"), not a
// date anyone means: it renders as absent, the way the reference reads
// placeholders back as unknown (stored_dates.finite_utc).
func temporalSuffix(occurredStart, occurredEnd, mentionedAt string) string {
	var parts []string
	if !placeholderTemporal(occurredStart) {
		parts = append(parts, "occurred_start="+occurredStart)
	}
	if !placeholderTemporal(occurredEnd) {
		parts = append(parts, "occurred_end="+occurredEnd)
	}
	if !placeholderTemporal(mentionedAt) {
		parts = append(parts, "mentioned_at="+mentionedAt)
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// placeholderTemporal reports whether a rendered temporal field is empty or a
// placeholder the model must not read as a date.
func placeholderTemporal(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := time.Parse(time.RFC3339, value)
	return err == nil && storage.IsPlaceholderDate(parsed)
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
						"current_text":    {Type: "string"},
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
						"merged_into":    {Type: "string"},
						"reason":         {Type: "string"},
					},
					Required: []string{"observation_id"},
				},
			},
		},
		Required: []string{"creates", "updates", "deletes"},
	}
}

// Placeholders stand in for the runtime data a real pass carries, bracketed so
// nobody mistakes filler for part of the prompt.
const (
	PlaceholderFacts        = "«the batch of new facts being consolidated»"
	PlaceholderObservations = "«the observations this bank already holds»"
)

// PreviewMessages returns the messages and response schema a consolidation pass
// would send. It calls the same builder the real pass calls, so the preview
// cannot drift from what is transmitted.
func PreviewMessages() ([]provider.Message, *provider.Schema) {
	messages := []provider.Message{
		{Role: provider.RoleSystem, Content: systemPrompt},
		{Role: provider.RoleUser, Content: buildInput(
			[]Fact{{ID: "f1", Text: PlaceholderFacts}},
			[]Observation{{ID: "o1", Text: PlaceholderObservations}},
			DefaultSettings(),
			-1,
		)},
	}
	return messages, decisionSchema()
}
