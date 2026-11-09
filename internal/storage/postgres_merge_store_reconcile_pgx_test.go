//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// finishRound runs one round with a fixed result. It is the store-level form
// of the job: the app decides Reconcile, so a test can drive a segmented
// rebuild one segment at a time.
func (f *mergeFixture) finishRound(t *testing.T, replace MergeReplaceResult, reconcile bool) {
	t.Helper()
	f.finishRoundAs(t, replace, reconcile, false)
}

// finishRoundAs runs one round with a fixed result and an explicit "this round
// completes a rebuild" flag. A completing rebuild retires everything the replay
// did not write, so the tests that check reconciliation drive both flags.
func (f *mergeFixture) finishRoundAs(t *testing.T, replace MergeReplaceResult, reconcile, rebuildDone bool) {
	t.Helper()
	ctx := context.Background()
	var stamp time.Time
	ran, err := f.store.BeginMergeStanza(ctx, f.teamID, f.projectID, mergeRoundOpts(100, time.Minute),
		func(stanza MergeStanza) error {
			stamp = stanza.(*mergeStanza).ClaimStamp()
			return nil
		})
	if err != nil {
		t.Fatalf("BeginMergeStanza: %v", err)
	}
	if !ran {
		t.Fatal("round did not run")
	}
	if err := f.store.FinishMergeStanza(ctx, FinishMergeRequest{
		TeamID: f.teamID, ProjectID: f.projectID, TeamSchema: f.teamID,
		BankID: tenant.ProjectBankID(f.teamID, f.projectID), Stamp: stamp,
		Replace: replace, Members: []string{f.memberA, f.memberB},
		Reconcile: reconcile, Rebuild: rebuildDone, RebuildDone: rebuildDone,
	}); err != nil {
		t.Fatalf("FinishMergeStanza: %v", err)
	}
}

// TestRebuildReconcilesFactsWhoseSourcesAreGone pins the rule that replaced the
// old newest-window removal: a completed rebuild deletes a team fact only when
// no member holds a source for it. The fact whose source was deleted goes; the
// facts an earlier segment replayed stay.
func TestRebuildReconcilesFactsWhoseSourcesAreGone(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	liveOneText := "A fact that stays."
	liveTwoText := "Another fact that stays."
	goneText := "A fact that was deleted."
	liveOne := f.writeMemberFact(t, f.memberA, liveOneText)
	liveTwo := f.writeMemberFact(t, f.memberA, liveTwoText)
	gone := f.writeMemberFact(t, f.memberA, goneText)
	keyOne, keyTwo, keyGone := tenant.MergeKey(liveOneText), tenant.MergeKey(liveTwoText), tenant.MergeKey(goneText)

	f.finishRound(t, MergeReplaceResult{
		Insert: []TeamFactRow{
			{Text: liveOneText, MergeKey: keyOne, SourceUnitID: liveOne, SourceTenant: f.memberA},
			{Text: liveTwoText, MergeKey: keyTwo, SourceUnitID: liveTwo, SourceTenant: f.memberA},
			{Text: goneText, MergeKey: keyGone, SourceUnitID: gone, SourceTenant: f.memberA},
		},
		Sources: map[string][]MergeSourceRow{f.memberA: {
			{UnitID: liveOne, MergeKey: keyOne},
			{UnitID: liveTwo, MergeKey: keyTwo},
			{UnitID: gone, MergeKey: keyGone},
		}},
	}, false)
	f.deleteMemberFact(t, f.memberA, gone)

	// An earlier segment of the rebuild: it has not seen the whole bank and must
	// not remove anything yet.
	f.finishRound(t, MergeReplaceResult{Sources: map[string][]MergeSourceRow{f.memberA: {
		{UnitID: liveOne, MergeKey: keyOne},
		{UnitID: liveTwo, MergeKey: keyTwo},
	}}}, false)
	if got := f.teamFactTexts(t); len(got) != 3 {
		t.Fatalf("a partial rebuild removed content: team facts = %v", got)
	}

	// The completing segment retires what the replay did not write. The deleted
	// source has no live unit behind it, so its team fact goes; the two facts the
	// replay consumed stay.
	f.finishRoundAs(t, MergeReplaceResult{Sources: map[string][]MergeSourceRow{f.memberA: {
		{UnitID: liveOne, MergeKey: keyOne},
		{UnitID: liveTwo, MergeKey: keyTwo},
	}}}, true, false)
	got := f.teamFactTexts(t)
	if len(got) != 2 || got[0] != liveOneText || got[1] != liveTwoText {
		t.Fatalf("team facts after reconcile = %v, want the two live facts", got)
	}
	var staleLedger int
	if err := f.db.QueryRowContext(context.Background(),
		"SELECT count(*) FROM "+adminTable("merge_source_unit")+" WHERE team_tenant_id=$1 AND project_id=$2 AND tenant_id=$3 AND merge_key=$4",
		f.teamID, f.projectID, f.memberA, keyGone).Scan(&staleLedger); err != nil {
		t.Fatalf("count ledger: %v", err)
	}
	if staleLedger != 0 {
		t.Fatalf("the deleted source left %d ledger rows behind, want 0", staleLedger)
	}
}

// TestRebuildKeepsAFactAnotherMemberStillHolds pins the other side of the
// reconciliation rule: one member deleting their copy must not remove a fact
// the other member still contributes.
func TestRebuildKeepsAFactAnotherMemberStillHolds(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	text := "A fact both members hold."
	key := tenant.MergeKey(text)
	unitA := f.writeMemberFact(t, f.memberA, text)
	unitB := f.writeMemberFact(t, f.memberB, text)
	f.finishRound(t, MergeReplaceResult{
		Insert: []TeamFactRow{
			{Text: text, MergeKey: key, SourceUnitID: unitA, SourceTenant: f.memberA},
			{Text: text, MergeKey: key, SourceUnitID: unitB, SourceTenant: f.memberB},
		},
		Sources: map[string][]MergeSourceRow{
			f.memberA: {{UnitID: unitA, MergeKey: key}},
			f.memberB: {{UnitID: unitB, MergeKey: key}},
		},
	}, false)
	f.deleteMemberFact(t, f.memberA, unitA)

	f.finishRoundAs(t, MergeReplaceResult{Sources: map[string][]MergeSourceRow{f.memberB: {
		{UnitID: unitB, MergeKey: key},
	}}}, true, false)
	if got := f.teamFactTexts(t); len(got) != 1 || got[0] != text {
		t.Fatalf("a fact the other member still holds was removed: %v", got)
	}
}

// TestIncrementalRoundDoesNotReconcile pins the flag: an incremental round is
// additive, so a fact whose source vanished waits for a rebuild instead of
// being removed by a normal tick.
func TestIncrementalRoundDoesNotReconcile(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	text := "A fact whose source disappears."
	key := tenant.MergeKey(text)
	unit := f.writeMemberFact(t, f.memberA, text)
	f.finishRound(t, MergeReplaceResult{
		Insert:  []TeamFactRow{{Text: text, MergeKey: key, SourceUnitID: unit, SourceTenant: f.memberA}},
		Sources: map[string][]MergeSourceRow{f.memberA: {{UnitID: unit, MergeKey: key}}},
	}, false)
	f.deleteMemberFact(t, f.memberA, unit)

	f.finishRound(t, MergeReplaceResult{}, false)
	if got := f.teamFactTexts(t); len(got) != 1 {
		t.Fatalf("an incremental round removed a fact: %v", got)
	}
	f.finishRoundAs(t, MergeReplaceResult{}, true, false)
	if got := f.teamFactTexts(t); len(got) != 0 {
		t.Fatalf("a completed rebuild kept a fact with no live source: %v", got)
	}
}

// TestSegmentedRebuildKeepsEveryReplayedSegment pins the defect this change
// fixes: the completing round has not read the earlier segments, so it must
// not treat their facts as absent. Every live fact survives.
func TestSegmentedRebuildKeepsEveryReplayedSegment(t *testing.T) {
	f := newMergeFixture(t)
	f.ensureTeamBank(t)
	texts := []string{"segment fact one", "segment fact two", "segment fact three", "segment fact four"}
	units := make([]string, len(texts))
	keys := make([]string, len(texts))
	insert := make([]TeamFactRow, len(texts))
	sources := make([]MergeSourceRow, len(texts))
	for i, text := range texts {
		units[i] = f.writeMemberFact(t, f.memberA, text)
		keys[i] = tenant.MergeKey(text)
		insert[i] = TeamFactRow{Text: text, MergeKey: keys[i], SourceUnitID: units[i], SourceTenant: f.memberA}
		sources[i] = MergeSourceRow{UnitID: units[i], MergeKey: keys[i]}
	}
	f.finishRound(t, MergeReplaceResult{
		Insert:  insert,
		Sources: map[string][]MergeSourceRow{f.memberA: sources},
	}, false)

	// Replay the bank in two segments, only the second of which completes.
	f.finishRound(t, MergeReplaceResult{Sources: map[string][]MergeSourceRow{f.memberA: sources[:2]}}, false)
	f.finishRoundAs(t, MergeReplaceResult{Sources: map[string][]MergeSourceRow{f.memberA: sources[2:]}}, true, false)
	if got := f.teamFactTexts(t); len(got) != len(texts) {
		t.Fatalf("a segmented rebuild lost facts: got %v, want %v", got, texts)
	}
}
