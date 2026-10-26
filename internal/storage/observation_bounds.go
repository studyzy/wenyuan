package storage

import "time"

// ObservationBounds are the temporal columns an observation inherits from the
// facts behind it.
//
// The reference derives them with one rule, `_aggregate_source_fields`
// (engine/consolidation/consolidator.py:838-860):
//
//	event_date     — earliest across sources (min)
//	occurred_start — earliest across sources (min)
//	occurred_end   — latest across sources (max)
//	mentioned_at   — latest across sources (max)
//
// and every path that writes an observation applies it: a create
// (consolidator.py:2585 → :3008-3011), an update (`_TemporalBounds.merged_with`,
// consolidator.py:257-262, whose comment spells out the LEAST/GREATEST the SQL
// paths use) and a fold of one observation into another
// (engine/memories/pg/consolidation.py:118-126, :196-200, :246-251). A field
// stays absent when no source carries it, so an observation is never stamped
// with an artificial timestamp.
//
// A row whose dates were left at NULL instead reads as an observation that
// happened at no known time, which is a different fact from the one it rests
// on.
type ObservationBounds struct {
	EventDate     *time.Time
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
}

// AggregatedObservationBounds applies the reference's source-aggregation rule to
// the facts an observation rests on. A source that is absent (an identifier
// that resolved to no row) contributes nothing.
func AggregatedObservationBounds(sources []MemoryUnit) ObservationBounds {
	var bounds ObservationBounds
	for _, source := range sources {
		bounds.EventDate = earlierTime(bounds.EventDate, source.EventDate)
		bounds.OccurredStart = earlierTime(bounds.OccurredStart, source.OccurredStart)
		bounds.OccurredEnd = laterTime(bounds.OccurredEnd, source.OccurredEnd)
		bounds.MentionedAt = laterTime(bounds.MentionedAt, source.MentionedAt)
	}
	return bounds
}

// MergedWith widens these bounds with another set, the reference's
// `_TemporalBounds.merged_with`: an update keeps the earlier event and start and
// the later end and mention, with a missing value on either side ignored.
func (b ObservationBounds) MergedWith(other ObservationBounds) ObservationBounds {
	return ObservationBounds{
		EventDate:     earlierTime(b.EventDate, other.EventDate),
		OccurredStart: earlierTime(b.OccurredStart, other.OccurredStart),
		OccurredEnd:   laterTime(b.OccurredEnd, other.OccurredEnd),
		MentionedAt:   laterTime(b.MentionedAt, other.MentionedAt),
	}
}

// ApplyTo writes the bounds onto a unit. It is the single place a derived
// observation bound reaches a row.
func (b ObservationBounds) ApplyTo(unit *MemoryUnit) {
	unit.EventDate = b.EventDate
	unit.OccurredStart = b.OccurredStart
	unit.OccurredEnd = b.OccurredEnd
	unit.MentionedAt = b.MentionedAt
}

// earlierTime is the reference's `_min_date`: the earliest of two values, with
// an absent side ignored.
func earlierTime(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.Before(*current) {
		return candidate
	}
	return current
}

// laterTime is the reference's `_max_date`: the latest of two values, with an
// absent side ignored.
func laterTime(current, candidate *time.Time) *time.Time {
	if candidate == nil {
		return current
	}
	if current == nil || candidate.After(*current) {
		return candidate
	}
	return current
}
