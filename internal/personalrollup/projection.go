package personalrollup

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"time"
)

// ProjectionKey is the identity of one row in the aggregate bank: the source
// unit it mirrors. It is deliberately not a hash of the text — a fact edited in
// its own bank keeps its unit id, so editing replaces one projected row rather
// than creating a second; and two identical facts in two different banks are
// two facts, exactly as they are inside any one bank.
//
// The pair is hashed into hex, without any text normalization: the key travels
// through JSONB metadata (a raw concatenation would carry control bytes JSON
// cannot escape), and folding whitespace could make two distinct bank ids
// collide. The hash is over the exact ids, so identity is stable and unique.
func ProjectionKey(bankID, unitID string) string {
	sum := sha256.Sum256([]byte(bankID + "\x00" + unitID))
	return hex.EncodeToString(sum[:])
}

// ProjectionFact is one row of the aggregate bank, or one source fact a round
// read. Every row of the aggregate bank is a verbatim projection of exactly one
// source fact, so a consolidation pass over the aggregate bank is reading the
// person's memories rather than conclusions drawn from them.
type ProjectionFact struct {
	// ID is the stored row's id. It is empty for a source fact that has not been
	// projected yet.
	ID string
	// Key is ProjectionKey(bank, unit). For a stored row it is read back from
	// metadata; for a source fact it is computed.
	Key string
	// Text, FactType and the temporal bounds are copied verbatim from the source
	// fact, so sourceObservationBounds sees the same evidence it would in the
	// source bank.
	Text     string
	FactType string
	// Tags are copied verbatim: recall's tag filters and the observation scope
	// both read them, so a projection that dropped them would make the aggregate
	// unreachable by the filters its source facts answer.
	Tags          []string
	EventDate     *time.Time
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	// BankID and UnitID name the source. They are needed only for the ledger.
	BankID string
	UnitID string
}

// ProjectionSource is one ledger row: a source unit supporting one projected row.
type ProjectionSource struct {
	UnitID   string
	MergeKey string
	BankID   string
}

// ProjectionDiff is what a round writes. Remove carries the stored rows to drop
// (with their ids); Insert carries the rows to add; Sources records one ledger
// row per fact the round consumed.
type ProjectionDiff struct {
	Remove  []ProjectionFact
	Insert  []ProjectionFact
	Sources []ProjectionSource
}

// DiffProjection compares the aggregate bank's projected rows with the facts a
// round read. Identity is the source unit, not the wording: a fact whose text,
// kind or dates changed replaces its row, while a fact that did not change keeps
// its row and therefore the observations already resting on it.
//
// A rebuild reads every source fact from the beginning, so any stored row whose
// source unit is absent is a source that was deleted and must go. An incremental
// round reads only new facts after a watermark, so an absent stored row is simply
// one this round did not read and must be left alone.
func DiffProjection(previous, fresh []ProjectionFact, rebuild bool) ProjectionDiff {
	previousByKey := make(map[string]ProjectionFact, len(previous))
	for _, fact := range previous {
		previousByKey[fact.Key] = fact
	}
	freshKeys := make(map[string]bool, len(fresh))
	result := ProjectionDiff{Sources: make([]ProjectionSource, 0, len(fresh))}
	for _, fact := range fresh {
		freshKeys[fact.Key] = true
		result.Sources = append(result.Sources, ProjectionSource{
			UnitID: fact.UnitID, MergeKey: fact.Key, BankID: fact.BankID,
		})
		stored, exists := previousByKey[fact.Key]
		if exists && sameProjection(stored, fact) {
			continue
		}
		if exists {
			result.Remove = append(result.Remove, stored)
		}
		result.Insert = append(result.Insert, fact)
	}
	if rebuild {
		stale := make([]string, 0)
		for key := range previousByKey {
			if !freshKeys[key] {
				stale = append(stale, key)
			}
		}
		sort.Strings(stale)
		for _, key := range stale {
			result.Remove = append(result.Remove, previousByKey[key])
		}
	}
	return result
}

// sameProjection reports whether a stored row already equals a source fact.
func sameProjection(stored, fresh ProjectionFact) bool {
	return stored.Text == fresh.Text &&
		stored.FactType == fresh.FactType &&
		sameStrings(stored.Tags, fresh.Tags) &&
		sameTime(stored.EventDate, fresh.EventDate) &&
		sameTime(stored.OccurredStart, fresh.OccurredStart) &&
		sameTime(stored.OccurredEnd, fresh.OccurredEnd) &&
		sameTime(stored.MentionedAt, fresh.MentionedAt)
}

// sameStrings reports set equality, because a tag list carries no order.
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, value := range a {
		seen[value]++
	}
	for _, value := range b {
		seen[value]--
		if seen[value] < 0 {
			return false
		}
	}
	return true
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}
