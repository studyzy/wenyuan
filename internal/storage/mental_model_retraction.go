package storage

import (
	"context"
	"strings"
)

// MemoryBackedFactTypes are the fact types whose identifiers address rows in
// memory_units.
//
// A mental model records what it was built from under reflect_response.based_on,
// keyed by fact type. That map also carries mental models and directives, whose
// identifiers address entirely different tables — feeding those to a live-memory
// lookup would report every one as missing and retract content grounded on a
// perfectly healthy sibling document. So membership here is an allowlist, never
// an exclusion list.
var MemoryBackedFactTypes = map[string]bool{
	"world":       true,
	"experience":  true,
	"observation": true,
}

// RetractedGrounding is the memory-backed evidence a document cites that no
// longer exists.
type RetractedGrounding struct {
	// Facts keeps the recorded entries verbatim, including their text: an
	// observation swept away when its source was invalidated is hard-deleted
	// along with its history, so the copy the document recorded is the only
	// surviving record of what it said.
	Facts []GroundedFact
	// CitedCount is how many memory-backed facts the document cited in total,
	// the live ones included.
	CitedCount int
}

// IDs returns the identifiers that no longer resolve.
func (r RetractedGrounding) IDs() []string {
	ids := make([]string, 0, len(r.Facts))
	for _, fact := range r.Facts {
		if fact.ID != "" {
			ids = append(ids, fact.ID)
		}
	}
	return ids
}

// Unresolvable reports that none of the cited grounding resolves, which is a
// broken link rather than a retraction.
//
// A retraction removes some of a document's evidence; losing all of it at once
// means the record is not usable as evidence of anything, and acting on it would
// have the model unsay a document it has nothing to check against.
func (r RetractedGrounding) Unresolvable() bool {
	return r.CitedCount > 0 && len(r.Facts) == r.CitedCount
}

// GroundedFact is one cited fact, as the document recorded it.
type GroundedFact struct {
	ID   string
	Type string
	Text string
}

// CitedGrounding returns the memory-backed facts a mental model recorded as its
// grounding, in the order they were stored.
func CitedGrounding(model MentalModel) []GroundedFact {
	if model.ReflectResult == nil {
		return nil
	}
	basedOn, ok := model.ReflectResult["based_on"].(map[string]any)
	if !ok {
		return nil
	}
	raw, ok := basedOn["memories"].([]any)
	if !ok {
		return nil
	}
	facts := make([]GroundedFact, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		id, _ := entry["id"].(string)
		factType, _ := entry["type"].(string)
		if strings.TrimSpace(id) == "" || !MemoryBackedFactTypes[factType] {
			continue
		}
		text, _ := entry["text"].(string)
		facts = append(facts, GroundedFact{ID: id, Type: factType, Text: text})
	}
	return facts
}

// RetractionOf reports which of a document's cited facts no longer exist.
//
// The cause is deliberately not distinguished: invalidated, deleted and swept
// away all present identically as an identifier with no live row, and from the
// document's point of view they are the same event — the sentence it is telling
// the reader is no longer supported by anything.
func RetractionOf(model MentalModel, live map[string]bool) RetractedGrounding {
	cited := CitedGrounding(model)
	result := RetractedGrounding{CitedCount: len(cited)}
	for _, fact := range cited {
		if !live[fact.ID] {
			result.Facts = append(result.Facts, fact)
		}
	}
	return result
}

// LiveMemoryIDs is the capability a backend implements to answer which of the
// given identifiers still exist as memory units of a bank.
type LiveMemoryIDs interface {
	LiveMemoryIDs(ctx context.Context, bankID string, ids []string) (map[string]bool, error)
}

// groundingRetracted reports whether any of the model's cited evidence has gone.
//
// It is the check the watermark cannot make: staleness asks whether anything was
// written since the last refresh, and a retraction is the absence of a row, so it
// raises no watermark and reaches no prompt.
func groundingRetracted(ctx context.Context, store LiveMemoryIDs, bankID string, model MentalModel) bool {
	cited := CitedGrounding(model)
	if len(cited) == 0 {
		return false
	}
	ids := make([]string, 0, len(cited))
	for _, fact := range cited {
		ids = append(ids, fact.ID)
	}
	live, err := store.LiveMemoryIDs(ctx, bankID, ids)
	if err != nil {
		// A lookup that fails must not park a model as fresh: the next tick
		// retries rather than silently skipping the retraction.
		return false
	}
	retraction := RetractionOf(model, live)
	return len(retraction.Facts) > 0 && !retraction.Unresolvable()
}
