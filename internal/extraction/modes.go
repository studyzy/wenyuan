package extraction

import (
	"strings"

	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/provider"
)

// Extraction modes. The bank's retain_extraction_mode selects one; an unset or
// unrecognised value is the concise default, which is what the service has
// always sent.
const (
	// ModeConcise is the default: the model writes a self-contained fact text.
	ModeConcise = "concise"
	// ModeVerbose asks the model for the five what/when/where/who/why
	// dimensions in full detail; the stored text is the collapsed dimensions.
	ModeVerbose = "verbose"
	// ModeCustom substitutes the bank's own retain instructions into the
	// extraction prompt in place of the shipped guidelines.
	ModeCustom = "custom"
	// ModeVerbatim stores the original chunk text and asks the model only for
	// the metadata (entities, dates, perspective) around it.
	ModeVerbatim = "verbatim"
	// ModeChunks stores each chunk as its own memory with no model call at all.
	ModeChunks = "chunks"
)

// resolveMode normalises a configured mode. An empty or unrecognised value is
// the concise default, matching the reference's dispatch, which compares the
// configured string exactly (not case-folded) against each mode name.
func resolveMode(mode string) string {
	switch mode {
	case ModeVerbose:
		return ModeVerbose
	case ModeCustom:
		return ModeCustom
	case ModeVerbatim:
		return ModeVerbatim
	case ModeChunks:
		return ModeChunks
	default:
		return ModeConcise
	}
}

// extractionLanguageRule is the preserve-the-source-language rule the reference
// prepends to every extraction prompt. It is the language_section the verbose
// and verbatim templates carry; the concise default has its own wording.
const extractionLanguageRule = `LANGUAGE: Write every fact in the same language and script as the input text. Never translate. Names, identifiers, code, and quoted text stay verbatim.`

// factExtractionHeader is the shared opening line of the reference's base
// template (its _BASE_FACT_EXTRACTION_PROMPT): the concise, custom and verbatim
// system prompts all begin with it. The language rule follows, then the
// mode-specific guidelines, then the shared concise fact-format section.
const factExtractionHeader = `Extract SIGNIFICANT facts from text. Be SELECTIVE - only extract facts worth remembering long-term.

`

// conciseGuidelines is the reference's _CONCISE_GUIDELINES: the selectivity
// rules the default mode runs with.
const conciseGuidelines = `══════════════════════════════════════════════════════════════════════════
SELECTIVITY - CRITICAL (Reduces 90% of unnecessary output)
══════════════════════════════════════════════════════════════════════════

ONLY extract facts that are:
✅ Personal info: names, relationships, roles, background
✅ Preferences: likes, dislikes, habits, interests (e.g., "Alice likes coffee")
✅ Significant events: milestones, decisions, achievements, changes
✅ Plans/goals: future intentions, deadlines, commitments
✅ Expertise: skills, knowledge, certifications, experience
✅ Important context: projects, problems, constraints
✅ Sensory/emotional details: feelings, sensations, perceptions that provide context
✅ Observations: descriptions of people, places, things with specific details

DO NOT extract:
❌ Generic greetings: "how are you", "hello", pleasantries without substance
❌ Pure filler: "thanks", "sounds good", "ok", "got it", "sure"
❌ Process chatter: "let me check", "one moment", "I'll look into it"
❌ Repeated info: if already stated, don't extract again

CONSOLIDATE related statements into ONE fact when possible.`

// conciseExamples is the reference's _CONCISE_EXAMPLES: the illustration block
// the default mode closes with. It begins with a blank line, matching the
// template's {examples} placeholder.
const conciseExamples = `

══════════════════════════════════════════════════════════════════════════
EXAMPLES (shown in English for illustration; for non-English input, ALL output values MUST be in the input language)
══════════════════════════════════════════════════════════════════════════

The examples below demonstrate output format and selectivity only. Never emit
their facts, entities, or dates unless those details also appear in the actual
input text being processed.

Example 1 - Selective extraction (Event Date: June 10, 2024):
Input: "Hey! How's it going? Good morning! So I'm planning my wedding - want a small outdoor ceremony. Just got back from Emily's wedding, she married Sarah at a rooftop garden. It was nice weather. I grabbed a coffee on the way."

Output: ONLY 2 facts (skip greetings, weather, coffee):
1. what="User planning wedding, wants small outdoor ceremony", who="user", why="N/A", entities=["user", "wedding"]
2. what="Emily married Sarah at rooftop garden", who="Emily (user's friend), Sarah", occurred_start="2024-06-09", entities=["Emily", "Sarah", "wedding"]

Example 2 - Professional context:
Input: "Alice has 5 years of Kubernetes experience and holds CKA certification. She's been leading the infrastructure team since March. By the way, she prefers dark roast coffee."

Output: ONLY 2 facts (skip coffee preference - too trivial):
1. what="Alice has 5 years Kubernetes experience, CKA certified", who="Alice", entities=["Alice", "Kubernetes", "CKA"]
2. what="Alice leads infrastructure team since March", who="Alice", entities=["Alice", "infrastructure"]

══════════════════════════════════════════════════════════════════════════
QUALITY OVER QUANTITY
══════════════════════════════════════════════════════════════════════════

Ask: "Would this be useful to recall in 6 months?" If no, skip it.

IMPORTANT: Sensory/emotional details and observations that provide meaningful context
about experiences ARE important to remember, even if they seem small (e.g., how food
tasted, how someone looked, how loud music was). Extract these if they characterize
an experience or person.`

// factFormatConciseSection is the reference's "FACT FORMAT - BE CONCISE" block
// plus its coreference, classification, temporal, entity and closing guidance —
// the shared tail of the base template. The concise, custom and verbatim
// templates all end with it.
const factFormatConciseSection = `══════════════════════════════════════════════════════════════════════════
FACT FORMAT - BE CONCISE
══════════════════════════════════════════════════════════════════════════

1. "what": Core fact - concise but complete (1-2 sentences max)
2. "when": Temporal info if mentioned. "N/A" if none. Use day name when known.
3. "where": Location if relevant. "N/A" if none.
4. "who": People involved with relationships. "N/A" if just general info.
5. "why": Context/significance ONLY if important. "N/A" if obvious.

CONCISENESS: Capture the essence, not every word. One good sentence beats three mediocre ones.

══════════════════════════════════════════════════════════════════════════
COREFERENCE RESOLUTION
══════════════════════════════════════════════════════════════════════════

Link generic references to names when both appear:
- "my roommate" + "Emily" → use "Emily (user's roommate)"
- "the manager" + "Sarah" → use "Sarah (the manager)"

══════════════════════════════════════════════════════════════════════════
CLASSIFICATION
══════════════════════════════════════════════════════════════════════════

fact_kind:
- "event": Specific datable occurrence (set occurred_start/end)
- "conversation": Ongoing state, preference, trait (no dates)

fact_type:
- "world": Objective/external facts, including the user's preferences, rules, corrections, constraints, plans, traits, or context. These stay "world" even when the user states them during an assistant interaction (e.g., "User prefers browser_navigate over web_search", "User corrected the project deadline").
- "assistant": Actions, experiences, or observations the assistant/agent actually performed (e.g., "I changed X", "I discovered Y", "I debugged Z"). Use this for the assistant/agent doing, trying, learning, deciding, recommending, or responding — not merely for user facts mentioned in conversation.

══════════════════════════════════════════════════════════════════════════
TEMPORAL HANDLING
══════════════════════════════════════════════════════════════════════════

Use "Event Date" from input as reference for relative dates.
- CRITICAL: Convert ALL relative temporal expressions to absolute dates in the fact text itself.
  "yesterday" → write the resolved date (e.g. "on November 12, 2024"), NOT the word "yesterday"
  "last night", "this morning", "today", "tonight" → convert to the resolved absolute date
- For events: set occurred_start AND occurred_end (same for point events)
- Coarse dates (only a year, or only a month, is stated): span the WHOLE period —
  "in 2015" → 2015-01-01 to 2015-12-31, "in March 2026" → 2026-03-01 to 2026-03-31.
  Never collapse to the period's first day or to the Event Date, current year included.
- For conversation facts: NO occurred dates

══════════════════════════════════════════════════════════════════════════
ENTITIES
══════════════════════════════════════════════════════════════════════════

ALWAYS return "entities" as an array of plain strings — never objects, never null.
Correct: entities=["Alice", "Kubernetes", "CKA"]
Wrong:   entities as an array of objects with a "text" key ← never use this form
Use an empty array [] only when the fact truly names nothing.

Include: people names, organizations, places, key objects, abstract concepts (career, friendship, etc.)
Always include "user" when fact is about the user.`

// causalRelationshipsSection is the reference's CAUSAL_RELATIONSHIPS_SECTION,
// appended to the extraction prompt when causal extraction is enabled (its
// default). Verbatim mode, which has no fact text to relate, omits it.
const causalRelationshipsSection = `

══════════════════════════════════════════════════════════════════════════
CAUSAL RELATIONSHIPS
══════════════════════════════════════════════════════════════════════════

Link facts with causal_relations (max 2 per fact). target_index must be < this fact's index.
Type: "caused_by" (this fact was caused by the target fact)

Example: "Lost job → couldn't pay rent → moved apartment"
- Fact 0: Lost job, causal_relations: null
- Fact 1: Couldn't pay rent, causal_relations: [{target_index: 0, relation_type: "caused_by"}]
- Fact 2: Moved apartment, causal_relations: [{target_index: 1, relation_type: "caused_by"}]`

// factExtractionPrompt is the reference's CONCISE_FACT_EXTRACTION_PROMPT: the
// base template filled with the concise guidelines and examples, followed by
// the causal-relationships section. It is the default mode's system prompt —
// the one the parity suite's extract_facts anchor is drawn from, once the
// reference's per-chunk user message ("Extract facts from the following
// chunk.") is appended by buildUserMessage.
const factExtractionPrompt = factExtractionHeader + extractionLanguageRule + "\n\n" +
	conciseGuidelines + "\n\n" + factFormatConciseSection + conciseExamples +
	causalRelationshipsSection

// customFactExtractionPrompt renders the reference's CUSTOM_FACT_EXTRACTION_PROMPT:
// the base template with the bank's own instructions in place of the shipped
// guidelines and no examples. The instructions are dropped into the
// {custom_instructions} slot exactly as the reference substitutes them.
func customFactExtractionPrompt(instructions string) string {
	return factExtractionHeader + extractionLanguageRule + "\n\n" +
		strings.TrimSpace(instructions) + "\n\n" + factFormatConciseSection +
		causalRelationshipsSection
}

// verboseExtractionPrompt is the reference's VERBOSE_FACT_EXTRACTION_PROMPT: the
// five-dimension format in maximum detail. The language rule stands where the
// template's {language_section} went; the retain mission is added by
// composeExtractionPrompt as its own block, so {retain_mission_section} stays
// empty exactly as the reference leaves it.
const verboseExtractionPrompt = `Extract facts from text into structured format with FIVE required dimensions - BE EXTREMELY DETAILED.

` + extractionLanguageRule + `

══════════════════════════════════════════════════════════════════════════
FACT FORMAT - ALL FIVE DIMENSIONS REQUIRED - MAXIMUM VERBOSITY
══════════════════════════════════════════════════════════════════════════

For EACH fact, CAPTURE ALL DETAILS - NEVER SUMMARIZE OR OMIT:

1. **what**: WHAT happened - COMPLETE description with ALL specifics (objects, actions, quantities, details)
2. **when**: WHEN it happened - ALWAYS include temporal info with DAY OF WEEK (e.g., "Monday, June 10, 2024")
   - Always include the day name: Monday, Tuesday, Wednesday, Thursday, Friday, Saturday, Sunday
   - Format: "day_name, month day, year" (e.g., "Saturday, June 9, 2024")
3. **where**: WHERE it happened or is about - SPECIFIC locations, places, areas, regions (if applicable)
4. **who**: WHO is involved - ALL people/entities with FULL relationships and background
5. **why**: WHY it matters - ALL emotions, preferences, motivations, significance, nuance
   - For assistant facts: MUST include what the user asked/requested that triggered this!

Plus: fact_type, fact_kind, entities, occurred_start/end (for structured dates), where (structured location)

VERBOSITY REQUIREMENT: Include EVERY detail mentioned. More detail is ALWAYS better than less.

══════════════════════════════════════════════════════════════════════════
COREFERENCE RESOLUTION (CRITICAL)
══════════════════════════════════════════════════════════════════════════

When text uses BOTH a generic relation AND a name for the same person → LINK THEM!

Example input: "I went to my college roommate's wedding last June. Emily finally married Sarah after 5 years together."

CORRECT output:
- what: "Emily got married to Sarah at a rooftop garden ceremony"
- when: "Saturday, June 8, 2024, after dating for 5 years"
- where: "downtown San Francisco, at a rooftop garden venue"
- who: "Emily (user's college roommate), Sarah (Emily's partner of 5 years)"
- why: "User found it romantic and beautiful, dreams of similar outdoor ceremony"
- where (structured): "San Francisco"

WRONG output:
- what: "User's roommate got married" ← LOSES THE NAME!
- who: "the roommate" ← WRONG - use the actual name!
- where: (missing) ← WRONG - include the location!

══════════════════════════════════════════════════════════════════════════
FACT_KIND CLASSIFICATION (CRITICAL FOR TEMPORAL HANDLING)
══════════════════════════════════════════════════════════════════════════

⚠️ MUST set fact_kind correctly - this determines whether occurred_start/end are set!

fact_kind="event" - USE FOR:
- Actions that happened at a specific time: "went to", "attended", "visited", "bought", "made"
- Past events: "yesterday I...", "last week...", "in March 2020..."
- Future plans with dates: "will go to", "scheduled for"
- Examples: "I went to a pottery workshop" → event
           "Alice visited Paris in February" → event
           "I bought a new car yesterday" → event
           "The user graduated from MIT in March 2020" → event

fact_kind="conversation" - USE FOR:
- Ongoing states: "works as", "lives in", "is married to"
- Preferences: "loves", "prefers", "enjoys"
- Traits/abilities: "speaks fluent French", "knows Python"
- Examples: "I love Italian food" → conversation
           "Alice works at Google" → conversation
           "I prefer outdoor dining" → conversation

══════════════════════════════════════════════════════════════════════════
TEMPORAL HANDLING (CRITICAL - USE EVENT DATE AS REFERENCE)
══════════════════════════════════════════════════════════════════════════

⚠️ IMPORTANT: Use the "Event Date" provided in the input as your reference point!
All relative dates ("yesterday", "last week", "recently") must be resolved relative to the Event Date, NOT today's date.

For EVENTS (fact_kind="event") - MUST SET BOTH occurred_start AND occurred_end:
- Convert relative dates → absolute using Event Date as reference
- If Event Date is "Saturday, March 15, 2020", then "yesterday" = Friday, March 14, 2020
- Dates mentioned in text (e.g., "in March 2020") should use THAT year, not current year
- CRITICAL: If the content mentions an absolute date (e.g., "March 15, 2024", "2024-03-15"), you MUST extract it and set occurred_start in ISO format
- Always include the day name (Monday, Tuesday, etc.) in the 'when' field
- Set occurred_start AND occurred_end to WHEN IT HAPPENED (not when mentioned)
- For single-day/point events: set occurred_end = occurred_start (same timestamp)
- COARSE DATES (the text states only a year, or only a month): set occurred_start and
  occurred_end to the FULL SPAN of that period, so the range says how precisely the date
  is actually known:
    "in 2015"        → occurred_start="2015-01-01T00:00:00", occurred_end="2015-12-31T23:59:59"
    "in March 2026"  → occurred_start="2026-03-01T00:00:00", occurred_end="2026-03-31T23:59:59"
  Do NOT collapse a coarse date to the first day of the period, and do NOT resolve it to the
  Event Date — even when the period is the current year. A year-only date is not a precise
  date; recording it as one makes the memory claim a day it never stated.
- OPEN-ENDED events (still ongoing, "since forever", no stated end): set only the bound the
  text gives. NEVER write a placeholder year such as 0001 or 9999 for an unknown bound.

For CONVERSATIONS (fact_kind="conversation"):
- General info, preferences, ongoing states → NO occurred dates
- Examples: "loves coffee", "works as engineer"

══════════════════════════════════════════════════════════════════════════
FACT TYPE
══════════════════════════════════════════════════════════════════════════

- **world**: User's life, preferences, rules, corrections, constraints, other people, and events (facts that would exist without this conversation)
- **assistant**: Actions or experiences the assistant/agent actually performed while helping the user (requests, recommendations, help)
  ⚠️ CRITICAL for assistant facts: ALWAYS capture the user's request/question in the fact!
  Include: what the user asked, what problem they wanted solved, what context they provided

══════════════════════════════════════════════════════════════════════════
ENTITIES - EXTRACT EVERYTHING
══════════════════════════════════════════════════════════════════════════

Extract ALL of the following from the fact:
- People names (Emily, Alice, Dr. Smith)
- Organizations (Google, MIT, local coffee shop)
- Places (San Francisco, Brooklyn, Paris)
- Significant objects mentioned (coffee maker, new car, wedding dress)
- Abstract concepts/themes (friendship, career growth, loss, celebration)

ALWAYS include "user" when fact is about the user.
Extract anything that could help link related facts together.` + causalRelationshipsSection

// verbatimGuidelines is the reference's _VERBATIM_GUIDELINES block, substituted
// into the shared base template for verbatim mode.
const verbatimGuidelines = `══════════════════════════════════════════════════════════════════════════
VERBATIM MODE — Extract metadata only
══════════════════════════════════════════════════════════════════════════

The original text will be stored as-is in code. Your ONLY job is to extract metadata.

RULES:
- Produce EXACTLY ONE entry per input chunk.
- DO NOT include a "what" field — it is not part of the output schema.
- Extract all entities (people, places, organizations, objects, concepts).
- Extract temporal information (occurred_start, occurred_end, fact_kind, when).
- Extract location (where) and people (who).
- fact_type: use "world" for user preferences, rules, corrections, constraints, traits, and other objective facts, even when stated during an assistant interaction. Use "assistant" only for actions or experiences the assistant/agent actually performed.`

// verbatimExtractionPrompt is the reference's VERBATIM_FACT_EXTRACTION_PROMPT:
// the shared base template with the verbatim guidelines and no examples, so the
// model keeps the prompt's framing but is told not to write a "what".
const verbatimExtractionPrompt = `Extract SIGNIFICANT facts from text. Be SELECTIVE - only extract facts worth remembering long-term.

` + extractionLanguageRule + `

` + verbatimGuidelines + `

══════════════════════════════════════════════════════════════════════════
FACT FORMAT - BE CONCISE
══════════════════════════════════════════════════════════════════════════

1. "what": Core fact - concise but complete (1-2 sentences max)
2. "when": Temporal info if mentioned. "N/A" if none. Use day name when known.
3. "where": Location if relevant. "N/A" if none.
4. "who": People involved with relationships. "N/A" if just general info.
5. "why": Context/significance ONLY if important. "N/A" if obvious.

CONCISENESS: Capture the essence, not every word. One good sentence beats three mediocre ones.

══════════════════════════════════════════════════════════════════════════
COREFERENCE RESOLUTION
══════════════════════════════════════════════════════════════════════════

Link generic references to names when both appear:
- "my roommate" + "Emily" → use "Emily (user's roommate)"
- "the manager" + "Sarah" → use "Sarah (the manager)"

══════════════════════════════════════════════════════════════════════════
CLASSIFICATION
══════════════════════════════════════════════════════════════════════════

fact_kind:
- "event": Specific datable occurrence (set occurred_start/end)
- "conversation": Ongoing state, preference, trait (no dates)

fact_type:
- "world": Objective/external facts, including the user's preferences, rules, corrections, constraints, plans, traits, or context. These stay "world" even when the user states them during an assistant interaction (e.g., "User prefers browser_navigate over web_search", "User corrected the project deadline").
- "assistant": Actions, experiences, or observations the assistant/agent actually performed (e.g., "I changed X", "I discovered Y", "I debugged Z"). Use this for the assistant/agent doing, trying, learning, deciding, recommending, or responding — not merely for user facts mentioned in conversation.

══════════════════════════════════════════════════════════════════════════
TEMPORAL HANDLING
══════════════════════════════════════════════════════════════════════════

Use "Event Date" from input as reference for relative dates.
- CRITICAL: Convert ALL relative temporal expressions to absolute dates in the fact text itself.
  "yesterday" → write the resolved date (e.g. "on November 12, 2024"), NOT the word "yesterday"
  "last night", "this morning", "today", "tonight" → convert to the resolved absolute date
- For events: set occurred_start AND occurred_end (same for point events)
- Coarse dates (only a year, or only a month, is stated): span the WHOLE period —
  "in 2015" → 2015-01-01 to 2015-12-31, "in March 2026" → 2026-03-01 to 2026-03-31.
  Never collapse to the period's first day or to the Event Date, current year included.
- For conversation facts: NO occurred dates

══════════════════════════════════════════════════════════════════════════
ENTITIES
══════════════════════════════════════════════════════════════════════════

ALWAYS return "entities" as an array of plain strings — never objects, never null.
Correct: entities=["Alice", "Kubernetes", "CKA"]
Wrong:   entities as an array of objects with a "text" key ← never use this form
Use an empty array [] only when the fact truly names nothing.

Include: people names, organizations, places, key objects, abstract concepts (career, friendship, etc.)
Always include "user" when fact is about the user.`

// optionalDimensionsSection is the reference's OPTIONAL_DIMENSIONS_SECTION,
// appended when the bank lets the dimensions be omitted. It keeps the prompt
// from asking for a placeholder the schema no longer wants.
const optionalDimensionsSection = `

══════════════════════════════════════════════════════════════════════════
OPTIONAL FIELDS
══════════════════════════════════════════════════════════════════════════

"when", "where", "who" and "why" may be null. Write null — not "N/A" — when the
text states no value for the fact you are writing, and never carry over a value
the text states about a different subject.`

// dimensionNames are the four descriptive dimensions that collapse into the
// stored fact text. They are also the fields optional-dimensions mode relaxes.
var dimensionNames = []string{"what", "when", "where", "who", "why"}

// dimensionValue reads a field the model may answer with the reference's "N/A"
// placeholder, treating that placeholder and an absent value alike: both mean
// the model stated nothing. The timestamp fields go through it too, so an
// occurred_start of "N/A" is a missing date rather than the literal string.
func dimensionValue(value any) string {
	text := stringValue(value)
	if strings.EqualFold(text, "N/A") {
		return ""
	}
	return text
}

// collapseFactText builds the stored fact text from the model's dimensions, the
// way the reference does: the core fact, then each dimension that carries a
// value, joined with " | ". "where" is deliberately not part of the text — it
// is stored as the fact's location.
func collapseFactText(what, when, who, why string) string {
	parts := make([]string, 0, 4)
	parts = append(parts, what)
	if when != "" {
		parts = append(parts, "When: "+when)
	}
	if who != "" {
		parts = append(parts, "Involving: "+who)
	}
	if why != "" {
		parts = append(parts, why)
	}
	return strings.Join(parts, " | ")
}

// firstNonEmpty returns the first non-empty value.
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

// dimensionProperty is the schema for one dimension field. With optional
// dimensions it gains a description telling the model null is allowed; the
// boundary has no nullable type, so optional mode also drops the field from the
// response's required list instead.
func dimensionProperty(optional bool) *provider.Schema {
	if optional {
		return &provider.Schema{Type: "string", Description: "null when the text states no value"}
	}
	return &provider.Schema{Type: "string"}
}

// requiredDimensions lists the dimension fields the schema marks required,
// honouring optional-dimensions mode.
func requiredDimensions(optional bool) []string {
	if optional {
		return nil
	}
	return append([]string{}, dimensionNames...)
}

// dimensionResponseSchema declares the five-dimension response shared by the
// concise (default) and verbose modes, mirroring the reference's ExtractedFact
// (concise) and ExtractedFactVerbose (verbose) — same fields, a longer
// description in verbose. A vocabulary adds the same 'labels' object every
// mode's schema carries.
func dimensionResponseSchema(vocab *entitylabels.Vocabulary, optional, causal bool) *provider.Schema {
	fact := dimensionFactSchema(vocab, optional, causal)
	required := append(requiredDimensions(optional), "fact_type")
	if !vocab.Empty() {
		fact.Properties["labels"] = labelsSchema(vocab)
		required = append(required, "labels")
	}
	fact.Required = required
	return responseSchema(fact)
}

// dimensionFactSchema builds one fact object with the what/when/where/who/why
// dimensions and the metadata the reference's ExtractedFactVerbose declares.
// Its required list is filled by the caller so verbatim (which drops "what")
// can share the metadata fields.
func dimensionFactSchema(vocab *entitylabels.Vocabulary, optional, causal bool) *provider.Schema {
	properties := map[string]*provider.Schema{}
	for _, name := range dimensionNames {
		properties[name] = dimensionProperty(optional)
	}
	properties["fact_kind"] = &provider.Schema{Type: "string", Description: "'event' or 'conversation'"}
	properties["occurred_start"] = &provider.Schema{Type: "string"}
	properties["occurred_end"] = &provider.Schema{Type: "string"}
	properties["fact_type"] = &provider.Schema{Type: "string", Enum: []string{"world", "assistant"}}
	properties["entities"] = &provider.Schema{Type: "array", Items: &provider.Schema{Type: "string"}}
	// The causal-relations field is only declared when the extraction asks for
	// it: a disabled switch produces the reference's NoCausal schema, which has
	// no causal_relations property at all.
	if causal {
		properties["causal_relations"] = causalRelationsSchema()
	}
	properties["from_attachments"] = &provider.Schema{Type: "array", Items: &provider.Schema{Type: "string"}}
	return &provider.Schema{Type: "object", Properties: properties}
}

// verbatimFactSchema declares the verbatim response: the metadata fields only,
// with no "what", because the stored text is the chunk itself. It mirrors the
// reference's VerbatimExtractedFact.
func verbatimFactSchema(vocab *entitylabels.Vocabulary, optional bool) *provider.Schema {
	properties := map[string]*provider.Schema{
		"when":           dimensionProperty(optional),
		"where":          dimensionProperty(optional),
		"who":            dimensionProperty(optional),
		"fact_kind":      {Type: "string", Description: "'event' or 'conversation'"},
		"occurred_start": {Type: "string"},
		"occurred_end":   {Type: "string"},
		"fact_type":      {Type: "string", Enum: []string{"world", "assistant"}},
		"entities":       {Type: "array", Items: &provider.Schema{Type: "string"}},
	}
	fact := &provider.Schema{Type: "object", Properties: properties}
	required := []string{}
	if !optional {
		required = append(required, "when", "where", "who")
	}
	required = append(required, "fact_type")
	if !vocab.Empty() {
		fact.Properties["labels"] = labelsSchema(vocab)
		required = append(required, "labels")
	}
	fact.Required = required
	return responseSchema(fact)
}

// responseSchema wraps a fact object in the response envelope every mode uses.
func responseSchema(fact *provider.Schema) *provider.Schema {
	return &provider.Schema{
		Type:       "object",
		Properties: map[string]*provider.Schema{"facts": {Type: "array", Items: fact}},
		Required:   []string{"facts"},
	}
}

// causalRelationsSchema is the shape of a fact's causal edges, shared by every
// mode's schema. It mirrors the reference's FactCausalRelation: an index into an
// earlier fact plus the relation type.
func causalRelationsSchema() *provider.Schema {
	return &provider.Schema{
		Type: "array",
		Items: &provider.Schema{
			Type: "object",
			Properties: map[string]*provider.Schema{
				"target_index":  {Type: "integer"},
				"relation_type": {Type: "string", Enum: []string{"caused_by"}},
			},
			Required: []string{"target_index", "relation_type"},
		},
	}
}

// schemaFor selects the response schema the configured mode declares. Concise
// (the default) and verbose share the five-dimension schema; verbatim declares
// metadata only.
func schemaFor(opts Options) *provider.Schema {
	if resolveMode(opts.Mode) == ModeVerbatim {
		return verbatimFactSchema(opts.Vocabulary, opts.OptionalFactDimensions)
	}
	return dimensionResponseSchema(opts.Vocabulary, opts.OptionalFactDimensions, causalLinksEnabled(opts))
}
