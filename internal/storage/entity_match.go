package storage

import (
	"math"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/projectmerge"
)

// EntityCandidateSimilarity is the trigram score at or above which a stored
// name is fetched as a candidate for a supplied name. It is a cheap recall
// filter; the merge decision belongs to the weighted score below.
const EntityCandidateSimilarity = 0.3

// EntitySimilarityThreshold is the score at or above which a candidate is
// reused instead of creating a new entity. A weighted score is compared to this
// bar after rounding to six decimal places, so equal scores decide alike
// whichever signals produced them.
const EntitySimilarityThreshold = 0.6

// The weighted score's components. A name whose trigram set is identical to a
// candidate's stands on its own as a full match; every other name is graded by
// its character-sequence similarity, the entities it was seen beside and how
// recently the candidate was seen.
const (
	entityNameWeight              = 0.5
	entityCooccurrenceScoreWeight = 0.3
	entityTemporalWeight          = 0.2
	entityTemporalWindowDays      = 7.0
	entityIdenticalTrigrams       = 1.0
	entityScoreScale              = 1e6
)

// EntityNameMaxLength is the longest entity name intake will accept, measured
// in characters (runes). It is the merge rule's own cap, re-exported so a
// caller here needs one import.
const EntityNameMaxLength = projectmerge.EntityNameMaxLength

// NormalizeEntityName collapses a name's internal whitespace to single spaces
// and trims the ends, the intake every candidate entity name passes through.
//
// ok is false when the normalized name is blank or past EntityNameMaxLength, in
// which case the name is dropped rather than stored.
//
// The rule lives in internal/projectmerge because a project merge round appends
// member facts to a shared bank and must apply the identical one; this is the
// member-side alias so both callers read the same function.
func NormalizeEntityName(name string) (string, bool) {
	return projectmerge.NormalizeEntityName(name)
}

// entityMatchKey normalizes a name for comparison: surrounding whitespace is
// dropped and internal whitespace runs collapse to a single space. Case is
// folded by the comparer, not here, so the stored display form keeps its case.
func entityMatchKey(name string) string {
	normalized, _ := NormalizeEntityName(name)
	return normalized
}

// entitySimilarity scores two names by their longest common character sequence,
// in the unit interval. An exact case-insensitive match is 1; a pair with no
// shared sequence is 0. A transposition or a doubled letter is a common way to
// spell one entity, and a sequence ratio keeps those close where an edit
// distance would count two separate edits.
func entitySimilarity(left, right string) float64 {
	a := strings.ToLower(entityMatchKey(left))
	b := strings.ToLower(entityMatchKey(right))
	if a == b {
		return 1
	}
	if a == "" || b == "" {
		return 0
	}
	return entitySequenceRatio(a, b)
}

// entityScoreContext carries the evidence a resolution weighs beyond the name.
// A mention with no surrounding entities supplies an empty context, and the
// score then rests on the name and the candidate's recency alone.
type entityScoreContext struct {
	// NearbyNames are the lowercased names of the other entities in the
	// mention's context.
	NearbyNames map[string]bool
	// CandidateCooccurrences are the lowercased names the candidate has been
	// seen beside.
	CandidateCooccurrences map[string]bool
	// Degrees counts how many distinct entities each nearby name co-occurs
	// with; a name absent from the map is treated as degree one.
	Degrees map[string]int
	// LastSeen is when the candidate was last mentioned; EventDate is the
	// mention's own date.
	LastSeen  *time.Time
	EventDate *time.Time
}

// entityMentionContext is the mention-side evidence one resolution call weighs:
// the names mentioned beside this one, how indiscriminate each of those names
// is as a co-occurrence partner, and the mention's own event time. Every
// candidate of one mention is graded against the same context.
type entityMentionContext struct {
	// NearbyNames are the lowercased names of the other entities in the
	// mention's context.
	NearbyNames map[string]bool
	// Degrees counts how many distinct entities each nearby name co-occurs
	// with; a name absent from the map is treated as degree one.
	Degrees map[string]int
	// EventDate is the mention's own date; nil leaves the recency share out.
	EventDate *time.Time
}

// scoreContextFor pairs the mention with one candidate's own evidence, the
// shape the scorer reads.
func (m entityMentionContext) scoreContextFor(candidate Entity) entityScoreContext {
	return entityScoreContext{
		NearbyNames:            m.NearbyNames,
		CandidateCooccurrences: candidate.Cooccurrences,
		Degrees:                m.Degrees,
		LastSeen:               candidate.LastSeen,
		EventDate:              m.EventDate,
	}
}

// entityNearbyNames lowercases the names mentioned beside one entity of the
// same retain item, dropping the entity's own spelling, so the co-occurrence
// signal compares the candidate's partners with the mention's context. A blank
// name contributes nothing.
func entityNearbyNames(self string, mentions []RetainEntity) map[string]bool {
	nearby := make(map[string]bool, len(mentions))
	for _, mention := range mentions {
		if mention.Text == self {
			continue
		}
		if key := entityMatchKey(mention.Text); key != "" {
			nearby[strings.ToLower(key)] = true
		}
	}
	return nearby
}

// entityCooccurrenceEvidence folds unordered co-occurrence identity pairs into
// the evidence the scorer reads. A candidate carries the lowercased names of
// the partners it was seen beside — partners whose identity is in named, since
// a partner must be known by name to match a nearby name — and the returned
// degrees count, per named identity, how many distinct entities it co-occurs
// with over every partner, named or not.
func entityCooccurrenceEvidence(candidates []Entity, named map[string]string, pairs [][2]string) map[string]int {
	neighbours := make(map[string]map[string]bool)
	for _, pair := range pairs {
		entityCooccurrenceLink(neighbours, pair[0], pair[1])
		entityCooccurrenceLink(neighbours, pair[1], pair[0])
	}
	byID := make(map[string]int, len(candidates))
	for i, candidate := range candidates {
		byID[candidate.ID] = i
	}
	// A row is stored with the smaller identity first, so either endpoint can
	// be the candidate; both directions are folded.
	for _, pair := range pairs {
		entityCooccurrenceName(candidates, byID, named, pair[0], pair[1])
		entityCooccurrenceName(candidates, byID, named, pair[1], pair[0])
	}
	degrees := make(map[string]int, len(named))
	for id, name := range named {
		if degree := len(neighbours[id]); degree > degrees[name] {
			degrees[name] = degree
		}
	}
	return degrees
}

// entityCooccurrenceName records, onto the candidate named from, the known
// name of its partner to — neither side of a pair is obliged to be known.
func entityCooccurrenceName(candidates []Entity, byID map[string]int, named map[string]string, from, to string) {
	left, ok := byID[from]
	if !ok {
		return
	}
	name, ok := named[to]
	if !ok {
		return
	}
	if candidates[left].Cooccurrences == nil {
		candidates[left].Cooccurrences = make(map[string]bool)
	}
	candidates[left].Cooccurrences[name] = true
}

// entityCooccurrenceLink records one direction of a co-occurrence pair.
func entityCooccurrenceLink(neighbours map[string]map[string]bool, from, to string) {
	set, ok := neighbours[from]
	if !ok {
		set = make(map[string]bool)
		neighbours[from] = set
	}
	set[to] = true
}

// entityCooccurrenceWeight damps one shared neighbour by how indiscriminate it
// is: a partner seen with a single entity keeps its full vote, a hub seen with
// a hundred is worth a tenth.
func entityCooccurrenceWeight(degree int) float64 {
	if degree < 1 {
		degree = 1
	}
	return 1.0 / math.Sqrt(float64(degree))
}

// scoreEntityMatch grades one candidate in the unit interval. Identical trigram
// sets mean the same words under different case, punctuation or decoration and
// score the full mark on their own. Otherwise the name, the shared neighbours
// and the recency each contribute their weighted share.
func scoreEntityMatch(name, canonical string, trigramSimilarity float64, ctx entityScoreContext) float64 {
	if trigramSimilarity >= entityIdenticalTrigrams {
		return 1.0
	}
	score := entitySimilarity(name, canonical) * entityNameWeight
	if len(ctx.NearbyNames) > 0 {
		matched := 0.0
		for nearby := range ctx.NearbyNames {
			if ctx.CandidateCooccurrences[nearby] {
				matched += entityCooccurrenceWeight(ctx.Degrees[nearby])
			}
		}
		score += (matched / float64(len(ctx.NearbyNames))) * entityCooccurrenceScoreWeight
	}
	if ctx.LastSeen != nil && ctx.EventDate != nil {
		days := math.Abs(ctx.EventDate.Sub(*ctx.LastSeen).Hours()) / 24
		if days < entityTemporalWindowDays {
			score += math.Max(0, 1-days/entityTemporalWindowDays) * entityTemporalWeight
		}
	}
	return score
}

// entityScoreAccepted applies the merge bar to a rounded score. The weights are
// decimal fractions binary floating point cannot hold exactly, so a score of
// the bar itself could otherwise land on either side depending only on which
// signals produced it.
func entityScoreAccepted(score float64) bool {
	return math.Round(score*entityScoreScale)/entityScoreScale >= EntitySimilarityThreshold
}

// entityMergeFloorPasses applies the trigram gate: a candidate whose name
// shares too few trigrams with the supplied name is dropped before it is
// scored, even when the probe admitted it. A zero floor admits every candidate.
func entityMergeFloorPasses(name, candidate string, floor float64) bool {
	return trigramJaccard(strings.ToLower(name), strings.ToLower(candidate)) >= floor
}
