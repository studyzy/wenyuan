package reflect

import (
	"errors"
	"fmt"
	"strings"
)

// ErrDocumentSectionsInvalid reports a `done` document that did not match the
// schema the model was handed. It is not coerced: a block that arrives as an
// object is the model writing to a shape it invented, and there is no reading of
// that payload we can trust. Guessing one is how a Python repr once reached
// stored content; refusing it and re-asking is the fix.
var ErrDocumentSectionsInvalid = errors.New("done document did not match the required shape")

// maxDocumentRejections bounds how many times a `done` document the shape
// refuses is fed back before the run fails. One retry is what the observed cases
// need — the shape error is specific and the content is already written — and
// more than that is a model that will not comply.
const maxDocumentRejections = 1

// documentRejectionPrompt is the instruction that rides with a rejected
// document, so the model knows the shape it must emit instead.
const documentRejectionPrompt = `Your document did not match the required shape, so it was discarded. ` +
	`Call done() again with the same content, fixing every problem listed below. ` +
	`Required shape: {"sections": [{"heading": "...", "level": 2, "blocks": ["markdown string", "markdown string"]}]}.`

// doneDocumentAnswer reads a document-mode `done` call into the markdown to
// store. The model states either a structured `document` or a plain `answer`; a
// `document` wins when both are present.
func doneDocumentAnswer(input map[string]any) (string, error) {
	if raw, ok := input["document"]; ok {
		if document, ok := raw.(map[string]any); ok {
			return renderDoneDocument(document)
		}
		if raw != nil {
			// A document was sent but is not one. Falling through to the answer
			// would fail a whole refresh over a shape the model fixes when told,
			// so it is classified as a rejection and re-asked.
			return "", fmt.Errorf("%w: document: expected an object with a 'sections' array, got %T", ErrDocumentSectionsInvalid, raw)
		}
	}
	answer := strings.TrimSpace(stringArg(input, "answer"))
	if answer == "" {
		return "", errors.New("the done call carried no usable answer and no decodable document object")
	}
	return answer, nil
}

// renderDoneDocument builds the markdown a `done` document renders to.
//
// Normalising, but not guessing: a missing heading or an out-of-range level has
// one correct reading and nothing is lost rewriting it. A block that is not a
// string is refused, because there is no correct reading of it to pick.
func renderDoneDocument(payload map[string]any) (string, error) {
	// A one-section document is the shape a model most often flattens: it emits
	// the section itself, with no `sections` array around it. Every fact is
	// present and correctly structured, so take it as the single section it is.
	if _, ok := payload["sections"]; !ok {
		if _, ok := payload["blocks"].([]any); ok {
			payload = map[string]any{"sections": []any{payload}}
		}
	}
	rawSections, _ := payload["sections"].([]any)
	if len(rawSections) == 0 {
		return "", fmt.Errorf("%w: expected a non-empty 'sections' array", ErrDocumentSectionsInvalid)
	}

	rendered := make([]string, 0, len(rawSections))
	var problems []string
	for i, rawSection := range rawSections {
		section, ok := rawSection.(map[string]any)
		if !ok {
			problems = append(problems, fmt.Sprintf("sections[%d]: expected an object with heading/level/blocks, got %T", i, rawSection))
			continue
		}
		rawBlocks, _ := section["blocks"].([]any)
		for j, rawBlock := range rawBlocks {
			if _, ok := rawBlock.(string); !ok {
				problems = append(problems, fmt.Sprintf("sections[%d].blocks[%d]: expected a markdown string, got %T — write the markdown itself, not an object wrapping it", i, j, rawBlock))
			}
		}
		if len(problems) > 0 {
			continue
		}
		rendered = append(rendered, renderDoneSection(section, rawBlocks))
	}
	if len(problems) > 0 {
		return "", fmt.Errorf("%w: %s", ErrDocumentSectionsInvalid, strings.Join(problems, "; "))
	}

	body := make([]string, 0, len(rendered))
	for _, part := range rendered {
		if strings.TrimSpace(part) != "" {
			body = append(body, part)
		}
	}
	if len(body) == 0 {
		return "", fmt.Errorf("%w: the sections rendered to nothing", ErrDocumentSectionsInvalid)
	}
	return strings.Join(body, "\n\n"), nil
}

// renderDoneSection renders one section: its heading when it has one, then its
// blocks, blank-line separated.
func renderDoneSection(section map[string]any, rawBlocks []any) string {
	parts := make([]string, 0, len(rawBlocks)+1)
	if heading := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(stringArg(section, "heading")), "#")); heading != "" {
		level := intArg(section, "level", 2)
		if level < 1 {
			level = 1
		}
		if level > 6 {
			level = 6
		}
		parts = append(parts, strings.Repeat("#", level)+" "+heading)
	}
	for _, rawBlock := range rawBlocks {
		block, _ := rawBlock.(string)
		if strings.TrimSpace(block) == "" {
			continue
		}
		parts = append(parts, block)
	}
	return strings.Join(parts, "\n\n")
}
