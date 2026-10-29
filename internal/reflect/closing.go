// The closing step: after the loop stops, the answer is written by one more
// model call that asks for a `done` payload in the conversation the evidence
// was gathered in, and the citations it returns narrow what the run reports it
// read.

package reflect

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
)

// closingStep asks for the answer as a `done` call in the conversation the
// evidence was gathered in (the reference's closing step): the free turn's own
// wording is discarded, and this call — one extra model call under the
// `closing_done` scope — writes the answer against the question and the full
// evidence set. It pins `done` by tool_choice so the model makes the call
// rather than writing prose.
//
// It falls back to the standalone synthesis when the provider fails or the
// model will not make the call ("" returned, no error). An answer that arrives
// empty, or a document still past its shape after the bounded re-asks, fails
// the run instead.
func (l *Loop) closingStep(ctx context.Context, outcome *Outcome) (string, error) {
	for attempt := 0; ; attempt++ {
		response, err := l.callModel(ctx, "closing_done", provider.CompletionRequest{
			Messages:   l.closingMessages(ctx),
			Tools:      ToolDefinitions(l.cfg.AnswerAsDocument),
			ToolChoice: toolNamed(ToolDone),
			Scope:      "reflect",
			MaxTokens:  l.synthesisMaxTokens(),
		})
		outcome.Usage = addUsage(outcome.Usage, response.Usage)
		if err != nil {
			return "", nil
		}
		done, ok := l.closingDoneCall(response)
		if !ok {
			return "", nil
		}
		if l.cfg.AnswerAsDocument {
			answer, docErr := doneDocumentAnswer(done.Input)
			if docErr != nil {
				// A document the shape refused is re-asked on the same
				// conversation with the reasons attached; out of attempts, the
				// run fails rather than store something nobody can trust.
				if errors.Is(docErr, ErrDocumentSectionsInvalid) && attempt < maxDocumentRejections {
					l.documentFeedback = append(l.documentFeedback, docErr.Error())
					continue
				}
				return "", docErr
			}
			l.documentAnswer = answer
			l.captureCitations(done.Input)
			return answer, nil
		}
		// The closing done is a structured tool call, so its answer field is
		// the answer. One that carried nothing usable fails the run: standing
		// in a placeholder would read as a real answer downstream.
		answer := stringArg(done.Input, "answer")
		if answer == "" {
			return "", ErrNoAnswer
		}
		l.captureCitations(done.Input)
		return answer, nil
	}
}

// closingDoneCall finds the `done` call in the closing response, resolving the
// alias tokens in its arguments back to the identifiers they stand for.
func (l *Loop) closingDoneCall(response provider.CompletionResult) (ToolCall, bool) {
	for _, call := range response.ToolCalls {
		if NormalizeToolName(call.Name) != ToolDone {
			continue
		}
		input := map[string]any{}
		if len(call.Arguments) > 0 {
			var decoded map[string]any
			if err := json.Unmarshal(call.Arguments, &decoded); err == nil {
				input = decoded
			}
		}
		return ToolCall{ID: call.ID, Name: ToolDone, Input: l.resolveAliases(input)}, true
	}
	return ToolCall{}, false
}

// closingMessages builds the closing request's conversation: the turns the loop
// ran, then the done-request prompt, then — on a re-ask — the reasons the last
// document was refused, so the model fixes the shape instead of repeating it.
func (l *Loop) closingMessages(ctx context.Context) []provider.Message {
	messages := l.messages(ctx)
	messages = append(messages, provider.Message{
		Role:    provider.RoleUser,
		Content: buildDoneRequestPrompt(l.cfg.Question, l.cfg.MaxTokens),
	})
	if len(l.documentFeedback) > 0 {
		messages = append(messages, provider.Message{
			Role:    provider.RoleUser,
			Content: documentRejectionPrompt + "\nFixing: " + strings.Join(l.documentFeedback, "; "),
		})
	}
	return messages
}

// captureCitations records the identifiers a closing done call cited, per
// collection. The reference validates them against the ids the tools actually
// returned before its based_on reads them; the membership filter below applies
// the same rule.
func (l *Loop) captureCitations(input map[string]any) {
	l.citedMemoryIDs = idSet(stringListArg(input, "memory_ids"))
	l.citedModelIDs = idSet(stringListArg(input, "mental_model_ids"))
	l.citedObservationID = idSet(stringListArg(input, "observation_ids"))
}

// idSet turns a cited id list into a membership set.
func idSet(ids []string) map[string]bool {
	if len(ids) == 0 {
		return nil
	}
	out := make(map[string]bool, len(ids))
	for _, id := range ids {
		out[id] = true
	}
	return out
}

// applyCitedMembership keeps only the evidence the closing done call cited,
// per collection: a done that cited memory ids narrows the memories to them,
// and one that cited nothing — or cited only identifiers nothing retrieved —
// keeps everything. Perspectives prefer the full content a read returned, which
// the evidence already carries.
func (l *Loop) applyCitedMembership() {
	memories, models, observations := citedAvailable(l.evidence)
	memoryCited := intersectIDs(l.citedMemoryIDs, memories)
	modelCited := intersectIDs(l.citedModelIDs, models)
	observationCited := intersectIDs(l.citedObservationID, observations)
	if len(memoryCited) == 0 && len(modelCited) == 0 && len(observationCited) == 0 {
		return
	}
	kept := make([]Evidence, 0, len(l.evidence))
	for _, item := range l.evidence {
		switch item.Kind {
		case "mental_model":
			if len(modelCited) > 0 && !modelCited[item.ID] {
				continue
			}
		case "observation":
			if len(observationCited) > 0 && !observationCited[item.ID] {
				continue
			}
		default:
			if len(memoryCited) > 0 && !memoryCited[item.ID] {
				continue
			}
		}
		kept = append(kept, item)
	}
	l.evidence = kept
}

// citedAvailable indexes the retrieved evidence's ids by citation collection.
func citedAvailable(evidence []Evidence) (memories, models, observations map[string]bool) {
	memories, models, observations = map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, item := range evidence {
		switch item.Kind {
		case "mental_model":
			models[item.ID] = true
		case "observation":
			observations[item.ID] = true
		default:
			memories[item.ID] = true
		}
	}
	return memories, models, observations
}

// intersectIDs keeps the cited ids that name retrieved evidence.
func intersectIDs(cited, available map[string]bool) map[string]bool {
	if len(cited) == 0 {
		return nil
	}
	out := make(map[string]bool, len(cited))
	for id := range cited {
		if available[id] {
			out[id] = true
		}
	}
	return out
}
