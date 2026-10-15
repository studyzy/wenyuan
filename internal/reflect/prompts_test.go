package reflect

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// The four prompt texts a caller can recognise a step of the reflection by.
//
// They are pinned here because they are the only thing on the wire that says
// which step a model was asked to perform: a transcript, a wiremock rule or a
// golden file written against this service identifies the step by its prompt
// text and nothing else. A reworded prompt is a silent change to that
// contract, so each anchor is asserted together with the surrounding text it
// belongs to — a phrase spliced into different instructions would satisfy the
// anchor alone and still be a different prompt.
const (
	toolLoopAnchor      = "CRITICAL: You MUST ONLY use information from retrieved tool results."
	finalAnswerAnchor   = "You are a thoughtful assistant that synthesizes answers from retrieved memories."
	structuredAnchor    = "You are a precise data extraction assistant."
	connectionProbeText = "Say 'ok'"
)

// The reasoning loop's system prompt opens with the anti-hallucination rule and
// carries the retrieval instructions around it; it is not the answering prompt.
func TestToolSystemPromptCarriesTheReasoningLoopText(t *testing.T) {
	prompt := buildSystemPromptForTools(systemPromptOptions{
		Budget:              "mid",
		Directives:          []string{"Always answer in French."},
		HasMentalModels:     true,
		IncludeObservations: true,
	})

	for _, want := range []string{
		"CRITICAL: You MUST ONLY use information from retrieved tool results. " +
			"NEVER make up names, people, events, or entities.",
		"You are a reflection agent that answers questions by reasoning over retrieved memories.",
		"Answer the user's question by reasoning over retrieved memories.",
		"## CRITICAL RULES",
		"## How to Reason",
		"## What Counts As Inference",
		"## Temporal Reasoning",
		"## HIERARCHICAL RETRIEVAL STRATEGY",
		"### 1. MENTAL MODELS (search_mental_models) - Try First",
		"### 2. OBSERVATIONS (search_observations) - Second Priority",
		"### 3. RAW FACTS (recall) - Ground Truth",
		"## Search Plan",
		"## Query Strategy",
		"## RESEARCH DEPTH: MODERATE (Balanced)",
		"## Workflow",
		"## Output Format: Well-Formatted Markdown Answer",
		"## Current Date and Time",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the reasoning prompt is missing %q", want)
		}
	}
	// The answering role belongs to the other call: leaking it here would make
	// the two prompts indistinguishable and give the loop prose instructions.
	if strings.Contains(prompt, finalAnswerAnchor) {
		t.Fatal("the reasoning prompt carries the answering role")
	}
	// The directive is a mandatory section of this prompt, not a separate
	// message, so a model reading the prompt sees it before its own rules.
	if !strings.Contains(prompt, "## DIRECTIVES (MANDATORY)") {
		t.Fatal("the directives section is missing from the reasoning prompt")
	}
	if sectionIndex, rulesIndex := strings.Index(prompt, "## DIRECTIVES (MANDATORY)"),
		strings.Index(prompt, "## CRITICAL RULES"); sectionIndex > rulesIndex {
		t.Fatal("the directives section must precede the loop's own rules")
	}
}

// The reasoning prompt describes exactly the levels the model has tools for.
func TestToolSystemPromptDescribesTheOfferedLevels(t *testing.T) {
	observations := buildSystemPromptForTools(systemPromptOptions{IncludeObservations: true})
	if strings.Contains(observations, "search_mental_models()") {
		t.Fatal("a prompt with no perspective tool must not name one")
	}
	if !strings.Contains(observations, "You have access to TWO levels of knowledge.") {
		t.Fatal("two levels must be announced as two")
	}
	all := buildSystemPromptForTools(systemPromptOptions{HasMentalModels: true, IncludeObservations: true})
	if !strings.Contains(all, "You have access to THREE levels of knowledge.") {
		t.Fatal("three levels must be announced as three")
	}
}

// The answering call has its own system prompt and its own role.
func TestFinalSystemPromptCarriesTheSynthesisRole(t *testing.T) {
	prompt := buildFinalSystemPrompt(nil, bankProfile{})

	for _, want := range []string{
		"CRITICAL: You MUST ONLY use information from retrieved tool results. " +
			"NEVER make up names, people, events, or entities.",
		"You are a thoughtful assistant that synthesizes answers from retrieved memories.",
		"Your approach:",
		"## What Counts As Inference",
		"FORMATTING: Use proper markdown formatting in your answer:",
		"CRITICAL: This is a NON-CONVERSATIONAL system.",
		"## Current Date and Time",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the answering prompt is missing %q", want)
		}
	}
	// The retrieval instructions are the loop's; the answering call has already
	// retrieved everything it will reason over.
	for _, unwanted := range []string{"## HIERARCHICAL RETRIEVAL STRATEGY", "## Search Plan", "## Workflow"} {
		if strings.Contains(prompt, unwanted) {
			t.Fatalf("the answering prompt carries retrieval instructions (%q)", unwanted)
		}
	}
}

// The answering user message carries the evidence, the question and the
// instructions, in that order, and states the length target when there is one.
func TestFinalPromptCarriesEvidenceQuestionAndInstructions(t *testing.T) {
	prompt := buildFinalPrompt("where does Alice live?", "f1 Alice moved to Berlin\n", 300)

	evidence := strings.Index(prompt, "## Retrieved Data (synthesize and reason from this data)")
	question := strings.Index(prompt, "## Question\nwhere does Alice live?")
	instructions := strings.Index(prompt, "## Instructions\nProvide a thoughtful answer by synthesizing")
	length := strings.Index(prompt, "## Length")
	if evidence < 0 || question < 0 || instructions < 0 || length < 0 {
		t.Fatalf("the answering prompt is incomplete:\n%s", prompt)
	}
	if evidence >= question || question >= instructions || instructions >= length {
		t.Fatalf("the answering prompt is out of order:\n%s", prompt)
	}
	if !strings.Contains(prompt, "f1 Alice moved to Berlin") {
		t.Fatal("the evidence must reach the answering call")
	}
	withoutEvidence := buildFinalPrompt("q", "", 0)
	if !strings.Contains(withoutEvidence, "## Retrieved Data\nNo data was retrieved.") {
		t.Fatal("an empty evidence set must say so rather than render nothing")
	}
	if strings.Contains(withoutEvidence, "## Length") {
		t.Fatal("a request with no length target must carry no length directive")
	}
}

// The extraction call states its role and is handed the schema it must satisfy.
func TestStructuredExtractionPromptCarriesSchemaAndInstructions(t *testing.T) {
	if !strings.Contains(structuredExtractionSystemPrompt, structuredAnchor) {
		t.Fatal("the extraction system prompt lost its role line")
	}
	for _, want := range []string{
		"You are a precise data extraction assistant.",
		"return it as valid JSON matching the provided schema",
		"Always extract actual content - never return empty strings for required fields",
	} {
		if !strings.Contains(structuredExtractionSystemPrompt, want) {
			t.Fatalf("the extraction role is incomplete (%q)", want)
		}
	}

	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"city":      map[string]any{"type": "string", "description": "the city"},
			"confident": map[string]any{"type": "boolean"},
		},
		"required": []any{"city"},
	}
	prompt, err := buildStructuredExtractionPrompt("Alice lives in Berlin.", schema)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"ANSWER TO EXTRACT FROM:",
		"Alice lives in Berlin.",
		"REQUIRED OUTPUT FORMAT",
		"- city (string) (REQUIRED): the city",
		"- confident (boolean) (optional):",
		"JSON Schema:",
		"\"city\"",
		"INSTRUCTIONS:",
		"For required fields: you MUST provide a value extracted from the answer",
		"OUTPUT:",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("the extraction prompt is missing %q:\n%s", want, prompt)
		}
	}

	if _, err := buildStructuredExtractionPrompt("answer", map[string]any{"type": "object"}); err == nil {
		t.Fatal("a schema with no properties must be refused rather than sent")
	}
}

// A schema the request supplied reaches the extraction call and its result
// reaches the outcome; without a schema no second call is made.
func TestStructuredExtractionRunsOnlyWithASchema(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
	}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "Berlin"}}}
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
			toolCall(ToolDone, nil),
		),
		provider.CompletionResult{Content: "Alice lives in Berlin."},
		provider.CompletionResult{Structured: map[string]any{"city": "Berlin"}, Usage: provider.Usage{InputTokens: 7, OutputTokens: 3, TotalTokens: 10}},
	)}
	loop := NewLoop(p, retrieval, Config{Question: "where?", ResponseSchema: schema})
	outcome, err := loop.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if outcome.StructuredOutput["city"] != "Berlin" {
		t.Fatalf("structured output = %v", outcome.StructuredOutput)
	}
	if outcome.StructuredOutputError != "" {
		t.Fatalf("unexpected extraction error: %s", outcome.StructuredOutputError)
	}
	if outcome.Usage.TotalTokens != 10 {
		t.Fatalf("the extraction call's usage must be billed: %+v", outcome.Usage)
	}
	last := p.requests[len(p.requests)-1]
	if !strings.Contains(last.Messages[0].Content, structuredAnchor) {
		t.Fatal("the second call must be the extraction call")
	}
	if last.ResponseSchema == nil {
		t.Fatal("the extraction call must ask for the caller's schema")
	}

	// No schema: the answering call is the last call, and no extraction follows.
	plain := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
			toolCall(ToolDone, nil),
		),
		provider.CompletionResult{Content: "Alice lives in Berlin."},
	)}
	if _, err := NewLoop(plain, retrieval, Config{Question: "where?"}).Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, req := range plain.requests {
		if strings.Contains(req.Messages[0].Content, structuredAnchor) {
			t.Fatal("a request with no schema must not pay for an extraction call")
		}
	}
}

// A failed extraction is reported on the outcome rather than raised: the
// answer that could not be reshaped is still an answer.
func TestStructuredExtractionFailureIsReported(t *testing.T) {
	schema := map[string]any{
		"type":       "object",
		"properties": map[string]any{"city": map[string]any{"type": "string"}},
	}
	retrieval := &stubRetrieval{observations: []Evidence{{Kind: "observation", ID: "obs-1", Text: "Berlin"}}}
	p := &scriptedProvider{name: "stub", script: append(
		turnScript(
			toolCall(ToolSearchObservations, map[string]any{"query": "q"}),
			toolCall(ToolDone, nil),
		),
		provider.CompletionResult{Content: "Alice lives in Berlin."},
		provider.CompletionResult{Content: "absolutely not json"},
	)}
	outcome, err := NewLoop(p, retrieval, Config{Question: "where?", ResponseSchema: schema}).Run(context.Background())
	if err != nil {
		t.Fatalf("a failed extraction must not fail the reflection: %v", err)
	}
	if outcome.StructuredOutput != nil {
		t.Fatalf("structured output = %v, want none", outcome.StructuredOutput)
	}
	if outcome.StructuredOutputError == "" {
		t.Fatal("a failed extraction must say so")
	}
	if outcome.Answer != "Alice lives in Berlin." {
		t.Fatalf("the answer must survive the failed extraction: %q", outcome.Answer)
	}
}

// The connection probe is a literal every deployment can recognise; it is not
// the model's own answer text and it does not vary with configuration.
func TestConnectionProbeText(t *testing.T) {
	if provider.VerificationPrompt != "Say 'ok'" {
		t.Fatalf("probe text = %q", provider.VerificationPrompt)
	}
	if provider.AnthropicVerificationPrompt != "test" {
		t.Fatalf("anthropic probe text = %q", provider.AnthropicVerificationPrompt)
	}
}

// A model that answers in prose before calling any tool cannot drive the loop,
// and the failure names the cause rather than the bank: the caller is an agent
// that would otherwise act on a plausible non-answer.
func TestTextOnlyFirstTurnNamesTheProviderMismatch(t *testing.T) {
	retrieval := &stubRetrieval{}
	p := &scriptedProvider{name: "openai-compatible", script: []provider.CompletionResult{{
		Content:      "I think Alice lives in Berlin.",
		FinishReason: "stop",
		Model:        "stub-model",
	}}}
	_, err := NewLoop(p, retrieval, Config{Question: "where does Alice live?"}).Run(context.Background())
	if err == nil {
		t.Fatal("prose before any tool call must not be returned as an answer")
	}
	message := err.Error()
	for _, want := range []string{
		"produced no usable tool call during initial tool selection",
		"tool_choice=search_mental_models",
		"finish_reason=stop",
		"openai-compatible/stub-model",
		"I think Alice lives in Berlin.",
	} {
		if !strings.Contains(message, want) {
			t.Fatalf("the failure does not name %q: %s", want, message)
		}
	}
}
