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

// SummarizedAnswer is one fact a summarising pass (the personal rollup) produced,
// with the inputs it came from. The team merge no longer produces these — it
// carries facts verbatim (see dedupe.go) — but the rollup's identity rules are
// shared, so the shape stays here.
type SummarizedAnswer struct {
	// Text is the fact's text, as the model wrote it.
	Text string
	// Sources are indexes into the round's Fresh slice: the member facts this
	// fact was made from.
	Sources []int
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
func Diff(previous []TeamFact, answers []SummarizedAnswer, fresh []MemberFact) MergeResult {
	result := MergeResult{Sources: map[string][]int{}}
	seen := make(map[string]bool, len(answers))
	order := make([]string, 0, len(answers))
	byKey := make(map[string]SummarizedAnswer, len(answers))
	for _, answer := range answers {
		key := MergeKey(answer.Text)
		if existing, ok := byKey[key]; ok {
			existing.Sources = unionInts(existing.Sources, answer.Sources)
			byKey[key] = existing
			continue
		}
		byKey[key] = SummarizedAnswer{Text: Normalize(answer.Text), Sources: append([]int(nil), answer.Sources...)}
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
