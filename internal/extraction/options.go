package extraction

import (
	"encoding/base64"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/provider"
)

// Options carries the per-call settings the retain pipeline supplies for one
// extraction. Every field is optional: the zero value renders the historical
// prompt and disables date inference, so an unwired caller keeps its behaviour.
type Options struct {
	// Mode is the bank's retain_extraction_mode: concise (the default),
	// verbose, custom, verbatim or chunks. It selects the prompt and response
	// schema. An empty or unrecognised value is the concise default.
	Mode string
	// Mission is the bank's retain mission, rendered as the FOCUS preamble of
	// the user message so the model knows what this bank is for. It is
	// deliberately not part of the system prompt, matching the reference's
	// bank-agnostic cached prefix.
	Mission string
	// Instructions are the bank's custom retain instructions. They are rendered
	// only in custom mode, which is when the reference consults them.
	Instructions string
	// Strategy names the retain strategy the request selected. The reference
	// uses it to group work, not in the extraction prompt, so it is carried but
	// not rendered.
	Strategy string
	// ReferenceTime resolves relative date phrases ("last week") to an absolute
	// day. The zero value leaves every relative phrase unresolved.
	ReferenceTime time.Time
	// OptionalFactDimensions lets the four descriptive dimensions be omitted
	// instead of spelled out as "N/A". It relaxes the dimensions in the prompt
	// and the schema.
	OptionalFactDimensions bool
	// Vocabulary is the bank's controlled label vocabulary. When it is set the
	// prompt describes it and the schema gains a 'labels' object the model must
	// fill; a nil or empty vocabulary leaves both exactly as they were.
	Vocabulary *entitylabels.Vocabulary
	// Attachments are the item's inline attachments' decoded bytes. They are
	// interleaved back into the user message where their placeholders stand, so
	// the model reads an image in the position the caller wrote it.
	Attachments []Attachment
	// ExtractCausalLinks turns causal-relation extraction on or off. A nil
	// value keeps the reference's shipped default (on): the prompt asks for
	// causal_relations, the schema declares the field, and the model's answer
	// is parsed. False drops the ask and the parse, so a disabled switch yields
	// no causal relations at all, exactly as the reference threads the option.
	ExtractCausalLinks *bool
}

// causalLinksEnabled reports whether the extraction should ask for and parse
// causal relations. An unset option is the reference's default (on).
func causalLinksEnabled(opts Options) bool {
	return opts.ExtractCausalLinks == nil || *opts.ExtractCausalLinks
}

// Attachment is one inline attachment carried into an extraction call: the
// bytes plus the short id its placeholder names.
type Attachment struct {
	ShortID   string
	MediaType string
	// Kind is "image" or "file", the caller's own distinction.
	Kind string
	Data []byte
}

// attachmentPlaceholderRe matches a well-formed inline-attachment placeholder
// and captures its short id (submatch indexes 2,3).
var attachmentPlaceholderRe = regexp.MustCompile(`\x{27e6}hs-att:([0-9a-fA-F]+)\x{27e7}`)

// promptParts turns placeholder text back into an ordered multimodal message,
// with each image sitting exactly where its placeholder stood. It returns nil
// when there is nothing to interleave, so a text-only chunk keeps the
// byte-identical string form.
func promptParts(text string, attachments []Attachment) []provider.ContentPart {
	if len(attachments) == 0 || !attachmentPlaceholderRe.MatchString(text) {
		return nil
	}
	byID := make(map[string]Attachment, len(attachments))
	for _, attachment := range attachments {
		byID[attachment.ShortID] = attachment
	}
	parts := make([]provider.ContentPart, 0)
	var pending strings.Builder
	flush := func() {
		if pending.Len() == 0 {
			return
		}
		joined := pending.String()
		pending.Reset()
		if strings.TrimSpace(joined) != "" {
			parts = append(parts, provider.ContentPart{Type: "text", Text: joined})
		}
	}
	cursor := 0
	for _, match := range attachmentPlaceholderRe.FindAllStringSubmatchIndex(text, -1) {
		pending.WriteString(text[cursor:match[0]])
		attachment, ok := byID[text[match[2]:match[3]]]
		if !ok {
			pending.WriteString("[attachment unavailable]")
		} else if attachment.Kind == "" || attachment.Kind == "image" {
			flush()
			parts = append(parts, provider.ContentPart{Type: "image_url", ImageURL: attachmentDataURI(attachment)})
		}
		cursor = match[1]
	}
	pending.WriteString(text[cursor:])
	flush()
	if len(parts) == 0 {
		return nil
	}
	return parts
}

// attachmentDataURI renders an attachment's bytes as the data URI an image_url
// part carries.
func attachmentDataURI(attachment Attachment) string {
	return "data:" + attachment.MediaType + ";base64," + base64.StdEncoding.EncodeToString(attachment.Data)
}

// composeExtractionPrompt renders the fact-extraction system prompt for the
// configured mode. The bank's mission is NOT part of it: the reference keeps the
// cached prefix bank-agnostic and carries the mission in the per-request user
// message instead (see buildUserMessage). Custom instructions are substituted
// into the prompt in custom mode, which is the only mode the reference consults
// them in; a vocabulary adds its entity-labels block, and optional-dimensions
// mode appends its section.
func composeExtractionPrompt(opts Options) string {
	mode := resolveMode(opts.Mode)
	prompt := factExtractionPrompt
	switch mode {
	case ModeVerbose:
		prompt = verboseExtractionPrompt
	case ModeVerbatim:
		prompt = verbatimExtractionPrompt
	case ModeCustom:
		if instructions := strings.TrimSpace(opts.Instructions); instructions != "" {
			prompt = customFactExtractionPrompt(instructions)
		}
	}
	// Every non-verbatim prompt ends with the causal-relationships section, so a
	// disabled switch drops exactly that tail: the reference appends the section
	// to the base prompt only when causal extraction is on, which is the same
	// string a suffix trim produces. Verbatim never carries the section, so the
	// trim is a no-op there.
	if !causalLinksEnabled(opts) {
		prompt = strings.TrimSuffix(prompt, causalRelationshipsSection)
	}
	var builder strings.Builder
	builder.WriteString(prompt)
	if !opts.Vocabulary.Empty() {
		builder.WriteString(labelsPromptSection(opts.Vocabulary))
	}
	if opts.OptionalFactDimensions && mode != ModeVerbatim {
		builder.WriteString(optionalDimensionsSection)
	}
	return builder.String()
}

// retainMissionPreamble renders the bank's retain mission as the FOCUS block the
// reference prepends to the extraction user message. It is kept out of the
// cached system prompt so one prompt serves every bank. Returns "" when unset.
func retainMissionPreamble(mission string) string {
	trimmed := strings.TrimSpace(mission)
	if trimmed == "" {
		return ""
	}
	return "══════════════════════════════════════════════════════════════════════════\n" +
		"FOCUS — What to retain for this bank (takes priority over the general guidelines)\n" +
		"══════════════════════════════════════════════════════════════════════════\n\n" +
		trimmed + "\n\n"
}

// buildUserMessage renders the reference's per-chunk extraction user message:
// the bank's mission preamble (if any), then the "Extract facts from the
// following chunk." instruction and the chunk's position, event date, context
// and content. This is where the parity suite's extract_facts anchor lives.
func buildUserMessage(text string, opts Options) string {
	eventDate := "Unknown"
	if !opts.ReferenceTime.IsZero() {
		eventDate = fmt.Sprintf("%s (%s)",
			opts.ReferenceTime.Format("Monday, January 02, 2006"),
			opts.ReferenceTime.Format("2006-01-02T15:04:05"))
	}
	return fmt.Sprintf(`%sExtract facts from the following chunk.

Chunk: 1/1
Event Date: %s
Context: none

Content:
%s`, retainMissionPreamble(opts.Mission), eventDate, text)
}
