package reflect

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// The prompt text the reflection sends. It is written once, here, because the
// same wording is asserted by callers that never see the code: an operator reads
// the prompt through the preview route, and a deployment's transcript is
// compared against the service it replaced. Rebuilding any of it elsewhere would
// be a second copy to keep in step.

// defaultRole is the reasoning-loop role for a bank that sets no mission.
const defaultRole = "You are a reflection agent that answers questions by reasoning over retrieved memories."

// defaultFinalRole is the answering role for a bank that sets no mission. The
// answer is written by a separate call with its own system prompt, so it needs
// its own role.
const defaultFinalRole = "You are a thoughtful assistant that synthesizes answers from retrieved memories."

// toolSystemPreamble opens the tool-loop system prompt, ahead of everything
// else, including the directives. It is the loop's anti-hallucination rule and
// it is on every reasoning turn regardless of how a bank is configured.
const toolSystemPreamble = "CRITICAL: You MUST ONLY use information from retrieved tool results. NEVER make up names, people, events, or entities."

// toolsLanguageRule is the reasoning loop's default language rule. It is stated
// only when no output language is configured; the rule defers to a directive
// above it, and a configured language is carried by the directive itself.
const toolsLanguageRule = "## LANGUAGE RULE (default - directives take precedence)\n" +
	"- By default, detect the language of the user's question and respond in that SAME language.\n" +
	"- If the question is in Chinese, respond in Chinese. If in Japanese, respond in Japanese.\n" +
	"- IMPORTANT: The DIRECTIVES section above has HIGHER PRIORITY than this rule.\n" +
	"  If a directive specifies a language (e.g. 'Always respond in French'), follow the directive."

// finalLanguageRule is the same rule for the answering call, which has its own
// system prompt and therefore does not inherit the reasoning one.
const finalLanguageRule = "## LANGUAGE\n" +
	"- Respond in the SAME language as the user's question " +
	"(e.g. a question in Chinese gets a Chinese answer; Japanese → Japanese).\n" +
	"- If a directive above specifies a response language, follow the directive — " +
	"it takes precedence over this default."

// groundingBoundary is the line between synthesis and invention, shared by every
// path that writes an answer so they cannot drift apart: inference may
// characterise what the data covers, it may not manufacture a value for
// something the data does not cover.
const groundingBoundary = "## What Counts As Inference\n" +
	"Infer freely about what the retrieved data covers. Never produce a value (number, date, name, " +
	"status, amount) for a period, entity or person the data does not cover: extrapolating a trend, " +
	"interpolating between dated facts, or borrowing from a similar entity is invention. If no fact " +
	"states the value for the thing asked, say the data does not record it (a complete answer), then " +
	"give what IS recorded, labelled with the period or entity it belongs to. Never call a derived " +
	"value exact, reliable, deduced or confirmed; label any derivation an estimate. Qualitative " +
	"inference is unaffected."

// finalInstructions closes the answering prompt.
const finalInstructions = "Provide a thoughtful answer by synthesizing and reasoning from the retrieved data above. " +
	"You can make reasonable inferences from the memories, but don't completely fabricate information. " +
	"If the exact answer isn't stated, use what IS stated to give the best possible answer, " +
	"within the inference rules in the system prompt.\n\n" +
	"IMPORTANT: Output ONLY the final answer. Do NOT include meta-commentary like " +
	"\"I'll search...\" or \"Let me analyze...\". Do NOT explain your reasoning process. " +
	"Just provide the direct synthesized answer."

// finalSystemPromptBase is the answering call's system prompt template. It keeps
// the tool loop's preamble: the loop's turns and the answering turn are separate
// calls, but they answer the same question from the same evidence and are
// governed by the same grounding rule.
const finalSystemPromptBase = `CRITICAL: You MUST ONLY use information from retrieved tool results. NEVER make up names, people, events, or entities.

%s

Your approach:
- Reason over the retrieved memories to answer the question
- Make reasonable inferences when the exact answer isn't explicitly stated
- Connect related memories to form a complete picture
- Be helpful - if you have related information, use it to give the best possible answer
- ONLY use information from tool results - no external knowledge or guessing

%s

FORMATTING: Use proper markdown formatting in your answer:
- Headers (##, ###) for sections
- Lists (bullet or numbered) for enumerations
- Bold/italic for emphasis
- Tables with proper syntax (ensure blank line before and after)
- Code blocks where appropriate
- CRITICAL: Always add blank lines before and after block elements (tables, code blocks, lists)
- Proper spacing between sections

CRITICAL: Output ONLY the final synthesized answer. Do NOT include:
- Meta-commentary about what you're doing ("I'll search...", "Let me analyze...")
- Explanations of your reasoning process
- Descriptions of your approach
Just provide the direct answer with proper markdown formatting.

CRITICAL: This is a NON-CONVERSATIONAL system. NEVER ask follow-up questions, offer to search again, suggest alternatives, or end with anything like "Would you like me to..." or "Let me know if...". The user cannot reply. Your answer must be complete and self-contained.`

// structuredExtractionSystemPrompt states the role of the second call, the one
// that reshapes the prose answer into the caller's schema.
const structuredExtractionSystemPrompt = "You are a precise data extraction assistant. Extract information from text and return it as valid JSON matching the provided schema. Always extract actual content - never return empty strings for required fields if information is available."

// bankProfile carries the bank's identity into the prompt: its name, its
// mission (the standing instruction that replaces the default role) and its
// graded disposition traits. A zero profile is a prompt with no identity lines,
// which is what the preview and a bank that has configured none produce.
type bankProfile struct {
	Name        string
	Mission     string
	Disposition map[string]int
}

// systemPromptOptions is what the tool-loop prompt is built from.
type systemPromptOptions struct {
	// Profile is the bank's identity; a zero value adds no identity lines and
	// keeps the default role.
	Profile bankProfile
	// Budget is the caller's effort level (low, mid or high); empty omits the
	// depth section.
	Budget string
	// Directives are the bank's standing rules, stated as a mandatory section
	// near the top of the system prompt. The numbered reminder that closes the
	// answering call is sent by that call itself, as its last message.
	Directives []string
	// HasMentalModels and IncludeObservations say which retrieval levels the
	// prompt may describe. Naming a level the model has no tool for makes a
	// weaker model either hallucinate the call or give up.
	HasMentalModels     bool
	IncludeObservations bool
	// MaxTokens is the desired visible answer length, emitted as a soft
	// directive when positive.
	MaxTokens int
}

// buildSystemPromptForTools builds the reasoning loop's system prompt.
func buildSystemPromptForTools(opts systemPromptOptions) string {
	parts := []string{toolSystemPreamble, ""}

	if section := directivesSection(opts.Directives); section != "" {
		parts = append(parts, section)
	}

	role := defaultRole
	if trimmed := strings.TrimSpace(opts.Profile.Mission); trimmed != "" {
		role = trimmed
	}
	parts = append(parts,
		role,
		"",
		"Answer the user's question by reasoning over retrieved memories.",
		"",
		toolsLanguageRule,
		"",
	)

	parts = append(parts,
		"## CRITICAL RULES",
		"- ONLY use information from tool results - no external knowledge or guessing",
		"- You SHOULD synthesize, infer, and reason from the retrieved memories",
		"- You MUST search before saying you don't have information",
		"",
		"## How to Reason",
		"- If memories mention someone did an activity, you can infer they likely enjoyed it",
		"- Synthesize a coherent narrative from related memories",
		"- Be a thoughtful interpreter, not just a literal repeater",
		"- When the exact answer isn't stated, use what IS stated to give a best-effort answer AND surface any uncertainty — never invent confidence the data doesn't support.",
		"",
		groundingBoundary,
		"",
		"## Temporal Reasoning",
		"Every memory and observation carries temporal fields in the JSON tool result:",
		"- `mentioned_at` — when the user retained the fact (always set).",
		"- `occurred_start` / `occurred_end` — when the underlying event happened (optional, set for dated events).",
		"",
		"When facts about the SAME facet conflict — counts, statuses, ownership, location, presence, etc. — the fact with the LATEST `mentioned_at` is authoritative. Later statements SUPERSEDE earlier ones. Do NOT average, sum, or favor an explicitly-dated fact over a more recent one.",
		"",
		"Example: three count facts come back from recall:",
		"  - 'Team has 2 engineers' (mentioned_at=T1)",
		"  - 'Team now has 1 engineer' (mentioned_at=T2, occurred_start=2026-05-25)",
		"  - 'Team has 5 engineers' (mentioned_at=T3)",
		"with T1 < T2 < T3. The current size is 5, not 1. Then apply later events (e.g. someone leaving after T3) on top of that.",
		"",
		"For reconstructing a TIMELINE of events, order by `occurred_start` / `occurred_end` (when things happened), not `mentioned_at` (when they were retained).",
		"",
		"## Conflicts and Ambiguity",
		"Not every retrieval converges on a single answer. Distinguish two cases:",
		"",
		"- RESOLVABLE conflict — the temporal rule above (latest `mentioned_at` wins) cleanly picks a winner. Apply it and move on.",
		"- UNRESOLVABLE ambiguity — the data is internally inconsistent in a way the temporal rule does NOT settle. Examples: a recent aggregate (count, total) is incompatible with the individual entities you can enumerate; two equally-recent facts disagree and no later fact resolves them; events are described but their relative order is unclear; the user's own statements contradict each other and nothing later reconciles them.",
		"",
		"When the data is genuinely ambiguous: SAY SO in your answer. Name the conflicting facts. Explain why they can't be reconciled. Give a range or a best-effort interpretation with explicit uncertainty (e.g. 'between X and Y, depending on [unresolved condition]'; or 'the most recent statement says A, but B was stated earlier and the gap isn't accounted for in any later fact').",
		"",
		"An honest 'the data is inconsistent about X' beats a confident wrong answer. Do NOT pick a value arbitrarily, average conflicting values, or smooth over gaps in confident prose. Acknowledging ambiguity is a successful answer, not a failure mode.",
		"",
		"## Showing Your Reasoning",
		"For any answer that resolves a conflict between facts, applies events on top of a count or status, or settles an ambiguity — show your work in the answer text so a reader can audit it.",
		"",
		"Walk through these steps explicitly:",
		"1. **List the relevant facts in `mentioned_at` order (oldest → newest)**, each with the value it asserts. Use a short bulleted list.",
		"2. **Identify the authoritative fact** under the temporal rule (latest `mentioned_at` for the contested facet). Write its date down.",
		"3. **List candidate events to apply on top** — anything that changes the count, status, or state being asked about. Write each event's date down next to it.",
		"4. **Sanity-check each candidate event against the authoritative date** — for EVERY event from step 3, write a one-line check in the form `<event> (<event_date>) vs authoritative (<authoritative_date>) → BEFORE/AFTER → KEEP/DROP`. If the event is BEFORE or EQUAL to the authoritative date, DROP it: it is already reflected in the authoritative fact, and applying it again is double-counting. This is the single most common mistake — do not skip this step even if you feel confident.",
		"5. **Show the arithmetic or derivation explicitly** using only the KEEP events from step 4 — e.g. 'authoritative count = 5 (at 2025-02-12); kept events: Shadow died (2025-03-12, AFTER); 5 − 1 = 4'.",
		"6. If step 2 or 3 cannot be done cleanly (no clear winner, overlapping timestamps, unclear event order), STOP and surface this as an UNRESOLVABLE ambiguity per the section above — do not fabricate a derivation.",
		"",
		"For simple factual lookups that don't involve conflict or arithmetic, you can answer directly without this scaffolding.",
		"",
		"## HIERARCHICAL RETRIEVAL STRATEGY",
		"",
	)

	parts = append(parts, retrievalLadder(opts)...)

	parts = append(parts,
		"## Query Strategy",
		"recall() uses semantic search. NEVER just echo the user's question - decompose it into targeted searches:",
		"",
		"BAD: User asks 'recurring lesson themes between students' → recall('recurring lesson themes between students')",
		"GOOD: Break it down into component searches:",
		"  1. recall('lessons') - find all lesson-related memories",
		"  2. recall('teaching sessions') - alternative phrasing",
		"  3. recall('student progress') - find student-related memories",
		"",
		"Think: What ENTITIES and CONCEPTS does this question involve? Search for each separately.",
		"",
	)

	parts = append(parts, budgetDepthSection(opts.Budget)...)
	parts = append(parts, workflowSection(opts)...)
	parts = append(parts, outputFormatSection()...)

	// The volatile "now" reference sits after every static instruction, so
	// everything above it stays a cacheable prefix.
	parts = append(parts, "", currentDatetimeSection())

	// The bank's identity is the per-request tail: the name, mission and
	// disposition are the only bank-specific rows, so everything above stays a
	// cacheable prefix shared across banks.
	parts = append(parts, bankIdentityLines(opts.Profile)...)
	return strings.Join(parts, "\n")
}

// bankIdentityLines renders the bank's name, mission and disposition as the
// prompt writes them. A bank that reports none produces no lines.
func bankIdentityLines(profile bankProfile) []string {
	if profile.Name == "" && profile.Mission == "" && len(profile.Disposition) == 0 {
		return nil
	}
	name := profile.Name
	if name == "" {
		name = "Assistant"
	}
	lines := []string{"", "## Memory Bank: " + name}
	if mission := strings.TrimSpace(profile.Mission); mission != "" {
		lines = append(lines, "Mission: "+mission)
	}
	if line := dispositionLine(profile.Disposition); line != "" {
		lines = append(lines, line)
	}
	return lines
}

// dispositionLine renders the graded traits, and — for any trait away from
// neutral — what the number instructs the model to do. An all-neutral
// disposition keeps the bare traits line: nobody pays with instructions for a
// feature they did not turn on.
func dispositionLine(disposition map[string]int) string {
	if len(disposition) == 0 {
		return ""
	}
	traits := make([]string, 0, 3)
	allNeutral := true
	for _, trait := range []string{"skepticism", "literalism", "empathy"} {
		value, ok := disposition[trait]
		if !ok {
			continue
		}
		traits = append(traits, fmt.Sprintf("%s=%d", trait, value))
		if value != neutralDispositionTrait {
			allNeutral = false
		}
	}
	if len(traits) == 0 {
		return ""
	}
	line := "Disposition: " + strings.Join(traits, ", ")
	if allNeutral {
		return line
	}
	return line + "\n" + buildDispositionDescription(disposition)
}

// neutralDispositionTrait is the value a bank that never touched a trait
// reports.
const neutralDispositionTrait = 3

// traitLevelWords render a 1-5 trait as the word the instruction uses.
var traitLevelWords = map[int]string{1: "very low", 2: "low", 3: "moderate", 4: "high", 5: "very high"}

// buildDispositionDescription spells out what each graded trait means, so the
// numbers reach the model as an instruction rather than as metadata it ignores.
func buildDispositionDescription(disposition map[string]int) string {
	skepticism := map[int]string{
		1: "You are very trusting and tend to take information at face value.",
		2: "You tend to trust information but may question obvious inconsistencies.",
		3: "You have a balanced approach to information, neither too trusting nor too skeptical.",
		4: "You are somewhat skeptical and often question the reliability of information.",
		5: "You are highly skeptical and critically examine all information for accuracy and hidden motives.",
	}
	literalism := map[int]string{
		1: "You interpret information very flexibly, reading between the lines and inferring intent.",
		2: "You tend to consider context and implied meaning alongside literal statements.",
		3: "You balance literal interpretation with contextual understanding.",
		4: "You prefer to interpret information more literally and precisely.",
		5: "You interpret information very literally and focus on exact wording and commitments.",
	}
	empathy := map[int]string{
		1: "You focus primarily on facts and data, setting aside emotional context.",
		2: "You consider facts first but acknowledge emotional factors exist.",
		3: "You balance factual analysis with emotional understanding.",
		4: "You give significant weight to emotional context and human factors.",
		5: "You strongly consider the emotional state and circumstances of others when forming memories.",
	}
	return "Your disposition traits:\n" +
		fmt.Sprintf("- Skepticism (%s): %s", traitLevelWord(disposition["skepticism"]), pickTrait(skepticism, disposition["skepticism"])) + "\n" +
		fmt.Sprintf("- Literalism (%s): %s", traitLevelWord(disposition["literalism"]), pickTrait(literalism, disposition["literalism"])) + "\n" +
		fmt.Sprintf("- Empathy (%s): %s", traitLevelWord(disposition["empathy"]), pickTrait(empathy, disposition["empathy"]))
}

// traitLevelWord names a trait value, defaulting to the neutral word.
func traitLevelWord(value int) string {
	if word, ok := traitLevelWords[value]; ok {
		return word
	}
	return "moderate"
}

// pickTrait returns a trait description, defaulting to the neutral one.
func pickTrait(options map[int]string, value int) string {
	if text, ok := options[value]; ok {
		return text
	}
	return options[neutralDispositionTrait]
}

// retrievalLadder describes the levels the model may descend, in the order it
// must descend them, followed by the plan that repeats the order.
func retrievalLadder(opts systemPromptOptions) []string {
	type level struct {
		header string
		body   []string
	}
	levels := make([]level, 0, 3)
	if opts.HasMentalModels {
		levels = append(levels, level{
			header: "MENTAL MODELS (search_mental_models)",
			body: []string{
				"- User-curated summaries about specific topics",
				"- HIGHEST quality - manually created and maintained",
				"- Search returns the best match in full and a SNIPPET of the others; call " +
					"read_mental_models on any id whose snippet looks like it answers the question, and read it " +
					"before answering from it",
				"- A FRESH mental model may fully answer the question — but only if it actually STATES the " +
					"answer. One that shares the question's topic without stating the answer (e.g. it describes a " +
					"process, or names the thing without its status) has not answered it: go to the next level",
				"- Check `is_stale` field - if stale, also verify with lower levels",
			},
		})
	}
	if opts.IncludeObservations {
		levels = append(levels, level{
			header: "OBSERVATIONS (search_observations)",
			body: []string{
				"- Auto-consolidated knowledge from memories",
				"- Check `is_stale` field - if stale, ALSO use recall() to verify",
				"- Good for understanding patterns and summaries",
			},
		})
	}
	recallBody := []string{"- Individual memories (world facts and experiences)"}
	switch {
	case opts.HasMentalModels && opts.IncludeObservations:
		recallBody = append(recallBody,
			"- Use when: no mental models/observations exist, they're stale, or you need specific details",
			"- MANDATORY: If search_mental_models and search_observations return 0 results, OR return results that "+
				"do not STATE the answer, you MUST call recall() before giving up. Never report that the bank holds "+
				"nothing about something until recall() has run with the question's key terms (an issue key, name or "+
				"identifier) verbatim",
			"- This is the source of truth that other levels are built from",
			"",
			"**Tool result ordering:** `recall()` and `search_observations()` return their `memories` / `observations` arrays sorted by SEMANTIC RELEVANCE to the query, NOT by time. The POSITION of an entry tells you nothing about when it was retained. For any temporal reasoning — recency, supersession, applying events on top of a state — IGNORE the position and read the per-entry `mentioned_at` field (and `occurred_start` / `occurred_end` for events).",
			"",
		)
	case opts.HasMentalModels:
		recallBody = append(recallBody,
			"- Use when: no mental model exists, it's stale, or you need specific details",
			"- MANDATORY: If search_mental_models returns 0 results, OR returns a model that does not STATE the "+
				"answer, you MUST call recall() before giving up. Never report that the bank holds nothing about "+
				"something until recall() has run with the question's key terms verbatim",
			"- This is the source of truth that mental models are built from",
		)
	case opts.IncludeObservations:
		recallBody = append(recallBody,
			"- Use when: no observations exist, they're stale, or you need specific details",
			"- MANDATORY: If search_observations returns 0 results or count=0, OR returns observations that do "+
				"not STATE the answer, you MUST call recall() before giving up. Never report that the bank holds "+
				"nothing about something until recall() has run with the question's key terms verbatim",
			"- This is the source of truth that observations are built from",
			"",
			"**Tool result ordering:** `recall()` and `search_observations()` return their `memories` / `observations` arrays sorted by SEMANTIC RELEVANCE to the query, NOT by time. The POSITION of an entry tells you nothing about when it was retained. For any temporal reasoning — recency, supersession, applying events on top of a state — IGNORE the position and read the per-entry `mentioned_at` field (and `occurred_start` / `occurred_end` for events).",
			"",
		)
	default:
		recallBody = append(recallBody,
			"- MANDATORY: Call recall() to gather facts before giving up",
			"- This is the source of truth.",
		)
	}
	levels = append(levels, level{header: "RAW FACTS (recall) - Ground Truth", body: recallBody})

	suffixes := make([]string, len(levels))
	if len(levels) >= 2 {
		suffixes[0] = " - Try First"
	}
	if len(levels) == 3 {
		suffixes[1] = " - Second Priority"
	}

	parts := make([]string, 0, 4*len(levels)+8)
	if len(levels) == 1 {
		parts = append(parts, "You have access to ONE level of knowledge:")
	} else {
		word := "TWO"
		if len(levels) == 3 {
			word = "THREE"
		}
		parts = append(parts, "You have access to "+word+" levels of knowledge. Use them in this order:")
	}
	parts = append(parts, "")
	for index, item := range levels {
		parts = append(parts, fmt.Sprintf("### %d. %s%s", index+1, item.header, suffixes[index]))
		parts = append(parts, item.body...)
		parts = append(parts, "")
	}

	if len(levels) > 1 {
		names := make([]string, 0, len(levels))
		for _, item := range levels {
			name := strings.Split(item.header, " (")[0]
			name = strings.Split(name, " - ")[0]
			names = append(names, titleCase(name))
		}
		parts = append(parts,
			"## Search Plan",
			"Work down the levels in order ("+strings.Join(names, " → ")+") before you answer:",
			"- Search a level before deciding it has nothing; a level you did not search is not evidence of absence.",
			"- Stop descending as soon as what you have answers the question — fresh "+names[0]+" often do.",
			"- Go deeper when the level above is stale, thin, or silent on what was asked.",
			"- Call `done` with the answer once you have the evidence. Do not write the answer as plain text.",
			"",
		)
	}
	return parts
}

// budgetDepthSection states how much depth the caller's effort level buys. It
// bounds how long the answer is and how many searches are spent on it — never
// whether the lower levels are consulted at all.
func budgetDepthSection(budget string) []string {
	switch strings.ToLower(strings.TrimSpace(budget)) {
	case "low":
		return []string{
			"## RESEARCH DEPTH: SHALLOW (Quick Response)",
			"- Keep the ANSWER short: a quick overview, not exhaustive detail. Depth is what you cut, " +
				"not coverage",
			"- Spend few searches, but make them count: vary the query instead of repeating one that " +
				"already ran",
			"- A mental model or observation that ANSWERS the question is enough to stop; one that is " +
				"merely on the same topic is not",
			"- If what you found does not cover the question, go on to the next level rather than " +
				"answering from it",
			"",
		}
	case "mid":
		return []string{
			"## RESEARCH DEPTH: MODERATE (Balanced)",
			"- Balance thoroughness with efficiency",
			"- Check multiple sources when the question warrants it",
			"- Verify stale data if it's central to the answer",
			"- A result that is merely on the same topic does not answer the question: when it does not " +
				"cover it, go on to the next level",
			"- Don't over-explore, but ensure reasonable coverage",
			"",
		}
	case "high":
		return []string{
			"## RESEARCH DEPTH: DEEP (Thorough Exploration)",
			"- Explore comprehensively before answering",
			"- Search across all available knowledge levels",
			"- Use multiple query variations to ensure coverage",
			"- Verify information across different retrieval levels",
			"- Use expand() to get full context on important memories",
			"- Take time to synthesize a complete, well-researched answer",
			"",
		}
	default:
		return nil
	}
}

// workflowSection states the order of the steps the model is expected to take.
func workflowSection(opts systemPromptOptions) []string {
	steps := make([]string, 0, 5)
	if opts.HasMentalModels {
		steps = append(steps, "First, try search_mental_models() - check if a curated summary exists")
	}
	if opts.IncludeObservations {
		if opts.HasMentalModels {
			steps = append(steps,
				"If there is no mental model, it's stale, OR it does not state the answer, "+
					"try search_observations() for consolidated knowledge")
		} else {
			steps = append(steps, "First, try search_observations() - check for consolidated knowledge")
		}
	}
	switch {
	case opts.IncludeObservations && opts.HasMentalModels:
		steps = append(steps,
			"If the levels above are stale, do not state the answer, OR you need specific details, "+
				"use recall() for raw facts. Reporting that nothing is known requires recall() first")
	case opts.IncludeObservations:
		steps = append(steps,
			"If search_observations returns 0 results, is stale, OR does not state the answer, you MUST call "+
				"recall() for raw facts")
	case opts.HasMentalModels:
		steps = append(steps,
			"If there is no mental model, it's stale, OR it does not state the answer, use recall() for raw facts. "+
				"Reporting that nothing is known requires recall() first")
	default:
		steps = append(steps, "Call recall() to gather raw facts")
	}
	steps = append(steps,
		"Use expand() if you need more context on specific memories",
		"When ready, call done() with your answer and supporting IDs")

	parts := append([]string{"## Workflow"}, make([]string, 0, len(steps))...)
	for index, step := range steps {
		parts = append(parts, fmt.Sprintf("%d. %s", index+1, step))
	}
	return parts
}

// outputFormatSection asks for a well-formatted markdown answer and states the
// rules that keep identifiers out of it.
func outputFormatSection() []string {
	return []string{
		"",
		"## Output Format: Well-Formatted Markdown Answer",
		"Call done() with a well-formatted markdown 'answer' field.",
		"- USE markdown formatting for structure (headers, lists, bold, italic, code blocks, tables, etc.)",
		"- CRITICAL: Add blank lines before and after block elements (tables, code blocks, lists)",
		"- Format for clarity and readability with proper spacing and hierarchy",
		"- NEVER include memory IDs, UUIDs, or 'Memory references' in the answer text",
		"- Put IDs ONLY in the memory_ids/mental_model_ids/observation_ids arrays, not in the answer",
		"- CRITICAL: This is a NON-CONVERSATIONAL system. NEVER ask follow-up questions, offer further assistance, or suggest next steps. Your answer must be complete and self-contained. The user cannot reply.",
	}
}

// buildFinalSystemPrompt builds the answering call's system prompt. The
// directives are repeated here because the answer is a separate call: a rule
// stated only in the reasoning prompt is out of scope for the model that
// actually writes the answer.
func buildFinalSystemPrompt(directives []string, profile bankProfile) string {
	role := defaultFinalRole
	if trimmed := strings.TrimSpace(profile.Mission); trimmed != "" {
		role = trimmed + "\n" + defaultFinalRole
	}
	base := fmt.Sprintf(finalSystemPromptBase, role, groundingBoundary)

	parts := []string{}
	if section := directivesSection(directives); section != "" {
		parts = append(parts, section)
	}
	parts = append(parts, base, finalLanguageRule)
	if reminder := directivesReminder(directives); reminder != "" {
		parts = append(parts, reminder)
	}
	parts = append(parts, currentDatetimeSection())
	if identity := bankIdentityLines(profile); len(identity) > 0 {
		parts = append(parts, strings.TrimSpace(strings.Join(identity, "\n")))
	}

	trimmed := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			trimmed = append(trimmed, part)
		}
	}
	return strings.Join(trimmed, "\n\n")
}

// buildFinalPrompt builds the answering call's user message: the evidence that
// was gathered, then the question, then the instructions and the length target.
func buildFinalPrompt(question, evidence string, maxTokens int) string {
	parts := []string{}
	if strings.TrimSpace(evidence) == "" {
		parts = append(parts, "\n## Retrieved Data\nNo data was retrieved.")
	} else {
		parts = append(parts,
			"\n## Retrieved Data (synthesize and reason from this data)",
			strings.TrimRight(evidence, "\n"))
	}
	parts = append(parts,
		"\n## Question\n"+question,
		"\n## Instructions\n"+finalInstructions)
	if directive := lengthDirective(maxTokens); directive != "" {
		parts = append(parts, directive)
	}
	return strings.Join(parts, "\n")
}

// lengthDirective states the caller's desired visible answer length. It steers
// the model rather than capping the call: on a thinking model a hard cap is
// eaten by reasoning tokens and cuts the answer off mid-word, so the length is
// enforced afterwards, once the answer exists.
func lengthDirective(maxTokens int) string {
	if maxTokens <= 0 {
		return ""
	}
	return "\n## Length\n" + fmt.Sprintf(
		"Aim for a complete, self-contained answer of approximately %d tokens. ", maxTokens) +
		"Finishing cleanly matters more than length: end on a complete sentence and NEVER stop " +
		"mid-word, mid-list, or mid-code-fence. If you near the budget, wrap up gracefully rather " +
		"than cutting off."
}

// currentDatetimeSection grounds time-relative reasoning in the current date.
func currentDatetimeSection() string {
	return "## Current Date and Time\nThe current date and time is " +
		time.Now().UTC().Format("2006-01-02 15:04") + " UTC."
}

// buildStructuredExtractionPrompt builds the user prompt of the extraction call
// that reshapes a prose answer into the caller's schema. The schema itself is
// shown to the model, together with a field-by-field listing that marks which
// fields are required, because a model told only "match this schema" leaves
// required fields empty.
func buildStructuredExtractionPrompt(answer string, schema map[string]any) (string, error) {
	properties, _ := schema["properties"].(map[string]any)
	if len(properties) == 0 {
		return "", fmt.Errorf("response_schema declares no properties")
	}
	required := map[string]bool{}
	if names, ok := schema["required"].([]any); ok {
		for _, name := range names {
			if text, ok := name.(string); ok {
				required[text] = true
			}
		}
	}
	names := make([]string, 0, len(properties))
	for name := range properties {
		names = append(names, name)
	}
	sort.Strings(names)

	descriptions := make([]string, 0, len(names))
	for _, name := range names {
		field, _ := properties[name].(map[string]any)
		fieldType := "string"
		if typed, ok := field["type"].(string); ok && typed != "" {
			fieldType = typed
		}
		description := ""
		if typed, ok := field["description"].(string); ok {
			description = typed
		}
		marker := " (optional)"
		if required[name] {
			marker = " (REQUIRED)"
		}
		descriptions = append(descriptions, fmt.Sprintf("- %s (%s)%s: %s", name, fieldType, marker, description))
	}

	encoded, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return "", err
	}

	return "Your task is to extract specific information from the answer below and format it as JSON.\n\n" +
		"ANSWER TO EXTRACT FROM:\n\"\"\"\n" + answer + "\n\"\"\"\n\n" +
		"REQUIRED OUTPUT FORMAT - Extract the following fields from the answer above:\n" +
		strings.Join(descriptions, "\n") + "\n\n" +
		"JSON Schema:\n```json\n" + string(encoded) + "\n```\n\n" +
		"INSTRUCTIONS:\n" +
		"1. Read the answer carefully and identify the information that matches each field\n" +
		"2. Extract the ACTUAL content from the answer - do NOT leave fields empty if information is present\n" +
		"3. For string fields: use the exact text or a clear summary from the answer\n" +
		"4. For array fields: return a JSON array (e.g., [\"item1\", \"item2\"]), NOT a string\n" +
		"5. For required fields: you MUST provide a value extracted from the answer\n" +
		"6. Return ONLY the JSON object, no explanation\n\n" +
		"OUTPUT:", nil
}

// schemaFromMap turns a caller-supplied JSON schema into the boundary's schema
// type, so the extraction call can ask for a shape the deployment's provider
// understands. A field the boundary does not model is ignored there rather than
// silently honoured.
func schemaFromMap(raw map[string]any) (*provider.Schema, error) {
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	schema := &provider.Schema{}
	if err := json.Unmarshal(encoded, schema); err != nil {
		return nil, err
	}
	return schema, nil
}

// titleCase upper-cases the first letter of each word, matching how the
// reasoning prompt names the retrieval levels in its search plan.
func titleCase(value string) string {
	words := strings.Fields(value)
	for index, word := range words {
		if word == "" {
			continue
		}
		words[index] = strings.ToUpper(word[:1]) + strings.ToLower(word[1:])
	}
	return strings.Join(words, " ")
}
