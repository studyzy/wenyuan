package app

import (
	"context"
	"errors"
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
	// prevScratch keeps the last stamp handed out.
	stamp time.Time
}

func (f *fakeMergeStore) ListMergeProjects(context.Context) ([]storage.MergeProject, error) {
	return f.projects, nil
}

func (f *fakeMergeStore) BeginMergeStanza(
	_ context.Context, _, _ string, _ int, _ time.Duration, fn func(storage.MergeStanza) error,
) (bool, error) {
	f.beginCalls++
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
		ProjectMergeInterval:        60 * time.Second,
		ProjectMergeBatchSize:       10,
		ProjectMergeRebuildMaxUnits: 5000,
		ProjectMergeMaxTeamFacts:    500,
		ProjectMergeFailThreshold:   5,
		ProjectMergeEnabled:         true,
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
func runOneRound(t *testing.T, merges *fakeMergeStore, model provider.Provider, cfg config.MaintenanceConfig) error {
	t.Helper()
	runner := &mergeRunner{merges: merges, model: model, cfg: cfg}
	return runner.runProjectMerges(context.Background())
}

// TestProjectMergeWritesTheRoundsResult is the job's happy path: one model call,
// one write, the ledger recorded, and the cursor advanced to the consumed fact.
func TestProjectMergeWritesTheRoundsResult(t *testing.T) {
	merges := &fakeMergeStore{
		projects: []storage.MergeProject{mergeTestProject()},
		input:    mergeTestInput(),
	}
	model := &answerProvider{
		name:  "fake",
		reply: `{"facts":[{"text":"The deploy uses port 15432.","sources":[0]}]}`,
	}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
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

// TestProjectMergeUncitedSourcesAreRecordedDropped is the ledger guarantee the
// rebuild depends on: a source the answer folded away is recorded as dropped, so
// its later deletion cannot force a rebuild.
func TestProjectMergeUncitedSourcesAreRecordedDropped(t *testing.T) {
	input := mergeTestInput()
	now := time.Now().UTC()
	input.Members[0].Fresh = append(input.Members[0].Fresh, storage.MergeFact{
		TenantID: "alice", UnitID: "00000000-0000-0000-0000-000000000002",
		Text: "A redundant restatement.", UpdatedAt: now,
	})
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	model := &answerProvider{
		name:  "fake",
		reply: `{"facts":[{"text":"The deploy uses port 15432.","sources":[0]}]}`,
	}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	sources := merges.finished.Replace.Sources["alice"]
	if len(sources) != 2 {
		t.Fatalf("recorded %d ledger rows, want one per source", len(sources))
	}
	var dropped int
	for _, source := range sources {
		if source.MergeKey == tenant.DroppedMergeKey {
			dropped++
		}
	}
	if dropped != 1 {
		t.Fatalf("dropped sources = %d, want 1", dropped)
	}
}

// TestProjectMergeSkipsWhenAnotherRoundHoldsTheProject pins that a taken project
// costs no model call.
func TestProjectMergeSkipsWhenAnotherRoundHoldsTheProject(t *testing.T) {
	merges := &fakeMergeStore{
		projects:  []storage.MergeProject{mergeTestProject()},
		input:     mergeTestInput(),
		skipBegin: true,
	}
	model := &answerProvider{name: "fake", reply: `{"facts":[]}`}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0 when the project is taken", model.calls)
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
	model := &answerProvider{name: "fake", reply: `{"facts":[]}`}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0 for a project with nothing new", model.calls)
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
	model := &answerProvider{name: "fake", reply: `{"facts":[]}`}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0 when the member has no bank", model.calls)
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
	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"a","sources":[0]}]}`}

	cfg := mergeTestCfg()
	cfg.ProjectMergeMaxTeamFacts = 2
	if err := runOneRound(t, merges, model, cfg); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0 over the cap", model.calls)
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
	for i := 0; i < 5; i++ {
		input.Members[0].Fresh = append(input.Members[0].Fresh, storage.MergeFact{
			TenantID: "alice", UnitID: "00000000-0000-0000-0000-00000000000" + string(rune('1'+i)),
			Text: "fact number " + string(rune('1'+i)), UpdatedAt: time.Now().UTC(),
		})
	}
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	model := &answerProvider{name: "fake", reply: `{"facts":[]}`}

	cfg := mergeTestCfg()
	cfg.ProjectMergeRebuildMaxUnits = 2
	if err := runOneRound(t, merges, model, cfg); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0 when a rebuild cannot fit", model.calls)
	}
	if merges.abandoned == nil || !strings.Contains(merges.abandoned.Error, "rebuild") {
		t.Fatalf("reported %+v, want the rebuild limit named", merges.abandoned)
	}
}

// TestProjectMergeRecordsAModelFailure pins that a model failure is counted, so
// the circuit breaker can halt a project that keeps failing.
func TestProjectMergeRecordsAModelFailure(t *testing.T) {
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: mergeTestInput()}
	model := &answerProvider{name: "fake", err: errors.New("endpoint down")}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished != nil {
		t.Fatal("a failed round must not write")
	}
	if merges.abandoned == nil || !merges.abandoned.Counting {
		t.Fatalf("reported %+v, want a counted failure", merges.abandoned)
	}
	if !strings.Contains(merges.abandoned.Error, "endpoint down") {
		t.Fatalf("error = %q, want the model's message", merges.abandoned.Error)
	}
}

// TestProjectMergeRecordsAnUnparseableAnswer pins that a malformed reply writes
// nothing and does not advance the cursors, so the next round retries.
func TestProjectMergeRecordsAnUnparseableAnswer(t *testing.T) {
	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: mergeTestInput()}
	model := &answerProvider{name: "fake", reply: "I merged everything for you!"}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished != nil {
		t.Fatal("an unparseable answer must write nothing")
	}
	if merges.abandoned == nil || !merges.abandoned.Counting {
		t.Fatalf("reported %+v, want a counted failure", merges.abandoned)
	}
}

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
	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"The deploy uses port 15432.","sources":[0]}]}`}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
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

// TestMergeEnabledRefusesWithoutStoreTeamOrModel pins the three reasons the job
// must not be registered.
func TestMergeEnabledRefusesWithoutStoreTeamOrModel(t *testing.T) {
	merges := &fakeMergeStore{}
	model := &answerProvider{name: "fake"}
	multi := testConfig()
	multi.TenantMode = config.TenantModeMulti

	cases := []struct {
		name   string
		cfg    config.Config
		merges storage.MergeStore
		model  provider.Provider
		want   bool
	}{
		{"multi with store and model", multi, merges, model, true},
		{"no store", multi, nil, model, false},
		{"single tenant", testConfig(), merges, model, false},
		{"no model", multi, merges, nil, false},
		{"no-model provider", multi, merges, &answerProvider{name: "none"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergeEnabled(tc.cfg, tc.merges, tc.model); got != tc.want {
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
	input.Members[0].Member.State.RebuildRequested = true

	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	model := &answerProvider{
		name:  "fake",
		reply: `{"facts":[{"text":"Only what the members still hold.","sources":[0]}]}`,
	}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished == nil {
		t.Fatal("the rebuild wrote nothing")
	}
	if !merges.finished.Rebuild {
		t.Fatal("the round was not marked as a rebuild")
	}
	// The previous fact is removed because the rebuild never saw it.
	removed := make(map[string]bool, len(merges.finished.Replace.Remove))
	for _, fact := range merges.finished.Replace.Remove {
		removed[fact.Text] = true
	}
	if !removed["A withdrawn member's fact."] {
		t.Fatalf("rebuild remove set = %v, want the previous fact dropped", merges.finished.Replace.Remove)
	}
	// And the members' ledger rows are replaced, not appended to.
	if len(merges.finished.Replace.RebuildMembers) == 0 {
		t.Fatal("a rebuild must replace the members' ledger rows")
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
	// The model's answer omits the previous fact; the prompt had it, so a round
	// that dropped it did so on the model's judgement rather than because it never
	// saw it.
	model := &answerProvider{
		name:  "fake",
		reply: `{"facts":[{"text":"The deploy uses port 15432.","sources":[0]}]}`,
	}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if merges.finished.Rebuild {
		t.Fatal("an incremental round was marked as a rebuild")
	}
	if len(merges.finished.Replace.RebuildMembers) != 0 {
		t.Fatalf("an incremental round replaced ledger rows: %v", merges.finished.Replace.RebuildMembers)
	}
}

// TestRebuildKeepsSurvivingRowsAndDropsTheRest pins a rebuild's fact-level effect:
// a fact whose wording survived keeps its row (so its id and observations), one
// the members no longer hold is removed, and nothing is inserted twice.
func TestRebuildKeepsSurvivingRowsAndDropsTheRest(t *testing.T) {
	survivor := "A fact both rounds agree on."
	withdrawn := "A withdrawn member's fact."
	input := mergeTestInput()
	input.Previous = []storage.TeamFactRow{
		{ID: "keep-me", Text: survivor, MergeKey: tenant.MergeKey(survivor)},
		{ID: "drop-me", Text: withdrawn, MergeKey: tenant.MergeKey(withdrawn)},
	}
	input.Members[0].Member.State.RebuildRequested = true
	input.Members[0].Fresh = []storage.MergeFact{
		{TenantID: "alice", UnitID: "00000000-0000-0000-0000-000000000001", Text: survivor, UpdatedAt: time.Now().UTC()},
	}

	merges := &fakeMergeStore{projects: []storage.MergeProject{mergeTestProject()}, input: input}
	model := &answerProvider{
		name:  "fake",
		reply: `{"facts":[{"text":"A fact both rounds agree on.","sources":[0]}]}`,
	}
	if err := runOneRound(t, merges, model, mergeTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	replace := merges.finished.Replace
	// The withdrawn fact is the only removal.
	if len(replace.Remove) != 1 || replace.Remove[0].ID != "drop-me" {
		t.Fatalf("remove = %+v, want only the withdrawn fact", replace.Remove)
	}
	// The survivor is not re-inserted: its row is already there.
	for _, fact := range replace.Insert {
		if fact.Text == survivor {
			t.Fatalf("the surviving fact was re-inserted: %+v", replace.Insert)
		}
	}
}
