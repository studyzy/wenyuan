package projectmerge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The dedupe pass: how a team bank is filled from its members' facts.
//
// Merging used to mean "ask the model for the project's complete fact list". That
// is a summarisation, and a summary is lossy by construction: a project with 1940
// member facts produced a team bank of 121, because the model was asked to merge
// duplicates and the shortest way to do that is to compress. The team bank is how
// members read each other's work, so it must carry the work.
//
// The pass is therefore deterministic about content and model-driven only about
// duplication:
//
//   - every member fact becomes a team fact, with its own text, unchanged;
//   - the model is asked only which of a batch's facts say the same thing as an
//     already-present fact or as another fact in the batch;
//   - a fact the model calls redundant is still written — it just points at the
//     fact that supersedes it, so the ledger keeps the provenance and an operator
//     can still find it. Nothing is ever deleted by the model's judgement.
//
// Nothing here calls a database. The round's reads and writes belong to the merge
// store; these are the pure rules.

// Batch is one group of member facts the dedupe pass considers together.
type Batch struct {
	// Present are the team facts already in the bank, with the normalized text
	// the model compares against.
	Present []TeamFact
	// Fresh are the new member facts this batch asks about.
	Fresh []MemberFact
}

// Answer is one duplication the model reported.
type Answer struct {
	// Fresh is the index into the batch's Fresh slice of the fact the model
	// judged redundant.
	Fresh int
	// DuplicateOf is the index into the batch's Present slice of the confirmed
	// fact this one repeats. A negative value means "repeats another fresh fact"
	// rather than one already in the bank.
	DuplicateOf int
}

// ErrAnswerShape reports a reply that is not the expected JSON.
var ErrAnswerShape = errors.New("dedupe answer is not the expected JSON")

// answerEnvelope is the wire shape the prompt asks for.
type answerEnvelope struct {
	Duplicates []struct {
		Fresh       int `json:"fresh"`
		DuplicateOf int `json:"duplicate_of"`
	} `json:"duplicates"`
}

// ParseAnswer decodes the model's reply.
//
// The reply is a list of duplicates, and an empty list is a valid answer: unlike
// the old merge, "nothing here repeats" is a normal outcome and must not be
// mistaken for a broken model. An index outside the batch is a shape failure —
// a ledger row pointing at nothing is worse than no row.
func ParseAnswer(raw string, freshCount, presentCount int) ([]Answer, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty reply", ErrAnswerShape)
	}
	if strings.HasPrefix(trimmed, "```") {
		if start := strings.Index(trimmed, "\n"); start >= 0 {
			trimmed = trimmed[start+1:]
		}
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	var envelope answerEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		relaxed := tenant.RelaxJSON(trimmed)
		if relaxed != trimmed {
			if err := json.Unmarshal([]byte(relaxed), &envelope); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrAnswerShape, err)
			}
		} else if body, ok := firstJSONObject(trimmed); ok {
			if err := json.Unmarshal([]byte(tenant.RelaxJSON(body)), &envelope); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrAnswerShape, err)
			}
		} else {
			return nil, fmt.Errorf("%w: %v", ErrAnswerShape, err)
		}
	}
	out := make([]Answer, 0, len(envelope.Duplicates))
	for i, dup := range envelope.Duplicates {
		if dup.Fresh < 0 || dup.Fresh >= freshCount {
			return nil, fmt.Errorf("%w: duplicate %d names fresh %d, outside the %d inputs",
				ErrAnswerShape, i, dup.Fresh, freshCount)
		}
		if dup.DuplicateOf >= presentCount {
			// The model named a P index the batch does not have — an off-by-one
			// against an empty bank is the common case. The claim is unusable but
			// harmless: every fact is written either way, so an unusable label
			// costs a repeated line, never a fact. Ignore it rather than fail the
			// round.
			continue
		}
		out = append(out, Answer{Fresh: dup.Fresh, DuplicateOf: dup.DuplicateOf})
	}
	return out, nil
}

// PlannedFact is one team fact to write.
type PlannedFact struct {
	// Fact is the member fact this team fact carries.
	Fact MemberFact
	// DuplicateOf is the merge key of the fact this one repeats, empty when it
	// repeats nothing. A duplicate is still written: the row is what lets an
	// operator find the member's original contribution, and what the ledger
	// points at.
	DuplicateOf string
}

// Plan is what a batch writes: every fresh fact, with the fact it repeats.
type Plan struct {
	// Facts are the facts to write, in the order the batch read them. Each one
	// is the member's own text: the pass never rewrites a fact.
	Facts []PlannedFact
}

// PlanBatch turns a batch and the model's duplicates into the batch's write.
//
// Every fact in the batch is planned. The model's judgement can only set a fact's
// DuplicateOf; it cannot remove the fact, which is what makes the pass
// non-lossy — the property the summary it replaced could not offer.
func PlanBatch(batch Batch, answers []Answer) Plan {
	// A key that appeared more than once in the batch is a duplicate of the
	// first occurrence even when the model says nothing: the same text twice is
	// the same fact twice, and that much needs no model.
	firstByKey := make(map[string]int, len(batch.Fresh))
	for i, fact := range batch.Fresh {
		key := MergeKey(fact.Text)
		if _, seen := firstByKey[key]; !seen {
			firstByKey[key] = i
		}
	}
	superseded := make(map[int]string, len(answers))
	for _, answer := range answers {
		if answer.Fresh < 0 || answer.Fresh >= len(batch.Fresh) {
			continue
		}
		if answer.DuplicateOf >= 0 && answer.DuplicateOf < len(batch.Present) {
			superseded[answer.Fresh] = batch.Present[answer.DuplicateOf].MergeKey
			continue
		}
		if answer.DuplicateOf >= 0 {
			// An index the batch has no present fact for; the label cannot be
			// honoured, so it is dropped rather than pointed at nothing.
			continue
		}
		// A negative index means "repeats another fact in this batch". The plan
		// already records that when the texts are identical; a model that says so
		// about two differently-worded facts is making a judgement this pass does
		// not act on, because it would mean choosing which of two facts a member
		// wrote to hide — and both are still written below.
		superseded[answer.Fresh] = ""
	}
	plan := Plan{Facts: make([]PlannedFact, 0, len(batch.Fresh))}
	for i, fact := range batch.Fresh {
		duplicateOf := ""
		if first, ok := firstByKey[MergeKey(fact.Text)]; ok && first != i {
			duplicateOf = MergeKey(batch.Fresh[first].Text)
		} else if key, ok := superseded[i]; ok {
			duplicateOf = key
		}
		plan.Facts = append(plan.Facts, PlannedFact{Fact: fact, DuplicateOf: duplicateOf})
	}
	return plan
}

// Prompt builds the dedupe pass's messages.
//
// The model is shown the facts already in the team bank (P) and this batch's new
// facts (F), and is asked one question: which new facts say the same thing as one
// of the others. It is deliberately not asked to rewrite, summarise or merge any
// text — the server writes every fact itself.
func Prompt(present []TeamFact, fresh []MemberFact) []provider.Message {
	var system strings.Builder
	system.WriteString("You find repeated facts in a team's shared knowledge base.\n")
	system.WriteString("You are given the facts the bank already holds and a batch of new facts from team members.\n")
	system.WriteString("Report only the new facts whose meaning is already stated by another fact.\n\n")
	system.WriteString("Rules:\n")
	system.WriteString("- Reply with JSON only, no prose, no code fences, no comments and no trailing commas.\n")
	system.WriteString("- The shape is {\"duplicates\": [{\"fresh\": 3, \"duplicate_of\": 1}]}.\n")
	system.WriteString("- \"fresh\" is the F index of the repeated new fact.\n")
	system.WriteString("- \"duplicate_of\" is the P index of the existing fact that already states it, ")
	system.WriteString("or -1 when it repeats another F fact in this batch.\n")
	system.WriteString("- Two facts are duplicates only when one says nothing the other does not. ")
	system.WriteString("Different specific values (ports, paths, versions, names, limits) mean two different facts.\n")
	system.WriteString("- When in doubt, report nothing: an unreported duplicate costs a repeated line, ")
	system.WriteString("a reported one hides a fact.\n")
	system.WriteString("- Never rewrite, summarise or merge any text. Report indexes only.\n")
	system.WriteString("- If nothing repeats, reply {\"duplicates\": []}.\n")

	var user strings.Builder
	user.WriteString("Facts the bank already holds:\n")
	if len(present) == 0 {
		user.WriteString("(none yet)\n")
	}
	writeFactsByType(&user, "P", presentNumbered(present))
	user.WriteString("\nNew facts from members:\n")
	if len(fresh) == 0 {
		user.WriteString("(none)\n")
	}
	user.WriteString("member ")
	user.WriteString(freshTenant(fresh))
	user.WriteString(":\n")
	writeFactsByType(&user, "F", freshNumbered(fresh))
	user.WriteString("\nReply with the repeated new facts as JSON.")

	return []provider.Message{
		{Role: provider.RoleSystem, Content: system.String()},
		{Role: provider.RoleUser, Content: user.String()},
	}
}

// presentNumbered and freshNumbered adapt the two shapes to the printer.
func presentNumbered(facts []TeamFact) []numberedFact {
	out := make([]numberedFact, 0, len(facts))
	for i, fact := range facts {
		out = append(out, numberedFact{index: i, text: fact.Text, factType: fact.FactType})
	}
	return out
}

func freshNumbered(facts []MemberFact) []numberedFact {
	out := make([]numberedFact, 0, len(facts))
	for i, fact := range facts {
		out = append(out, numberedFact{index: i, text: fact.Text, factType: fact.FactType})
	}
	return out
}

func freshTenant(facts []MemberFact) string {
	if len(facts) == 0 {
		return "(none)"
	}
	return facts[0].TenantID
}
