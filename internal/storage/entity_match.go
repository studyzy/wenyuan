package storage

import "strings"

// EntityCandidateSimilarity is the trigram score at or above which a stored
// entity is considered a candidate for a supplied name.
//
// It is deliberately far below EntitySimilarityThreshold: trigram similarity is
// a cheap recall filter that decides which names are worth scoring, and it
// ranks names differently from the Jaro-Winkler measure that actually decides
// the merge. Using it as the merge bar would reject pairs the scorer accepts,
// because a single missing letter in a long name drops the trigram score well
// below what the scorer considers a match.
const EntityCandidateSimilarity = 0.3

// EntitySimilarityThreshold is the score at or above which a supplied name is
// treated as a reference to an existing entity rather than a new one. It is a
// high bar on purpose: merging two genuinely different entities corrupts the
// graph, while failing to merge a near-duplicate only leaves two rows a caller
// can merge later.
//
// The scale is Jaro-Winkler, where 1 is an exact match. 0.84 is the same bar
// the reference applies, expressed on the unit interval rather than as a
// percentage.
const EntitySimilarityThreshold = 0.84

// Jaro-Winkler weighting. A shared prefix is evidence of the same name, so it
// lifts the score; the scaling factor is the conventional 0.1 and the prefix
// considered is capped at four characters.
const (
	jaroWinklerPrefixScale = 0.1
	jaroWinklerMaxPrefix   = 4
)

// EntityNameMaxLength is the longest entity name intake will accept, measured in
// characters (runes).
//
// A model can emit an unbounded label — base64, SVG path data, a JSON
// fragment — and the entity table's btree index cannot hold a tuple that long,
// so the INSERT would fail the whole retain, not just the one entity. A name
// past the cap is dropped at intake rather than truncated, so it never becomes a
// registry identity a later artifact could merge onto. 512 matches the
// reference's cap.
const EntityNameMaxLength = 512

// normalizeEntityName collapses a name's internal whitespace to single spaces
// and trims the ends, the intake every candidate entity name passes through.
//
// ok is false when the normalized name is blank or past EntityNameMaxLength, in
// which case the name is dropped rather than stored: a blank name is not an
// entity, and an over-long one is an extraction artifact (a run of markup or
// base64) that the btree index over canonical_name cannot hold.
func normalizeEntityName(name string) (string, bool) {
	normalized := strings.Join(strings.Fields(name), " ")
	if normalized == "" {
		return "", false
	}
	// A byte length within the cap proves the rune length is too, so the
	// common short name never pays for a rune conversion.
	if len(normalized) <= EntityNameMaxLength {
		return normalized, true
	}
	if len([]rune(normalized)) <= EntityNameMaxLength {
		return normalized, true
	}
	return "", false
}

// entityMatchKey normalizes a name for comparison: surrounding whitespace is
// dropped and internal whitespace runs collapse to a single space. Case is
// folded by the comparer, not here, so the stored display form keeps its case.
//
// Only a name intake accepted ever reaches the comparison, so the key is
// whatever normalizeEntityName returned for it; a name intake dropped is never
// stored and so never has a key to match against.
func entityMatchKey(name string) string {
	normalized, _ := normalizeEntityName(name)
	return normalized
}

// entitySimilarity scores how alike two entity names are, in the unit interval.
// The score is 1 for an exact case-insensitive match and falls off as the names
// diverge.
//
// The measure is Jaro-Winkler rather than a plain edit distance, because the
// names it compares are short and a transposition or a doubled letter is a
// common way to spell the same entity; an edit distance counts those as two
// edits and would score the pair far below the merge threshold.
func entitySimilarity(left, right string) float64 {
	a := strings.ToLower(entityMatchKey(left))
	b := strings.ToLower(entityMatchKey(right))
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	return jaroWinkler([]rune(a), []rune(b))
}

// entityMergeFloorPasses applies the reference's second trigram gate: a
// candidate whose name shares too few trigrams with the supplied name is
// dropped before it is scored, even when the probe admitted it.
//
// It is distinct from the candidate-fetch floor: that floor decides which
// stored names are worth scoring, while this one is the reference's own
// merge_min_similarity, applied at the point the candidate is about to be
// scored. The two measures disagree most on short names, where a coincidental
// shared run inflates one and not the other, so this gate keeps a name that is
// only coincidentally close from being merged onto. Names are compared
// case-insensitively. A zero floor admits every candidate.
func entityMergeFloorPasses(name, candidate string, floor float64) bool {
	return trigramJaccard(strings.ToLower(name), strings.ToLower(candidate)) >= floor
}

// jaroWinkler computes the Jaro similarity and applies the prefix bonus.
func jaroWinkler(left, right []rune) float64 {
	matches, transpositions := jaroMatches(left, right)
	if matches == 0 {
		return 0
	}
	jaro := (float64(matches)/float64(len(left)) +
		float64(matches)/float64(len(right)) +
		float64(matches-transpositions)/float64(matches)) / 3
	if jaro <= 0.7 {
		// The prefix bonus only applies once the pair is already similar
		// enough that a shared prefix is meaningful rather than coincidental.
		return jaro
	}
	prefix := 0
	for prefix < len(left) && prefix < len(right) && prefix < jaroWinklerMaxPrefix && left[prefix] == right[prefix] {
		prefix++
	}
	return jaro + float64(prefix)*jaroWinklerPrefixScale*(1-jaro)
}

// jaroMatches reports how many characters the two names share within the
// window the Jaro measure allows, and how many of those matches are out of
// order.
func jaroMatches(left, right []rune) (int, int) {
	window := maxInt(len(left), len(right))/2 - 1
	if window < 0 {
		window = 0
	}
	leftMatched := make([]bool, len(left))
	rightMatched := make([]bool, len(right))
	matches := 0
	for i := range left {
		start := i - window
		if start < 0 {
			start = 0
		}
		end := i + window + 1
		if end > len(right) {
			end = len(right)
		}
		for j := start; j < end; j++ {
			if rightMatched[j] || left[i] != right[j] {
				continue
			}
			leftMatched[i] = true
			rightMatched[j] = true
			matches++
			break
		}
	}
	if matches == 0 {
		return 0, 0
	}
	// Count how many matched characters appear in a different order.
	transpositions := 0
	j := 0
	for i := range left {
		if !leftMatched[i] {
			continue
		}
		for !rightMatched[j] {
			j++
		}
		if left[i] != right[j] {
			transpositions++
		}
		j++
	}
	return matches, transpositions / 2
}
