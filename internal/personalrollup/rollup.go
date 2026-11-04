// Package personalrollup holds the personal aggregate's rules that need no
// database: how an aggregate fact is identified, what a round's answer changes,
// how the round is asked, how the answer is read back, and what the navigation
// index is.
//
// Everything here is a pure function. The round itself — reading the source
// banks, taking the lock, calling the model, writing the result — belongs to the
// personal store and the maintenance job, which lets these rules be tested
// without PostgreSQL, a model or a network.
package personalrollup

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/projectmerge"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// Normalize folds away the differences that must not create two aggregate facts.
func Normalize(text string) string { return tenant.Normalize(text) }

// MergeKey is the identity of an aggregate fact: the hex SHA-256 of its
// normalized text.
func MergeKey(text string) string { return tenant.MergeKey(text) }

// DroppedMergeKey marks a source the model folded away.
const DroppedMergeKey = tenant.DroppedMergeKey

// PreviousFact is one fact the aggregate bank already holds.
type PreviousFact struct {
	// ID is the stored unit's id. It is kept across rounds when the fact's
	// wording does not change, which is what keeps the fact's observations
	// attached.
	ID string
	// Text is the stored text.
	Text string
	// MergeKey is derived from Text and carried so a caller does not recompute
	// it per comparison.
	MergeKey string
}

// SourceFact is one fact entering the round from a source bank.
type SourceFact struct {
	// BankID names the bank the fact lives in.
	BankID string
	// UnitID is that bank's own unit id, which is what the ledger records.
	UnitID string
	// Text is the fact's text.
	Text string
}

// Answer is one aggregate fact the model produced, with the inputs it came from.
type Answer struct {
	// Text is the fact's text, as the model wrote it.
	Text string
	// Sources are indexes into the round's Fresh slice: the source facts this
	// aggregate fact was made from. The indexes are what lets the ledger record a
	// provenance the server determined rather than one the model asserted.
	Sources []int
	// Topics are the vocabulary entries the fact belongs to, normalized.
	Topics []string
}

// Errors ParseAnswer reports. They are distinct because they mean different
// things to the caller: a shape failure is a broken model, while an empty answer
// is a well-formed claim that nothing is known — which would empty the person's
// aggregate and so is refused unless the round had nothing to begin with.
var (
	// ErrAnswerShape reports a reply that is not the expected JSON.
	ErrAnswerShape = errors.New("rollup answer is not the expected JSON")
	// ErrAnswerEmpty reports a well-formed reply carrying no facts.
	ErrAnswerEmpty = errors.New("rollup answer carries no facts")
)

// sourceIndexList decodes "sources" as indexes.
//
// The prompt asks for numbers, but a model that quotes them ("sources": ["2"])
// or writes one bare value would otherwise fail the whole answer on a JSON type
// quibble — the same class of failure as counting past the batch. Anything that
// is not an index at all is still a shape failure: unlike an out-of-range number,
// there is no defensible way to read it.
type sourceIndexList []int

// UnmarshalJSON accepts the number, numeric-string and single-number forms.
func (l *sourceIndexList) UnmarshalJSON(data []byte) error {
	decoded, err := decodeSourceIndexes(data)
	if err != nil {
		return err
	}
	*l = decoded
	return nil
}

// decodeSourceIndexes reads one "sources" value.
func decodeSourceIndexes(data []byte) ([]int, error) {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var raw []json.RawMessage
		if err := json.Unmarshal(trimmed, &raw); err != nil {
			return nil, err
		}
		out := make([]int, 0, len(raw))
		for _, item := range raw {
			index, ok := parseSourceIndex(item)
			if !ok {
				return nil, fmt.Errorf("sources carries %s, which is not an index", item)
			}
			out = append(out, index)
		}
		return out, nil
	}
	index, ok := parseSourceIndex(trimmed)
	if !ok {
		return nil, fmt.Errorf("sources carries %s, which is not an index", trimmed)
	}
	return []int{index}, nil
}

// parseSourceIndex reads one index written as a number or a quoted number.
func parseSourceIndex(raw json.RawMessage) (int, bool) {
	var number int
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, true
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, false
	}
	number, err := strconv.Atoi(strings.TrimSpace(text))
	return number, err == nil
}

// answerEnvelope is the wire shape the prompt asks for.
type answerEnvelope struct {
	Facts []struct {
		Text    string          `json:"text"`
		Sources sourceIndexList `json:"sources"`
		Topics  []string        `json:"topics"`
	} `json:"facts"`
}

// ParseAnswer decodes the model's reply.
//
// freshCount bounds the source indexes: an index outside the round's inputs is a
// silently dropped provenance rather than a shape failure, because a ledger row
// pointing at nothing is worse than no row at all. The prompt numbers fresh facts
// from F0, so a model that keeps counting past the batch — F20..F23 while the
// round carries three inputs — is stating a fact from its own history, not a
// broken reply: refusing the whole answer spends the round and leaves the
// aggregate behind for as long as the model keeps doing it.
//
// An empty answer is its own error. It is only acceptable when the round had
// nothing before it — otherwise it would empty the aggregate on a model hiccup.
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
		// A model that annotates its JSON ("a": "b", // ...) is the next most
		// common quibble after a code fence; the JSON the comment hides is the
		// answer, so read that rather than spend the round.
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
		var sources []int
		for _, source := range fact.Sources {
			if source < 0 || source >= freshCount {
				continue
			}
			sources = append(sources, source)
		}
		answers = append(answers, Answer{
			Text: text, Sources: sources, Topics: normalizeTopics(fact.Topics),
		})
	}
	return answers, nil
}

// normalizeTopics deduplicates and normalizes a fact's topic list.
func normalizeTopics(topics []string) []string {
	seen := make(map[string]bool, len(topics))
	out := make([]string, 0, len(topics))
	for _, topic := range topics {
		name := normalizeTopic(topic)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// normalizeTopic folds a topic to its vocabulary spelling: trimmed, whitespace
// collapsed and case-folded, so "Postgres" and " postgres " are one topic.
func normalizeTopic(topic string) string {
	return strings.ToLower(tenant.Normalize(topic))
}

// DiffResult is the fact-level change an answer produces, in the merge engine's
// own shape.
//
// Identity is decided by internal/projectmerge.Diff, which both planes share: a
// fact whose wording survives keeps its row (and so its observations) whichever
// plane wrote it. This package adds one thing that engine does not carry — the
// vocabulary a fact belongs to — so the directory can be projected from it.
type DiffResult struct {
	// Keep are the facts whose wording survived, in previous order.
	Keep []projectmerge.TeamFact
	// Remove are the facts the answer no longer contains.
	Remove []projectmerge.TeamFact
	// Insert are the facts the answer adds.
	Insert []projectmerge.TeamFact
	// Sources maps each fact's key to the indexes into the round's Fresh slice
	// that support it.
	Sources map[string][]int
	// Topics maps each fact's key to the topics it belongs to.
	Topics map[string][]string
}

// Diff classifies what a round's answer changes.
//
// Duplicate answers union their sources and topics, exactly as the merge engine
// unions sources: the model restating a fact is a formatting quibble, not a
// second fact.
func Diff(previous []PreviousFact, answers []Answer) DiffResult {
	carried := make([]projectmerge.TeamFact, 0, len(previous))
	for _, fact := range previous {
		carried = append(carried, projectmerge.TeamFact{ID: fact.ID, Text: fact.Text, MergeKey: fact.MergeKey})
	}
	merged := make([]projectmerge.SummarizedAnswer, 0, len(answers))
	for _, answer := range answers {
		merged = append(merged, projectmerge.SummarizedAnswer{Text: answer.Text, Sources: answer.Sources})
	}
	// The aggregate's own facts are all world facts and its input carries no
	// kinds either: the rollup summarizes a person's banks, it does not sort
	// their statements into types. Passing no fresh input is what makes every
	// inserted fact come out as world, which is what its writer stores.
	change := projectmerge.Diff(carried, merged, nil)
	result := DiffResult{
		Keep: change.Keep, Remove: change.Remove, Insert: change.Insert,
		Sources: change.Sources,
		Topics:  map[string][]string{},
	}
	for _, answer := range answers {
		key := MergeKey(answer.Text)
		if _, ok := result.Sources[key]; !ok {
			continue
		}
		result.Topics[key] = unionStrings(result.Topics[key], answer.Topics)
	}
	return result
}

// SourceMergeKeys maps each input index to the merge keys it supports, which is
// what the ledger records: a source is either supporting an aggregate fact or
// dropped.
//
// An input no surviving fact cites lands under DroppedMergeKey. That distinction
// is what keeps its later deletion from forcing a rebuild: nothing in the
// aggregate was made from it.
func (r DiffResult) SourceMergeKeys(freshCount int) map[int][]string {
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

// BanksByKey maps a fact's merge key to the source banks it spans, in first-seen
// order.
func BanksByKey(sources map[int][]string, fresh []SourceFact) map[string][]string {
	out := make(map[string][]string, len(sources))
	keys := make([]int, 0, len(sources))
	for index := range sources {
		keys = append(keys, index)
	}
	sort.Ints(keys)
	for _, index := range keys {
		if index < 0 || index >= len(fresh) {
			continue
		}
		for _, key := range sources[index] {
			out[key] = appendUnique(out[key], fresh[index].BankID)
		}
	}
	return out
}

// appendUnique appends value unless it is already present.
func appendUnique(list []string, value string) []string {
	for _, existing := range list {
		if existing == value {
			return list
		}
	}
	return append(list, value)
}

// unionStrings merges two string lists case-insensitively, first spelling wins.
func unionStrings(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, list := range [][]string{a, b} {
		for _, value := range list {
			folded := strings.ToLower(value)
			if folded == "" || seen[folded] {
				continue
			}
			seen[folded] = true
			out = append(out, value)
		}
	}
	return out
}
