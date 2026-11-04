// Package projectmerge holds the merge rules that need no database: how a team
// fact is identified, what a round's answer changes, how the round is asked, and
// how the answer is read back.
//
// Everything here is a pure function. The round itself — reading members, taking
// the lock, calling the model, writing the result — belongs to the merge store
// and the maintenance job, which lets these rules be tested without PostgreSQL,
// a model, or a network.
package projectmerge

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// Normalize folds away the differences that must not create two team facts.
func Normalize(text string) string { return tenant.Normalize(text) }

// MergeKey is the identity of a team fact: the hex SHA-256 of its normalized
// text. Two facts with the same key are the same fact and keep their row.
func MergeKey(text string) string { return tenant.MergeKey(text) }

// DroppedMergeKey marks a source the model folded away. It is re-exported so a
// caller writing a ledger row needs one import.
const DroppedMergeKey = tenant.DroppedMergeKey

// The fact kinds a round merges. They mirror storage's vocabulary; the strings
// are the column values, so they are spelled once here and never derived.
const (
	FactTypeWorld       = "world"
	FactTypeExperience  = "experience"
	FactTypeObservation = "observation"
)

// TeamFact is one fact the team bank already holds.
type TeamFact struct {
	// ID is the stored unit's id. It is kept across rounds when the fact's
	// wording does not change, which is what keeps the fact's observations
	// attached.
	ID string
	// Text is the stored text.
	Text string
	// MergeKey is the fact's key; it is derived from Text and carried so a
	// caller does not recompute it per comparison.
	MergeKey string
	// FactType is the stored fact type: world, experience or observation.
	FactType string
}

// MemberFact is one member's fact entering this round.
type MemberFact struct {
	// TenantID names the member the fact came from.
	TenantID string
	// UnitID is the member's own unit id, which is what the ledger records.
	UnitID string
	// Text is the fact's text.
	Text string
	// FactType is the member fact's type, which the prompt groups by and the
	// answer inherits.
	FactType string
}

// Answer is one team fact the model produced, with the inputs it came from.
type Answer struct {
	// Text is the fact's text, as the model wrote it.
	Text string
	// Sources are indexes into the round's Fresh slice: the member facts this
	// team fact was made from. The indexes are what lets the ledger record a
	// provenance the server determined rather than one the model asserted.
	Sources []int
}

// Errors ParseAnswer reports. They are distinct because they mean different
// things to the caller: a shape failure is a broken model, while an empty answer
// is a well-formed claim that the project holds no facts — which would delete
// the team's content and so is refused unless the round had none to begin with.
var (
	// ErrAnswerShape reports a reply that is not the expected JSON.
	ErrAnswerShape = errors.New("merge answer is not the expected JSON")
	// ErrAnswerEmpty reports a well-formed reply carrying no facts.
	ErrAnswerEmpty = errors.New("merge answer carries no facts")
)

// answerEnvelope is the wire shape the prompt asks for.
type answerEnvelope struct {
	Facts []struct {
		Text    string `json:"text"`
		Sources []int  `json:"sources"`
	} `json:"facts"`
}

// ParseAnswer decodes the model's reply.
//
// freshCount bounds the source indexes: an index outside the round's inputs is a
// shape failure rather than a silently dropped provenance, because a ledger row
// pointing at nothing is worse than no row at all.
//
// An empty answer is its own error. It is only acceptable when the round had
// nothing before it — otherwise it would empty the team's content on a model
// hiccup.
func ParseAnswer(raw string, freshCount int) ([]Answer, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: empty reply", ErrAnswerShape)
	}
	// A model that wraps its JSON in a fenced block is common enough that
	// refusing it would spend a round on a formatting quibble.
	if strings.HasPrefix(trimmed, "```") {
		if start := strings.Index(trimmed, "\n"); start >= 0 {
			trimmed = trimmed[start+1:]
		}
		trimmed = strings.TrimSuffix(strings.TrimSpace(trimmed), "```")
		trimmed = strings.TrimSpace(trimmed)
	}
	var envelope answerEnvelope
	if err := json.Unmarshal([]byte(trimmed), &envelope); err != nil {
		// A model that annotates its JSON with comments or leaves a trailing
		// comma is the same formatting quibble as a code fence: read the JSON
		// rather than spend the round.
		if relaxed := tenant.RelaxJSON(trimmed); relaxed != trimmed {
			if err := json.Unmarshal([]byte(relaxed), &envelope); err != nil {
				return nil, fmt.Errorf("%w: %v", ErrAnswerShape, err)
			}
		} else {
			return nil, fmt.Errorf("%w: %v", ErrAnswerShape, err)
		}
	}
	if envelope.Facts == nil {
		return nil, fmt.Errorf("%w: no facts member", ErrAnswerShape)
	}
	if len(envelope.Facts) == 0 {
		return nil, ErrAnswerEmpty
	}
	answers := make([]Answer, 0, len(envelope.Facts))
	for i, fact := range envelope.Facts {
		text := Normalize(fact.Text)
		if text == "" {
			return nil, fmt.Errorf("%w: fact %d has no text", ErrAnswerShape, i)
		}
		for _, source := range fact.Sources {
			if source < 0 || source >= freshCount {
				return nil, fmt.Errorf("%w: fact %d names source %d, outside the %d inputs",
					ErrAnswerShape, i, source, freshCount)
			}
		}
		answers = append(answers, Answer{Text: text, Sources: fact.Sources})
	}
	return answers, nil
}

// MergeResult is what a round's answer changes in the team bank.
type MergeResult struct {
	// Keep are the team facts whose wording survived, in previous order. Their
	// ids are unchanged, so their observations survive with them.
	Keep []TeamFact
	// Remove are the team facts the answer no longer contains.
	Remove []TeamFact
	// Insert are the facts the answer adds.
	Insert []TeamFact
	// Sources maps each surviving or inserted fact's key to the indexes into the
	// round's Fresh slice that support it. A key with no entry has no source this
	// round — it survived on wording alone.
	Sources map[string][]int
}

// Diff classifies what a round's answer changes.
//
// Identity is the merge key, never the text: the model is free to re-punctuate a
// fact without the ledger treating it as a new one. When the answer restates a
// fact twice, the first text wins and the sources union — the alternatives are to
// fail a round over a duplicate or to keep one arbitrarily.
//
// fresh carries the round's inputs, because an answer's sources decide the kind a
// new fact is written under: it takes the type most of its sources have, which is
// determined here rather than asked of the model. A fact with no sources carries
// no type of its own and is written as world.
func Diff(previous []TeamFact, answers []Answer, fresh []MemberFact) MergeResult {
	result := MergeResult{Sources: map[string][]int{}}
	seen := make(map[string]bool, len(answers))
	order := make([]string, 0, len(answers))
	byKey := make(map[string]Answer, len(answers))
	for _, answer := range answers {
		key := MergeKey(answer.Text)
		if existing, ok := byKey[key]; ok {
			existing.Sources = unionInts(existing.Sources, answer.Sources)
			byKey[key] = existing
			continue
		}
		byKey[key] = Answer{Text: Normalize(answer.Text), Sources: append([]int(nil), answer.Sources...)}
		seen[key] = true
		order = append(order, key)
	}
	previousKeys := make(map[string]bool, len(previous))
	for _, fact := range previous {
		key := fact.MergeKey
		if key == "" {
			key = MergeKey(fact.Text)
		}
		previousKeys[key] = true
		if seen[key] {
			result.Keep = append(result.Keep, TeamFact{
				ID: fact.ID, Text: fact.Text, MergeKey: key, FactType: fact.FactType,
			})
			result.Sources[key] = byKey[key].Sources
			continue
		}
		result.Remove = append(result.Remove, TeamFact{
			ID: fact.ID, Text: fact.Text, MergeKey: key, FactType: fact.FactType,
		})
	}
	for _, key := range order {
		if previousKeys[key] {
			continue
		}
		answer := byKey[key]
		result.Insert = append(result.Insert, TeamFact{
			Text: answer.Text, MergeKey: key, FactType: factTypeOf(answer.Sources, fresh),
		})
		result.Sources[key] = answer.Sources
	}
	return result
}

// factTypeOf is the type a new team fact is written under: the type most of the
// member facts it was made from carry, world when it cites none.
//
// A majority rather than the first source, because an answer may combine a
// member's world fact with their experience of it; the tie order is fixed so the
// same input always produces the same row. An unknown type from a caller's data
// counts as world, which is what the column's own default would be.
func factTypeOf(sources []int, fresh []MemberFact) string {
	counts := make(map[string]int, 3)
	for _, source := range sources {
		if source < 0 || source >= len(fresh) {
			continue
		}
		factType := fresh[source].FactType
		if factType == "" {
			factType = FactTypeWorld
		}
		counts[factType]++
	}
	best, bestCount := FactTypeWorld, 0
	for _, factType := range []string{FactTypeWorld, FactTypeExperience, FactTypeObservation} {
		if counts[factType] > bestCount {
			best, bestCount = factType, counts[factType]
		}
	}
	return best
}

// unionInts merges two source index lists, keeping first-seen order.
func unionInts(a, b []int) []int {
	seen := make(map[int]bool, len(a)+len(b))
	out := make([]int, 0, len(a)+len(b))
	for _, list := range [][]int{a, b} {
		for _, value := range list {
			if seen[value] {
				continue
			}
			seen[value] = true
			out = append(out, value)
		}
	}
	return out
}

// SourceMergeKeys maps each input index to the merge keys it supports, which is
// what the ledger records: a source is either supporting a team fact or dropped.
//
// An input no surviving fact cites lands under DroppedMergeKey. That distinction
// is what keeps its later deletion from forcing a rebuild: nothing in the team's
// content was made from it.
func (r MergeResult) SourceMergeKeys(freshCount int) map[int][]string {
	out := make(map[int][]string, freshCount)
	for key, sources := range r.Sources {
		for _, source := range sources {
			if source < 0 || source >= freshCount {
				continue
			}
			out[source] = append(out[source], key)
		}
	}
	for index := 0; index < freshCount; index++ {
		if len(out[index]) == 0 {
			out[index] = []string{DroppedMergeKey}
		}
	}
	return out
}

// Prompt builds the round's messages.
//
// Previous and fresh are numbered separately (P and F) so an answer's sources can
// only ever cite this round's inputs. A previous fact's provenance was settled in
// an earlier round and is not the model's to restate: letting it cite P entries
// would make the ledger describe whatever the model thought that round rather
// than what the fact was actually made from.
//
// Both lists are grouped by fact kind, because what a member knows about the
// project, what they did about it and what was concluded from both are different
// statements and the model must be told which it is looking at. The kind itself
// is never asked of the model: it comes from the cited sources (see Diff).
func Prompt(mission string, previous []TeamFact, fresh []MemberFact) []provider.Message {
	var system strings.Builder
	system.WriteString("You maintain a team's shared knowledge base for one project.\n")
	system.WriteString("You are given the team's current facts and the new facts contributed by team members.\n")
	system.WriteString("Produce the team's complete updated fact list: keep what is still true and supported, ")
	system.WriteString("merge duplicates and refinements across members, drop what the new facts supersede, ")
	system.WriteString("and add what is genuinely new.\n\n")
	system.WriteString("Facts come in three kinds, and a statement must stay the kind it was made from:\n")
	system.WriteString("- world: what is true about the project.\n")
	system.WriteString("- experience: what a member did about the project.\n")
	system.WriteString("- observation: a conclusion drawn from other facts.\n\n")
	system.WriteString("Rules:\n")
	system.WriteString("- Reply with JSON only, no prose, no code fences, no comments and no trailing commas.\n")
	system.WriteString("- Quote every key and string with double quotes; never leave the facts array or the object unterminated.\n")
	system.WriteString("- The shape is {\"facts\": [{\"text\": \"...\", \"sources\": [0, 1]}]}.\n")
	system.WriteString("- Each fact is one self-contained statement in a single sentence.\n")
	system.WriteString("- \"sources\" must contain only F indexes: the new facts this statement was made from.\n")
	system.WriteString("- Cite every F index that supports the statement; never cite a P index.\n")
	system.WriteString("- A fact carried over unchanged needs the same wording and the F indexes now supporting it.\n")
	system.WriteString("- Do not invent facts. Do not mention members by name.\n")
	if mission = strings.TrimSpace(mission); mission != "" {
		system.WriteString("\nThe project's mission, which decides what matters here:\n")
		system.WriteString(mission)
		system.WriteString("\n")
	}

	var user strings.Builder
	user.WriteString("Current team facts:\n")
	if len(previous) == 0 {
		user.WriteString("(none yet)\n")
	}
	numbered := make([]numberedFact, 0, len(previous))
	for i, fact := range previous {
		numbered = append(numbered, numberedFact{index: i, text: fact.Text, factType: fact.FactType})
	}
	writeFactsByType(&user, "P", numbered)

	user.WriteString("\nNew facts from members:\n")
	if len(fresh) == 0 {
		user.WriteString("(none)\n")
	}
	// The members are already contiguous in the round's input, so each member's
	// run is numbered with the index it has in the whole slice: the index is what
	// an answer cites, and renumbering per member would make every member's
	// sources point at the first member's facts.
	for start := 0; start < len(fresh); {
		end := start
		for end < len(fresh) && fresh[end].TenantID == fresh[start].TenantID {
			end++
		}
		fmt.Fprintf(&user, "member %s:\n", fresh[start].TenantID)
		numbered = numbered[:0]
		for i := start; i < end; i++ {
			numbered = append(numbered, numberedFact{index: i, text: fresh[i].Text, factType: fresh[i].FactType})
		}
		writeFactsByType(&user, "F", numbered)
		start = end
	}
	user.WriteString("\nReply with the complete updated fact list as JSON.")

	return []provider.Message{
		{Role: provider.RoleSystem, Content: system.String()},
		{Role: provider.RoleUser, Content: user.String()},
	}
}

// numberedFact is one fact with the label the prompt prints it under.
type numberedFact struct {
	index    int
	text     string
	factType string
}

// writeFactsByType prints one run of facts grouped by kind.
//
// The group headings are part of the prompt rather than decoration: the model has
// to know whether it is reading what is true, what a member did, or what was
// concluded, and the input arrives as one flat list.
func writeFactsByType(out *strings.Builder, prefix string, facts []numberedFact) {
	for _, factType := range []string{FactTypeWorld, FactTypeExperience, FactTypeObservation} {
		wroteHeading := false
		for _, fact := range facts {
			if labelOf(fact.factType) != factType {
				continue
			}
			if !wroteHeading {
				fmt.Fprintf(out, "  %s:\n", factType)
				wroteHeading = true
			}
			fmt.Fprintf(out, "  [%s%d] %s\n", prefix, fact.index, fact.text)
		}
	}
}

// labelOf folds a missing or unknown type to world, which is the kind a fact
// with no declared type has always been.
func labelOf(factType string) string {
	switch factType {
	case FactTypeExperience, FactTypeObservation:
		return factType
	default:
		return FactTypeWorld
	}
}
