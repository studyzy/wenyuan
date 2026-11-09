package projectmerge

import "time"

// The carry pass: how a project's team bank is filled from its members' facts.
//
// The pass is a copy, not a synthesis. A member's fact becomes a team fact with
// its own text and its own kind, and nothing is asked of a model:
//
//   - world facts and experiences are append-only in a member's bank, so there is
//     no merging for a model to decide. Two facts say what they say;
//   - a model call in the write path turns the model's health into the merge's
//     health. When it fails, the round fails, and the members' facts sit
//     unshared until it recovers. Copying has no such dependency.
//
// Only those two kinds are carried. An observation is a conclusion consolidation
// derives from a bank's facts, and it rewrites that row in place — same id, new
// text — without moving `updated_at`. A copy could therefore never follow it:
// the round's cursor would not see the rewrite, and were it to see one, the new
// text would arrive as a second fact beside the old. The team bank derives its
// own conclusions from the facts that are copied, which is why a member's
// conclusions still reach the project.
//
// Identity is the source unit, not the text. A team row mirrors exactly one
// member fact and is identified by that fact's unit id, so an edited fact keeps
// its row's identity: the round removes the row the old text produced and writes
// one for the new text. Keying on the text instead would have left both versions
// standing. Two members who happen to hold the same sentence therefore produce
// two team rows, exactly as they are two facts in either member's own bank.

// Plan is what one round writes.
type Plan struct {
	// Facts are the facts to write, in the order the round read them.
	Facts []PlannedFact
	// Replaced are the team rows an edited member fact no longer produces, in
	// the order the round found them.
	Replaced []TeamFact
}

// PlannedFact is one team fact to write.
type PlannedFact struct {
	// Fact is the member fact this team fact carries, unchanged — its text, its
	// kind and its tags. A tag that stops at the member's bank stops answering
	// recall's tag filter for everyone else in the project.
	Fact MemberFact
}

// PlanBatch decides what of a round's member facts needs writing.
//
// present is keyed by source unit id: the team rows this batch's member facts
// already produced. A fact with no row is new and is written. A fact whose row
// no longer matches it — its text, kind, tags, context, scopes or dates changed
// in the member's bank — replaces that row: the old row is listed in Replaced and
// the fact in Facts. A fact whose row still matches is left alone, which is what
// keeps the row's id and therefore the observations resting on it.
func PlanBatch(present map[string]TeamFact, fresh []MemberFact) Plan {
	plan := Plan{Facts: make([]PlannedFact, 0, len(fresh))}
	for _, fact := range fresh {
		existing, exists := present[fact.UnitID]
		if exists {
			if sameTeamFact(existing, fact) {
				continue
			}
			plan.Replaced = append(plan.Replaced, existing)
		}
		plan.Facts = append(plan.Facts, PlannedFact{Fact: fact})
	}
	return plan
}

// sameTeamFact reports whether a team row still says what its source says.
//
// The vector is not compared: it is a function of the text and the dates, both
// of which are compared, and float equality would rewrite a row for a
// re-embedding that decided nothing.
func sameTeamFact(row TeamFact, fact MemberFact) bool {
	return row.Text == fact.Text &&
		row.FactType == fact.FactType &&
		sameStringSet(row.Tags, fact.Tags) &&
		row.Context == fact.Context &&
		string(row.ObservationScopes) == string(fact.ObservationScopes) &&
		sameTime(row.EventDate, fact.EventDate) &&
		sameTime(row.OccurredStart, fact.OccurredStart) &&
		sameTime(row.OccurredEnd, fact.OccurredEnd) &&
		sameTime(row.MentionedAt, fact.MentionedAt)
}

// sameStringSet compares two tag lists as sets: a reordered tag list is the same
// fact.
func sameStringSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	counts := make(map[string]int, len(a))
	for _, value := range a {
		counts[value]++
	}
	for _, value := range b {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

// sameTime compares two optional timestamps at second precision, which is what
// the columns keep.
func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.UTC().Truncate(time.Second).Equal(b.UTC().Truncate(time.Second))
}
