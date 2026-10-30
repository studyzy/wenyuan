package extraction

import (
	"context"
	"strings"
	"testing"
)

// extractWithMode runs one extraction in the given mode against a canned
// response and returns the stub's captured request, so a test can inspect the
// prompt and schema a mode actually sends.
func extractWithMode(t *testing.T, opts Options, structured any) (*stubProvider, Result) {
	t.Helper()
	stub := &stubProvider{name: "openai", structured: structured}
	result, err := New(stub).ExtractWith(context.Background(), "Alice moved to Berlin.", opts)
	if err != nil {
		t.Fatalf("extract (%s): %v", opts.Mode, err)
	}
	return stub, result
}

// test-each-mode-selects-its-own-prompt-and-schema: the extraction mode decides
// the system prompt and the response schema, so verbose asks for the five
// dimensions, verbatim for metadata only, and the default keeps the shipped
// concise prompt.
func TestEachModeSelectsItsOwnPromptAndSchema(t *testing.T) {
	empty := map[string]any{"facts": []any{}}

	concise, _ := extractWithMode(t, Options{}, empty)
	if got := concise.lastReq.Messages[0].Content; got != factExtractionPrompt {
		t.Fatalf("default prompt is not the shipped one:\n%s", got)
	}
	if concise.lastReq.ResponseSchema.Properties["facts"].Items.Properties["what"] == nil {
		t.Fatal("the default schema must ask for the five dimensions")
	}

	verbose, _ := extractWithMode(t, Options{Mode: ModeVerbose}, empty)
	if got := verbose.lastReq.Messages[0].Content; got != verboseExtractionPrompt {
		t.Fatalf("verbose did not select the verbose prompt:\n%s", got)
	}
	for _, dimension := range dimensionNames {
		if verbose.lastReq.ResponseSchema.Properties["facts"].Items.Properties[dimension] == nil {
			t.Fatalf("verbose schema is missing the %q dimension", dimension)
		}
	}
	if verbose.lastReq.Messages[0].Content == concise.lastReq.Messages[0].Content {
		t.Fatal("verbose and concise must not share a prompt")
	}

	verbatim, _ := extractWithMode(t, Options{Mode: ModeVerbatim}, empty)
	if got := verbatim.lastReq.Messages[0].Content; got != verbatimExtractionPrompt {
		t.Fatalf("verbatim did not select the verbatim prompt:\n%s", got)
	}
	if verbatim.lastReq.ResponseSchema.Properties["facts"].Items.Properties["what"] != nil {
		t.Fatal("the verbatim schema must not ask for a 'what'")
	}
}

// The v1.1.0 extraction prompt tells the model not to write a placeholder year
// for an open-ended event (#5820, fact_extraction.py:1315-1316). It sits in the
// verbose prompt alone, beside the coarse-date rule it extends, which is where
// the reference renders it: the concise, custom and verbatim templates share the
// shorter temporal section and gained no such bullet.
func TestOpenEndedDatesAreToldNotToUseAPlaceholderYear(t *testing.T) {
	const bullet = "- OPEN-ENDED events (still ongoing, \"since forever\", no stated end): set only the bound the\n" +
		"  text gives. NEVER write a placeholder year such as 0001 or 9999 for an unknown bound.\n"
	if !strings.Contains(verboseExtractionPrompt, bullet) {
		t.Fatal("the verbose prompt is missing the open-ended-dates rule")
	}
	// It follows the coarse-date rule, as the reference lays it out.
	coarse := strings.Index(verboseExtractionPrompt, "- COARSE DATES")
	openEnded := strings.Index(verboseExtractionPrompt, "- OPEN-ENDED events")
	if coarse < 0 || openEnded < coarse {
		t.Fatalf("the open-ended rule must follow the coarse-date rule (coarse %d, open-ended %d)", coarse, openEnded)
	}
	for name, prompt := range map[string]string{
		"concise":  factExtractionPrompt,
		"verbatim": verbatimExtractionPrompt,
		"custom":   customFactExtractionPrompt("Record only what the speaker said."),
	} {
		if strings.Contains(prompt, bullet) {
			t.Fatalf("the %s prompt must not carry the verbose temporal section", name)
		}
	}
}

// test-custom-mode-carries-the-banks-instructions: a bank's custom instructions
// reach the prompt in custom mode, and only there — the reference stores them
// unconditionally but consults them only when the mode asks for them.
func TestCustomModeCarriesInstructionsAndOthersIgnoreThem(t *testing.T) {
	empty := map[string]any{"facts": []any{}}
	const instructions = "Record only what the speaker said about themselves."

	custom, _ := extractWithMode(t, Options{Mode: ModeCustom, Instructions: instructions}, empty)
	if !strings.Contains(custom.lastReq.Messages[0].Content, instructions) {
		t.Fatalf("custom prompt is missing the bank's instructions:\n%s", custom.lastReq.Messages[0].Content)
	}

	concise, _ := extractWithMode(t, Options{Instructions: instructions}, empty)
	if strings.Contains(concise.lastReq.Messages[0].Content, instructions) {
		t.Fatalf("the default mode must not carry custom instructions:\n%s", concise.lastReq.Messages[0].Content)
	}
}

// test-verbose-fact-stores-the-collapsed-text: verbose answers with the five
// dimensions, and the stored text is the reference's collapse of them, with
// "where" kept as the fact's location rather than folded into the text.
func TestVerboseFactStoresCollapsedText(t *testing.T) {
	structured := map[string]any{"facts": []any{
		map[string]any{
			"what":      "Emily married Sarah at a rooftop garden",
			"when":      "Saturday, June 8, 2024",
			"where":     "San Francisco",
			"who":       "Emily (user's roommate), Sarah",
			"why":       "the user dreams of a similar ceremony",
			"fact_type": "world",
		},
	}}
	_, result := extractWithMode(t, Options{Mode: ModeVerbose}, structured)
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want 1", len(result.Facts))
	}
	want := "Emily married Sarah at a rooftop garden | When: Saturday, June 8, 2024 | Involving: Emily (user's roommate), Sarah | the user dreams of a similar ceremony"
	if got := result.Facts[0].Text; got != want {
		t.Fatalf("collapsed text = %q, want %q", got, want)
	}
	if result.Facts[0].Where != "San Francisco" {
		t.Fatalf("where = %q, want it kept off the collapsed text", result.Facts[0].Where)
	}
}

// A dimension the model left as the reference's "N/A" placeholder is treated as
// absent, so the collapse does not carry a meaningless "When: N/A".
func TestCollapseSkipsAbsentDimensions(t *testing.T) {
	structured := map[string]any{"facts": []any{
		map[string]any{"what": "Alice likes coffee", "when": "N/A", "who": "N/A", "why": "N/A"},
	}}
	_, result := extractWithMode(t, Options{Mode: ModeVerbose}, structured)
	if got := result.Facts[0].Text; got != "Alice likes coffee" {
		t.Fatalf("collapsed text = %q, want the bare fact", got)
	}
}

// The "N/A" placeholder means "no value" for the temporal fields too, so a
// model that answers occurred_start with it must not have the literal string
// stored as a date.
func TestTimestampPlaceholderIsNotADate(t *testing.T) {
	structured := map[string]any{"facts": []any{
		map[string]any{"what": "Alice moved to Berlin", "occurred_start": "N/A", "occurred_end": "n/a"},
	}}
	_, result := extractWithMode(t, Options{Mode: ModeVerbose}, structured)
	fact := result.Facts[0]
	if fact.OccurredStart != "" || fact.OccurredEnd != "" {
		t.Fatalf("placeholder dates survived: %+v", fact)
	}
}

// test-verbatim-stores-the-chunk-text: verbatim mode stores the input text as
// the fact, with the metadata the model extracted (entities, dates) riding
// along, and collapses the model's entries to one fact.
func TestVerbatimStoresChunkText(t *testing.T) {
	const chunk = "Alice moved to Berlin in March."
	structured := map[string]any{"facts": []any{
		map[string]any{"entities": []any{"Alice", "Berlin"}, "where": "Berlin", "fact_kind": "event", "occurred_start": "2024-03-01T00:00:00", "fact_type": "world"},
		map[string]any{"entities": []any{"moving"}},
	}}
	stub := &stubProvider{name: "openai", structured: structured}
	result, err := New(stub).ExtractWith(context.Background(), chunk, Options{Mode: ModeVerbatim})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 {
		t.Fatalf("facts = %d, want the single collapsed fact", len(result.Facts))
	}
	fact := result.Facts[0]
	if fact.Text != chunk {
		t.Fatalf("verbatim text = %q, want the chunk %q", fact.Text, chunk)
	}
	if len(fact.Entities) != 3 {
		t.Fatalf("entities = %v, want the merged list", fact.Entities)
	}
	if fact.Where != "Berlin" || fact.OccurredStart != "2024-03-01T00:00:00" {
		t.Fatalf("verbatim metadata lost: %+v", fact)
	}
}

// test-verbatim-keeps-the-chunk-when-the-model-returns-nothing: the mode exists
// to preserve the original text, so a model that reports no metadata must not
// cost the write its memory.
func TestVerbatimKeepsChunkWhenModelReturnsNothing(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{}}}
	result, err := New(stub).ExtractWith(context.Background(), "Alice moved to Berlin.", Options{Mode: ModeVerbatim})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Facts) != 1 || result.Facts[0].Text != "Alice moved to Berlin." {
		t.Fatalf("verbatim must keep the chunk: %+v", result.Facts)
	}
}

// test-chunks-mode-skips-the-model: chunks mode asks the model nothing at all,
// so no completeness call is made and the caller stores the segments.
func TestChunksModeSkipsTheModel(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{}}}
	result, err := New(stub).ExtractWith(context.Background(), "Alice moved to Berlin.", Options{Mode: ModeChunks})
	if err != nil {
		t.Fatal(err)
	}
	if result.Extracted || len(result.Facts) != 0 {
		t.Fatalf("chunks mode must emit no facts: %+v", result)
	}
	if stub.lastReq.Messages != nil {
		t.Fatal("chunks mode must not call the model")
	}
}

// The mode name is matched exactly, as the reference's `== "chunks"` does: an
// unrecognised spelling such as "CHUNKS" is the concise default, so the model
// is called rather than the write taking the verbatim chunk path.
func TestModeNamesAreMatchedExactly(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{
		map[string]any{"what": "Alice moved to Berlin"},
	}}}
	result, err := New(stub).ExtractWith(context.Background(), "Alice moved to Berlin.",
		Options{Mode: "CHUNKS"})
	if err != nil {
		t.Fatal(err)
	}
	if stub.lastReq.Messages == nil {
		t.Fatal("an unrecognised mode must fall back to the model-backed concise default")
	}
	if len(result.Facts) != 1 || result.Facts[0].Text != "Alice moved to Berlin" {
		t.Fatalf("facts = %+v, want the concise extraction", result.Facts)
	}
	if stub.lastReq.Messages[0].Content != factExtractionPrompt {
		t.Fatal("an unrecognised mode must run the concise prompt")
	}
}

// The parity suite identifies a pipeline step by an anchor phrase found anywhere
// in the call's text. The extraction user message must carry the extract_facts
// anchor, in every mode, or the reference's stub cannot route the call.
func TestExtractionCarriesTheParityAnchor(t *testing.T) {
	const anchor = "Extract facts from the following chunk."
	for _, mode := range []string{ModeConcise, ModeVerbose, ModeCustom, ModeVerbatim} {
		stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{}}}
		opts := Options{Mode: mode}
		if mode == ModeCustom {
			opts.Instructions = "Record only what the speaker said about themselves."
		}
		if _, err := New(stub).ExtractWith(context.Background(), "Alice moved to Berlin.", opts); err != nil {
			t.Fatalf("mode %q: %v", mode, err)
		}
		var all strings.Builder
		for _, message := range stub.lastReq.Messages {
			all.WriteString(message.Content)
		}
		if !strings.Contains(all.String(), anchor) {
			t.Fatalf("mode %q: extraction text is missing the extract_facts anchor", mode)
		}
	}
}

// test-default-mode-is-unchanged: an unconfigured extractor sends the shipped
// prompt and schema, and a response in the historical "text" shape still stores
// that text.
func TestDefaultModeIsUnchanged(t *testing.T) {
	stub := &stubProvider{name: "openai", structured: map[string]any{"facts": []any{
		map[string]any{"text": "The deployment runs on Tuesdays.", "confidence": 0.9},
	}}}
	result, err := New(stub).Extract(context.Background(), "We deploy on Tuesdays.")
	if err != nil {
		t.Fatal(err)
	}
	if got := stub.lastReq.Messages[0].Content; got != factExtractionPrompt {
		t.Fatalf("default prompt drifted:\n%s", got)
	}
	if stub.lastReq.ResponseSchema.Properties["facts"].Items.Properties["what"] == nil {
		t.Fatal("the default schema must ask for the five dimensions")
	}
	if len(result.Facts) != 1 || result.Facts[0].Text != "The deployment runs on Tuesdays." {
		t.Fatalf("default facts = %+v", result.Facts)
	}
	if result.Facts[0].Confidence != 0.9 {
		t.Fatalf("confidence = %v", result.Facts[0].Confidence)
	}
}

// Optional fact dimensions relax the schema, so the model may leave the four
// descriptive dimensions out instead of spelling "N/A".
func TestOptionalFactDimensionsRelaxTheSchema(t *testing.T) {
	empty := map[string]any{"facts": []any{}}
	stub, _ := extractWithMode(t, Options{Mode: ModeVerbose, OptionalFactDimensions: true}, empty)
	required := stub.lastReq.ResponseSchema.Properties["facts"].Items.Required
	for _, name := range required {
		switch name {
		case "when", "where", "who":
			t.Fatalf("optional dimensions must not be required, got %v", required)
		}
	}
	if !strings.Contains(stub.lastReq.Messages[0].Content, "OPTIONAL FIELDS") {
		t.Fatalf("optional-dimensions prompt section missing:\n%s", stub.lastReq.Messages[0].Content)
	}
}
