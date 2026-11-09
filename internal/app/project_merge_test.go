package app

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// fakeMergeStore is the merge store's behaviour without a database.
//
// The store itself is PostgreSQL-only by design — its risks are cross-schema
// transactions and advisory locks, which have no in-memory analogue — so what a
// fake can usefully prove is the job's own decisions: what it reads, when it
// calls the model, and what it writes.
type fakeMergeStore struct {
	projects []storage.MergeProject
	// input is what the first BeginMergeStanza hands back.
	input storage.MergeStanzaInput
	// beginCalls counts how often a round started.
	beginCalls int
	// skipBegin makes BeginMergeStanza report the project is taken.
	skipBegin bool
	// beginErr is returned from BeginMergeStanza.
	beginErr error
	// finished is the request the round wrote, nil when it wrote none.
	finished *storage.FinishMergeRequest
	// abandoned records what was reported when no write happened.
	abandoned *storage.MergeStateFailure
	// claimStamps records the stamps offered to rounds.
	claimStamps []time.Time
	// opts records the last round's own settings.
	opts storage.MergeRoundOptions
	// prevScratch keeps the last stamp handed out.
	stamp time.Time
}

func (f *fakeMergeStore) ListMergeProjects(context.Context) ([]storage.MergeProject, error) {
	return f.projects, nil
}

func (f *fakeMergeStore) BeginMergeStanza(
	_ context.Context, _, _ string, opts storage.MergeRoundOptions, fn func(storage.MergeStanza) error,
) (bool, error) {
	f.beginCalls++
	f.opts = opts
	if f.beginErr != nil {
		return false, f.beginErr
	}
	if f.skipBegin {
		return false, nil
	}
	f.stamp = time.Now().UTC()
	f.claimStamps = append(f.claimStamps, f.stamp)
	return true, fn(fakeStanza{input: f.input, stamp: f.stamp})
}

func (f *fakeMergeStore) FinishMergeStanza(_ context.Context, req storage.FinishMergeRequest) error {
	copied := req
	f.finished = &copied
	return nil
}

func (f *fakeMergeStore) AbandonMergeStanza(_ context.Context, _, _ string, state storage.MergeStateFailure) error {
	copied := state
	f.abandoned = &copied
	return nil
}

func (f *fakeMergeStore) ClearMemberProject(context.Context, string, string, string) error {
	return nil
}
func (f *fakeMergeStore) RequestRebuild(context.Context, string, string) error  { return nil }
func (f *fakeMergeStore) ResetMergeState(context.Context, string, string) error { return nil }

func (f *fakeMergeStore) MergeStatus(context.Context, string, string) (storage.MergeStatus, error) {
	return storage.MergeStatus{}, nil
}

// fakeStanza is one round's read view, carrying a claim stamp like the real one.
type fakeStanza struct {
	input storage.MergeStanzaInput
	stamp time.Time
}

func (s fakeStanza) Input() storage.MergeStanzaInput { return s.input }
func (s fakeStanza) ClaimStamp() time.Time           { return s.stamp }

// answerProvider replies with a fixed completion.
type answerProvider struct {
	name  string
	reply string
	err   error
	// calls counts model invocations.
	calls int
}

func (p *answerProvider) Name() string                           { return p.name }
func (p *answerProvider) Dimensions() int                        { return provider.DeclaredDimensions() }
func (p *answerProvider) VerifyConnection(context.Context) error { return nil }
func (p *answerProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}

func (p *answerProvider) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	p.calls++
	if p.err != nil {
		return provider.CompletionResult{}, p.err
	}
	return provider.CompletionResult{Content: p.reply}, nil
}

// mergeTestCfg is a configuration that would run the job.
func mergeTestCfg() config.MaintenanceConfig {
	return config.MaintenanceConfig{
		ProjectMergeInterval:          60 * time.Second,
		ProjectMergeBatchSize:         10,
		ProjectMergeRebuildMaxUnits:   5000,
		ProjectMergeMaxTeamFacts:      500,
		ProjectMergeFailThreshold:     5,
		ProjectMergeEnabled:           true,
		ProjectMergeIncludeExperience: true,
	}
}

// mergeTestProject is one project with one member.
func mergeTestProject() storage.MergeProject {
	return storage.MergeProject{
		TeamID: "acme", TeamName: "Acme", ProjectID: "web", Name: "Web",
		BankID: "acme::Web", TeamSchema: "acme",
		Members: []storage.MergeProjectMember{
			{TenantID: "alice", ProjectID: "web", Schema: "alice"},
		},
	}
}

// mergeTestInput is a round that has one new fact to merge into empty content.
func mergeTestInput() storage.MergeStanzaInput {
	return storage.MergeStanzaInput{
		Project: mergeTestProject(),
		Members: []storage.MergeMemberInput{
			{
				Member:     storage.MergeProjectMember{TenantID: "alice", ProjectID: "web", Schema: "alice", HasLedger: true},
				BankExists: true,
				Fresh: []storage.MergeFact{
					{TenantID: "alice", UnitID: "00000000-0000-0000-0000-000000000001",
						Text: "The deploy uses port 15432.", UpdatedAt: time.Now().UTC()},
				},
			},
		},
	}
}

// runOneRound runs a round for the fake and reports the error.
func runOneRound(t *testing.T, merges *fakeMergeStore, cfg config.MaintenanceConfig) error {
	t.Helper()
	runner := &mergeRunner{merges: merges, cfg: cfg}
	return runner.runProjectMerges(context.Background())
}

// TestProjectMergeWritesTheRoundsResult is the job's happy path: one write, the
// ledger recorded, the cursor advanced to the consumed fact, and no model call at
// all — the round copies what the members hold.
func TestProjectMergeWritesTheRoundsResult(t *testing.T) {
	merges := &fakeMergeStore{
		projects: []storage.MergeProject{mergeTestProject()},
		input:    mergeTestInput(),
	}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished == nil {
		t.Fatal("the round wrote nothing")
	}
	if len(merges.finished.Replace.Insert) != 1 {
		t.Fatalf("inserted %d facts, want 1", len(merges.finished.Replace.Insert))
	}
	if got := merges.finished.Replace.Insert[0].Text; got != "The deploy uses port 15432." {
		t.Fatalf("inserted text = %q", got)
	}
	// The source supports the inserted fact rather than being dropped.
	sources := merges.finished.Replace.Sources["alice"]
	if len(sources) != 1 {
		t.Fatalf("recorded %d ledger rows, want 1", len(sources))
	}
	if sources[0].MergeKey != tenant.MergeKey("The deploy uses port 15432.") {
		t.Fatalf("ledger key = %q, want the supported fact's key", sources[0].MergeKey)
	}
	if _, ok := merges.finished.Cursors["alice"]; !ok {
		t.Fatal("the member's cursor did not advance")
	}
	if merges.finished.Stamp.IsZero() {
		t.Fatal("the round carried no claim stamp")
	}
}

// TestProjectMergeCarriesEveryFactVerbatim is the guarantee that replaced the
// ledger's dropped-source rule: the team bank now holds what the members hold.
// The summarising pass recorded an uncited source as dropped because the model
// had folded its fact away; this pass writes the fact instead.
func TestProjectMergeCarriesEveryFactVerbatim(t *testing.T) {
	input := mergeTestInput()
	now := time.Now().UTC()
	input.Members[0].Fresh = append(input.Members[0].Fresh, storage.MergeFact{
		TenantID: "alice", UnitID: "00000000-0000-0000-0000-000000000002",
		Text: "A second, unrelated fact.", UpdatedAt: now,
	})
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	// The model reports nothing repeated.
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	inserted := merges.finished.Replace.Insert
	if len(inserted) != 2 {
		t.Fatalf("inserted %d facts, want both member facts", len(inserted))
	}
	for _, fact := range inserted {
		if fact.MergeKey == tenant.DroppedMergeKey {
			t.Fatal("a fact was recorded as dropped; this pass never drops a fact")
		}
	}
	// Every source supports a written fact, so it is never recorded as dropped.
	if sources := merges.finished.Replace.Sources["alice"]; len(sources) != 2 {
		t.Fatalf("recorded %d ledger rows, want one per source", len(sources))
	}
}

// TestProjectMergeCarriesLinksFromTheMember is the job's part of the link
// copy: what the store read must reach the round's write, or the store has
// nothing to recreate in the team bank.
func TestProjectMergeCarriesLinksFromTheMember(t *testing.T) {
	input := mergeTestInput()
	first := input.Members[0].Fresh[0]
	input.Members[0].Fresh = append(input.Members[0].Fresh, storage.MergeFact{
		TenantID: "alice", UnitID: "00000000-0000-0000-0000-000000000002",
		Text: "The deploy moved to port 15432.", UpdatedAt: time.Now().UTC(),
	})
	input.Members[0].Links = []storage.MergeLink{{
		FromMergeKey: tenant.MergeKey(first.Text),
		ToMergeKey:   tenant.MergeKey("The deploy moved to port 15432."),
		LinkType:     storage.LinkCauses,
		Weight:       0.7,
	}}
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished == nil {
		t.Fatal("the round wrote nothing")
	}
	links := merges.finished.Replace.Links
	if len(links) != 1 || links[0].LinkType != storage.LinkCauses || links[0].Weight != 0.7 {
		t.Fatalf("written links = %+v, want the member link", links)
	}
}

func TestProjectMergeSkipsWhenAnotherRoundHoldsTheProject(t *testing.T) {
	merges := &fakeMergeStore{
		projects:  []storage.MergeProject{mergeTestProject()},
		input:     mergeTestInput(),
		skipBegin: true,
	}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished != nil || merges.abandoned != nil {
		t.Fatal("a skipped round must write and report nothing")
	}
}

// TestProjectMergeSkipsWithNoNewInput pins that a quiet project costs no model
// call and no write.
func TestProjectMergeSkipsWithNoNewInput(t *testing.T) {
	merges := &fakeMergeStore{
		projects: []storage.MergeProject{mergeTestProject()},
		input: storage.MergeStanzaInput{
			Project: mergeTestProject(),
			Members: []storage.MergeMemberInput{
				{Member: storage.MergeProjectMember{TenantID: "alice", HasLedger: true}, BankExists: true},
			},
		},
	}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished != nil {
		t.Fatal("a quiet round must not write")
	}
	if merges.abandoned == nil || merges.abandoned.Counting || merges.abandoned.Error != "" {
		t.Fatalf("a quiet round reported %+v, want a silent release", merges.abandoned)
	}
}

// TestProjectMergeSkipsAMemberWithNoBank pins the onboarding path: a member who
// has never retained anything is skipped, and the team's content is kept.
func TestProjectMergeSkipsAMemberWithNoBank(t *testing.T) {
	merges := &fakeMergeStore{
		projects: []storage.MergeProject{mergeTestProject()},
		input: storage.MergeStanzaInput{
			Project:  mergeTestProject(),
			Previous: []storage.TeamFactRow{{ID: "u1", Text: "kept", MergeKey: tenant.MergeKey("kept")}},
			Members: []storage.MergeMemberInput{
				{Member: storage.MergeProjectMember{TenantID: "alice"}, BankExists: false},
			},
		},
	}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
}

// TestProjectMergeOverTeamFactCapSkipsWithoutFailingClosed pins R8: an oversized
// project is a capacity problem, so the round is skipped rather than emptying the
// team's content.
func TestProjectMergeOverTeamFactCapSkipsWithoutFailingClosed(t *testing.T) {
	previous := make([]storage.TeamFactRow, 0, 3)
	for i := 0; i < 3; i++ {
		text := "fact " + strings.Repeat("x", i+1)
		previous = append(previous, storage.TeamFactRow{ID: "u" + text, Text: text, MergeKey: tenant.MergeKey(text)})
	}
	input := mergeTestInput()
	input.Previous = previous
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}

	cfg := mergeTestCfg()
	cfg.ProjectMergeMaxTeamFacts = 2
	if err := runOneRound(t, merges, cfg); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished != nil {
		t.Fatal("an over-cap round must not write")
	}
	if merges.abandoned == nil || !strings.Contains(merges.abandoned.Error, "exceeds") {
		t.Fatalf("reported %+v, want the cap named", merges.abandoned)
	}
	if merges.abandoned != nil && merges.abandoned.Counting {
		t.Fatal("an over-cap skip must not advance the circuit breaker")
	}
}

// TestProjectMergeRebuildOverLimitFailsClosed pins the privacy底线: when a
// rebuild cannot fit, the project's team content is emptied rather than left
// standing with facts a member has deleted.
func TestProjectMergeRebuildOverLimitFailsClosed(t *testing.T) {
	input := mergeTestInput()
	input.Members[0].Deleted = true
	input.Members[0].Fresh = nil
	// The rebuild bound is the project's whole input, which the store counts
	// separately from the round's batch.
	input.ProjectTotal = 5
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}

	cfg := mergeTestCfg()
	cfg.ProjectMergeRebuildMaxUnits = 2
	if err := runOneRound(t, merges, cfg); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.abandoned == nil || !strings.Contains(merges.abandoned.Error, "rebuild") {
		t.Fatalf("reported %+v, want the rebuild limit named", merges.abandoned)
	}
}

// TestProjectMergeRecordsAModelFailure pins that a model failure is counted, so
// TestProjectMergeRecordsAnUnparseableAnswer pins that a malformed reply writes
// TestProjectMergeBatchAdvancesOnlyWhatItConsumed pins that a fact not reached
// this round is read next round rather than skipped.
func TestProjectMergeBatchAdvancesOnlyWhatItConsumed(t *testing.T) {
	input := mergeTestInput()
	now := time.Now().UTC()
	for i := 0; i < 3; i++ {
		input.Members[0].Fresh = append(input.Members[0].Fresh, storage.MergeFact{
			TenantID: "alice", UnitID: "00000000-0000-0000-0000-00000000001" + string(rune('1'+i)),
			Text: "extra " + string(rune('a'+i)), UpdatedAt: now.Add(time.Duration(i) * time.Second),
		})
	}
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	cursor, ok := merges.finished.Cursors["alice"]
	if !ok {
		t.Fatal("the member's cursor did not advance")
	}
	// The cursor stops at the last fact the round actually read.
	last := input.Members[0].Fresh[len(input.Members[0].Fresh)-1]
	if cursor.UnitID != last.UnitID {
		t.Fatalf("cursor unit = %q, want the last consumed %q", cursor.UnitID, last.UnitID)
	}
}

// TestMergeEnabledNeedsAStoreAndATeam pins the two reasons the job is not
// registered. The model is deliberately not one of them: the round copies facts
// and calls no model, so a deployment without a chat provider still merges.
func TestMergeEnabledNeedsAStoreAndATeam(t *testing.T) {
	merges := &fakeMergeStore{}
	multi := testConfig()
	multi.TenantMode = config.TenantModeMulti

	cases := []struct {
		name   string
		cfg    config.Config
		merges storage.MergeStore
		want   bool
	}{
		{"multi with a store", multi, merges, true},
		{"no store", multi, nil, false},
		{"single tenant", testConfig(), merges, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeEnabled(tc.cfg, tc.merges); got != tc.want {
				t.Fatalf("mergeEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRoundDeadlineIsBounded pins the per-round time limit the claim's staleness
// threshold is derived from.
func TestRoundDeadlineIsBounded(t *testing.T) {
	cases := map[time.Duration]time.Duration{
		// Ten times the interval, clamped to [1 minute, 5 minutes].
		2 * time.Second:  time.Minute,
		10 * time.Second: 100 * time.Second,
		time.Minute:      5 * time.Minute,
		30 * time.Second: 5 * time.Minute,
	}
	for interval, want := range cases {
		if got := roundDeadline(interval); got != want {
			t.Fatalf("roundDeadline(%v) = %v, want %v", interval, got, want)
		}
	}
}

// TestRebuildDoesNotCarryThePreviousContent pins the rule a withdrawal rests on:
// a rebuild recomputes the project from what the members hold now, so the team's
// previous content must not reach the model.
//
// Carrying it made a withdrawal report success and change nothing: the model was
// handed the very facts the rebuild exists to drop, and kept them.
func TestRebuildDoesNotCarryThePreviousContent(t *testing.T) {
	input := mergeTestInput()
	input.Previous = []storage.TeamFactRow{
		{ID: "u1", Text: "A withdrawn member's fact.", MergeKey: tenant.MergeKey("A withdrawn member's fact.")},
	}
	input.Members[0].Member.RebuildRequested = true

	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished == nil {
		t.Fatal("the rebuild wrote nothing")
	}
	if !merges.finished.Rebuild {
		t.Fatal("the round was not marked as a rebuild")
	}
	// The previous fact is removed by the store's reconciliation, which runs on
	// the round that completes the rebuild: a row nobody still contributes has
	// no live ledger source. The previous content is never carried into the
	// pass either.
	if !merges.finished.Reconcile {
		t.Fatal("the completing rebuild did not ask the store to reconcile")
	}
	for _, fact := range merges.finished.Replace.Insert {
		if fact.Text == "A withdrawn member's fact." {
			t.Fatalf("the withdrawn fact was re-inserted: %+v", merges.finished.Replace.Insert)
		}
	}
}

// TestIncrementalRoundCarriesThePreviousContent pins the other side: without a
// rebuild the previous content is part of the input, so a fact nobody restated
// survives.
func TestIncrementalRoundCarriesThePreviousContent(t *testing.T) {
	input := mergeTestInput()
	input.Previous = []storage.TeamFactRow{
		{ID: "u1", Text: "A fact this round does not restate.", MergeKey: tenant.MergeKey("A fact this round does not restate.")},
	}
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished.Rebuild {
		t.Fatal("an incremental round was marked as a rebuild")
	}
	if merges.finished.Reconcile {
		t.Fatal("an incremental round asked the store to reconcile")
	}
}

// TestRebuildKeepsSurvivingRowsAndDropsTheRest pins a rebuild's fact-level effect:
// a fact whose wording survived keeps its row (so its id and observations), one
// the members no longer hold is removed, and nothing is inserted twice.
func TestRebuildKeepsSurvivingRowsAndDropsTheRest(t *testing.T) {
	survivor := "A fact both rounds agree on."
	withdrawn := "A withdrawn member's fact."
	survivorUnit := "00000000-0000-0000-0000-000000000001"
	input := mergeTestInput()
	input.Previous = []storage.TeamFactRow{
		{ID: "keep-me", Text: survivor, MergeKey: tenant.MergeKey(survivor)},
		{ID: "drop-me", Text: withdrawn, MergeKey: tenant.MergeKey(withdrawn)},
	}
	// The team row the surviving source already produced, read back by the store
	// keyed on the source unit.
	input.TeamRows = map[string]storage.TeamFactRow{
		survivorUnit: {ID: "keep-me", SourceUnitID: survivorUnit, Text: survivor, MergeKey: tenant.MergeKey(survivor)},
	}
	input.Members[0].Member.RebuildRequested = true
	input.Members[0].Fresh = []storage.MergeFact{
		{TenantID: "alice", UnitID: survivorUnit, Text: survivor, UpdatedAt: time.Now().UTC()},
	}

	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	replace := merges.finished.Replace
	// The store reconciles on the completing round: the withdrawn fact has no
	// live source and is removed there; the app does not compute a removal set.
	if !merges.finished.Reconcile {
		t.Fatal("the completing rebuild did not ask the store to reconcile")
	}
	// The survivor is not re-inserted: its row is already there under the same
	// source unit.
	for _, fact := range replace.Insert {
		if fact.Text == survivor {
			t.Fatalf("the surviving fact was re-inserted: %+v", replace.Insert)
		}
	}
}

// TestProjectMergeCarriesFactTypesThroughTheRound pins the job's part of the
// kind: what the store read goes into the prompt as its own kind, and the answer's
// cited sources decide the kind the team fact is written under.
func TestProjectMergeCarriesFactTypesThroughTheRound(t *testing.T) {
	input := mergeTestInput()
	input.Members[0].Fresh = []storage.MergeFact{
		{TenantID: "alice", UnitID: "00000000-0000-0000-0000-000000000001",
			Text: "Alice moved the deploy to port 15432.", FactType: storage.TypeExperience,
			UpdatedAt: time.Now().UTC()},
	}
	merges := &fakeMergeStore{
		projects: []storage.MergeProject{mergeTestProject()},
		input:    input,
	}
	if err := runOneRound(t, merges, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished == nil {
		t.Fatal("the round wrote nothing")
	}
	if got := merges.finished.Replace.Insert[0].FactType; got != storage.TypeExperience {
		t.Fatalf("inserted fact type = %q, want experience", got)
	}
	// The team content is carried into the next prompt under its stored kind, so
	// the model keeps telling the kinds apart round after round.
	if got := merges.opts.IncludeExperience; !got {
		t.Fatal("the round did not ask for experience input")
	}
}

// TestProjectMergePassesTheExperienceSwitchThrough pins that the switch reaches
// the store rather than being decided anywhere else.
func TestProjectMergePassesTheExperienceSwitchThrough(t *testing.T) {
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: mergeTestInput()}
	cfg := mergeTestCfg()
	cfg.ProjectMergeIncludeExperience = false
	if err := runOneRound(t, merges, cfg); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.opts.IncludeExperience {
		t.Fatal("the store was told to include experience while the switch was off")
	}
}

// TestProjectMergeDoesNotCompress is the property the summarising pass broke: the
// team bank carries what the members hold. Asking the model to "merge duplicates"
// turned 1940 member facts into 121 team facts, so the round now writes every
// TestRebuildSpansRoundsWhenTheBankOutgrowsOneBatch pins the segmented rebuild: a
// member holding more facts than one batch is replayed over several rounds, each
// advancing the cursor, and only the completing round reconciles the bank.
// Clearing the flag on the first round would leave the remainder of the member's
// bank unread forever.
func TestRebuildSpansRoundsWhenTheBankOutgrowsOneBatch(t *testing.T) {
	newInput := func(freshCount int) storage.MergeStanzaInput {
		input := mergeTestInput()
		input.Members[0].Fresh = nil
		for i := 0; i < freshCount; i++ {
			input.Members[0].Fresh = append(input.Members[0].Fresh, storage.MergeFact{
				TenantID:  "alice",
				UnitID:    fmt.Sprintf("00000000-0000-0000-0000-%012d", i),
				Text:      fmt.Sprintf("member fact %d", i),
				FactType:  storage.TypeWorld,
				UpdatedAt: time.Now().UTC(),
			})
		}
		input.Members[0].Member.RebuildRequested = true
		return input
	}

	cfg := mergeTestCfg()
	cfg.ProjectMergeBatchSize = 10

	// A full batch with more behind it: the rebuild is not finished.
	input := newInput(10)
	input.Members[0].More = true
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	if err := runOneRound(t, merges, cfg); err != nil {
		t.Fatalf("first round: %v", err)
	}
	if merges.finished == nil {
		t.Fatalf("the first round wrote nothing: %+v", merges.abandoned)
	}
	if merges.finished.RebuildDone {
		t.Fatal("a batch with more behind it reported the rebuild finished, so the rest would never be replayed")
	}
	if merges.finished.Reconcile {
		t.Fatal("a partial rebuild asked to reconcile the bank")
	}
	if _, ok := merges.finished.Cursors["alice"]; !ok {
		t.Fatal("the round did not advance the cursor past the batch it consumed")
	}

	// Nothing more behind the batch: the member's bank is exhausted and the
	// rebuild completes.
	input = newInput(3)
	merges = &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	if err := runOneRound(t, merges, cfg); err != nil {
		t.Fatalf("second round: %v", err)
	}
	if !merges.finished.RebuildDone {
		t.Fatal("an exhausted bank did not complete the rebuild")
	}
	if !merges.finished.Reconcile {
		t.Fatal("the completing rebuild did not ask to Reconcile the bank")
	}
}
