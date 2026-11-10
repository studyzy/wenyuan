package projectmerge

import (
	"sort"
	"strings"
)

// Team entities: how a member's entity names become one shared bank's entities.
//
// A member's bank resolves names with a weighted score — name similarity, shared
// neighbours, recency — because it is answering for one person. Carrying that
// rule into a shared bank would merge two teammates' different systems whenever
// their names look alike ("支付网关" and "支付系统" score high enough to merge),
// and a wrong merge cannot be undone: every fact of one now answers for the
// other. So the shared bank decides by exact identity only. A name that is
// merely similar stays a second entity, and the cost is a missing graph edge.
//
// The identity is the whitespace-normalized name, lowercased, which is exactly
// the identity the schema enforces: idx_entities_bank_lower_name is UNIQUE on
// (bank_id, lower(canonical_name)). A member bank cannot hold "ABC" and "abc" as
// two rows, so the shared bank's rule is not a second judgement about a name —
// it is the same one.

// EntityNameMaxLength is the longest entity name intake will accept, measured in
// characters (runes).
//
// A model can emit an unbounded label — base64, SVG path data, a JSON fragment —
// and the entity table's btree index cannot hold a tuple that long, so the
// INSERT would fail the whole retain, not just the one entity. A name past the
// cap is dropped at intake rather than truncated, so it never becomes a registry
// identity a later artifact could merge onto.
const EntityNameMaxLength = 512

// NormalizeEntityName collapses a name's internal whitespace to single spaces
// and trims the ends, the intake every candidate entity name passes through.
//
// ok is false when the normalized name is blank or past EntityNameMaxLength, in
// which case the name is dropped rather than stored.
//
// It lives here rather than in the storage package because the member bank and
// the merge round must apply the identical rule, and storage depends on this
// package — not the other way round.
func NormalizeEntityName(name string) (string, bool) {
	normalized := strings.Join(strings.Fields(name), " ")
	if normalized == "" {
		return "", false
	}
	// A byte length within the cap proves the rune length is too, so the common
	// short name never pays for a rune conversion.
	if len(normalized) <= EntityNameMaxLength {
		return normalized, true
	}
	if len([]rune(normalized)) <= EntityNameMaxLength {
		return normalized, true
	}
	return "", false
}

// EntityNameKey is the identity of a team entity for one entity name.
//
// ok is false for a name intake would drop — blank, or past the length cap. Such
// a name never became an entity in the member's own bank either, so the team bank
// must not create one.
//
// Case is the only difference it removes: whitespace was already collapsed by the
// intake. A full-width spelling stays a different name from its half-width
// counterpart, because the two are often genuinely different systems and nothing
// later can tell them apart again.
func EntityNameKey(name string) (string, bool) {
	normalized, ok := NormalizeEntityName(name)
	if !ok {
		return "", false
	}
	return strings.ToLower(normalized), true
}

// EntitySet is one fact's entity names reduced to team identities.
//
// It is the unit both the read and the write side work in, so a fact's names are
// folded once and never re-keyed differently in two places.
type EntitySet struct {
	// names maps an identity to the spelling the team bank stores for it.
	names map[string]string
	// order is the identities in the order their names first appeared, so the
	// set is deterministic without depending on map iteration.
	order []string
}

// NewEntitySet folds a fact's raw entity names into team identities.
//
// Two names with the same identity are one entity, and the first spelling wins: a
// member's own extraction may report "ABC" and "abc" for the same thing in one
// fact, and the shared bank holds one row for it. A name intake would drop is
// dropped here too, so the set never carries an identity the member's bank does
// not hold.
func NewEntitySet(names []string) EntitySet {
	set := EntitySet{names: make(map[string]string, len(names))}
	for _, name := range names {
		key, ok := EntityNameKey(name)
		if !ok {
			continue
		}
		if _, seen := set.names[key]; seen {
			continue
		}
		normalized, _ := NormalizeEntityName(name)
		set.names[key] = normalized
		set.order = append(set.order, key)
	}
	return set
}

// Keys returns the set's identities in first-seen order.
func (s EntitySet) Keys() []string {
	if len(s.order) == 0 {
		return nil
	}
	return append([]string(nil), s.order...)
}

// Name returns the stored spelling of one identity, and whether the set holds it.
func (s EntitySet) Name(key string) (string, bool) {
	name, ok := s.names[key]
	return name, ok
}

// Len is how many identities the set holds.
func (s EntitySet) Len() int { return len(s.order) }

// Pairs returns every unordered pair of the set's identities, each pair ordered
// lexicographically and the list sorted, so the same set always produces the same
// pairs in the same order.
//
// A pair is what the shared bank's entity_cooccurrences records: two entities a
// single fact mentions are seen together once.
func (s EntitySet) Pairs() [][2]string {
	if len(s.order) < 2 {
		return nil
	}
	sorted := append([]string(nil), s.order...)
	sort.Strings(sorted)
	pairs := make([][2]string, 0, len(sorted)*(len(sorted)-1)/2)
	for i := range len(sorted) {
		for j := i + 1; j < len(sorted); j++ {
			pairs = append(pairs, [2]string{sorted[i], sorted[j]})
		}
	}
	return pairs
}
