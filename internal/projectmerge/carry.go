package projectmerge

// The carry pass: how a project's team bank is filled from its members' facts.
//
// The pass is a copy, not a synthesis. A member's fact becomes a team fact with
// its own text and its own kind, and nothing is asked of a model:
//
//   - world facts and experiences are append-only in a member's bank, so there is
//     no merging for a model to decide. Two facts say what they say;
//   - the one thing worth suppressing is an exact repeat, and that is a string
//     comparison the server makes itself;
//   - a model call in the write path turns the model's health into the merge's
//     health. When it fails, the round fails, and the members' facts sit
//     unshared until it recovers. Copying has no such dependency.
//
// Identity is the merge key — the SHA-256 of the normalized text — which is what
// the team bank already uses for a row's id. Two facts whose text differs only in
// whitespace are the same fact, which is the same rule the old merge applied and
// the one a member would apply reading them.

// Plan is what one round writes.
type Plan struct {
	// Facts are the facts to write, in the order the round read them.
	Facts []PlannedFact
}

// PlannedFact is one team fact to write.
type PlannedFact struct {
	// Fact is the member fact this team fact carries, unchanged — its text, its
	// kind and its tags. A tag that stops at the member's bank stops answering
	// recall's tag filter for everyone else in the project.
	Fact MemberFact
}

// PlanBatch decides which of a round's member facts need a team row.
//
// A fact is skipped when its merge key is already in the team bank — the row is
// there, and writing it again would be a second row for one fact. A repeat inside
// the batch is skipped for the same reason: the first copy is the row, so the
// second has nowhere to go.
//
// present and fresh are the facts the round read; the check is on the key alone,
// so a fact whose kind changed in a member's bank (consolidation retyping an
// observation) is still the same fact and still gets no second row.
func PlanBatch(present []TeamFact, fresh []MemberFact) Plan {
	known := make(map[string]struct{}, len(present)+len(fresh))
	for _, fact := range present {
		key := fact.MergeKey
		if key == "" {
			key = MergeKey(fact.Text)
		}
		known[key] = struct{}{}
	}
	plan := Plan{Facts: make([]PlannedFact, 0, len(fresh))}
	for _, fact := range fresh {
		key := MergeKey(fact.Text)
		if _, ok := known[key]; ok {
			continue
		}
		known[key] = struct{}{}
		plan.Facts = append(plan.Facts, PlannedFact{Fact: fact})
	}
	return plan
}
