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

// entityMatchKey normalizes a name for comparison: surrounding whitespace is
// dropped and internal whitespace runs collapse to a single space. Case is
// folded by the comparer, not here, so the stored display form keeps its case.
func entityMatchKey(name string) string {
	return strings.Join(strings.Fields(name), " ")
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
