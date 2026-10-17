package extraction

import (
	"fmt"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/provider"
)

// DefaultLanguageRule is the extraction pipeline's preserve-the-source-language
// rule, the half of the output-language setting that stands when the deployment
// configures no language. The prompt preview reports it as the
// llm_output_language block in that case.
const DefaultLanguageRule = extractionLanguageRule

// PlaceholderContext stands in for a document's context in the preview, so the
// rendered user message shows the line a real retain would carry.
const PlaceholderContext = "«context supplied with the document»"

// CausalSection is the extraction prompt's causal-relationships section, which
// the retain_extract_causal_links switch appends. Exported so the prompt preview
// can report it as its own block.
const CausalSection = causalRelationshipsSection

// OptionalDimensionsSection is the section the retain_optional_fact_dimensions
// switch appends.
const OptionalDimensionsSection = optionalDimensionsSection

// LabelsPromptSection renders the entity-labels section for a vocabulary, or ""
// when none is configured. Exported so the prompt preview can split the section
// around the line the free-form flag decides.
func LabelsPromptSection(vocab *entitylabels.Vocabulary, freeForm bool) string {
	if vocab == nil || vocab.Empty() {
		return ""
	}
	return labelsPromptSection(vocab, freeForm)
}

// PreviewSchemaFor is the response schema a retain with these options declares,
// so the prompt preview can report it without building the messages twice.
func PreviewSchemaFor(opts Options) *provider.Schema { return schemaFor(opts) }

// PreviewMissionPreamble renders the bank's retain mission as the FOCUS block
// the user message prepends, or "" when none is set.
func PreviewMissionPreamble(mission string) string { return retainMissionPreamble(mission) }

// PreviewSystemPrompt renders the system prompt a retain would send for this
// bank, with the deployment's output-language handling applied the way the
// reference assembles it: an explicit language drops the preserve-the-source
// rule and is appended as the directive at the end.
func PreviewSystemPrompt(opts Options, outputLanguage string) string {
	text := composeExtractionPrompt(opts)
	if strings.TrimSpace(outputLanguage) == "" {
		return text
	}
	// The default rule sits directly after the shared header; removing it here is
	// exactly how the reference's default_language_section drops it.
	text = strings.Replace(text, extractionLanguageRule+"\n\n", "", 1)
	return text + provider.OutputLanguageDirective(outputLanguage)
}

// PreviewUserMessage renders the per-chunk user message the preview reports. It
// calls the same builder the real run does, but with the fixed runtime
// placeholders (the chunk text and its context) so the answer depends on the
// bank alone, and with the current time as the event date the way the reference
// passes utcnow().
func PreviewUserMessage(opts Options) string {
	return previewUserMessage(opts, time.Now().UTC(), PlaceholderContext)
}

// previewUserMessage is buildUserMessage with a caller-supplied event date and
// context, so the preview can pin both while a real call threads the item's own
// values through.
func previewUserMessage(opts Options, eventDate time.Time, context string) string {
	eventDateStr := "Unknown"
	if !eventDate.IsZero() {
		eventDateStr = fmt.Sprintf("%s (%s)", eventDate.Format("Monday, January 02, 2006"), pythonISO(eventDate))
	}
	if strings.TrimSpace(context) == "" {
		context = "none"
	}
	return fmt.Sprintf(`%sExtract facts from the following chunk.

Chunk: 1/1
Event Date: %s
Context: %s

Content:
%s`, retainMissionPreamble(opts.Mission), eventDateStr, context, PlaceholderContent)
}

// pythonISO renders a UTC instant the way Python's datetime.isoformat() does: a
// "+00:00" offset, and microseconds included exactly when they are non-zero.
func pythonISO(t time.Time) string {
	t = t.UTC()
	base := t.Format("2006-01-02T15:04:05")
	if micro := t.Nanosecond() / 1000; micro != 0 {
		return fmt.Sprintf("%s.%06d+00:00", base, micro)
	}
	return base + "+00:00"
}
