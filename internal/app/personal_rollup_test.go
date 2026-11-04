package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/personalrollup"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// fakePersonalStore is the aggregate store's behaviour without a database.
//
// The store itself is PostgreSQL-only by design — its risks are cross-schema
// transactions and advisory locks — so what a fake can usefully prove is the
// job's own decisions: what it reads, what it writes, and that it calls no model.
type fakePersonalStore struct {
	tenants []tenant.Tenant
	// input is what BeginPersonalRound hands back.
	input storage.PersonalRoundInput
	// beginCalls counts how often a round started.
	beginCalls int
	// skipBegin makes BeginPersonalRound report the tenant is taken.
	skipBegin bool
	// beginErr is returned from BeginPersonalRound.
	beginErr error
	// finishErr is returned from FinishPersonalRound.
	finishErr error
	// finished is the request the round wrote, nil when it wrote none.
	finished *storage.FinishPersonalRequest
	// abandoned records what was reported when no write happened.
	abandoned *storage.PersonalFailure
	// claimStamps records the stamps offered to rounds.
	claimStamps []time.Time
	// rebuilds counts RequestRebuild calls.
	rebuilds int
}

func (f *fakePersonalStore) ListRollupTenants(context.Context) ([]tenant.Tenant, error) {
	return f.tenants, nil
}

func (f *fakePersonalStore) BeginPersonalRound(
	_ context.Context, tenantID string, _ int, _ time.Duration,
	fn func(storage.PersonalRoundInput, time.Time) error,
) (bool, error) {
	f.beginCalls++
	if f.beginErr != nil {
		return false, f.beginErr
	}
	if f.skipBegin {
		return false, nil
	}
	stamp := time.Now().UTC()
	f.claimStamps = append(f.claimStamps, stamp)
	input := f.input
	input.TenantID = tenantID
	return true, fn(input, stamp)
}

func (f *fakePersonalStore) FinishPersonalRound(_ context.Context, req storage.FinishPersonalRequest) error {
	if f.finishErr != nil {
		return f.finishErr
	}
	copied := req
	f.finished = &copied
	return nil
}

func (f *fakePersonalStore) AbandonPersonalRound(_ context.Context, _ string, failure storage.PersonalFailure) error {
	copied := failure
	f.abandoned = &copied
	return nil
}

func (f *fakePersonalStore) RequestRebuild(context.Context, string) error {
	f.rebuilds++
	return nil
}

func (f *fakePersonalStore) ResetPersonalState(context.Context, string) error { return nil }

func (f *fakePersonalStore) PersonalStatus(context.Context, string, bool) (storage.PersonalStatus, error) {
	return storage.PersonalStatus{}, nil
}

func (f *fakePersonalStore) PersonalDirectory(context.Context, string) (storage.PersonalDirectory, error) {
	return storage.PersonalDirectory{}, nil
}

// rollupTestCfg is a configuration that would run the job.
func rollupTestCfg() config.MaintenanceConfig {
	return config.MaintenanceConfig{
		PersonalRollupInterval:        300 * time.Second,
		PersonalRollupBatchSize:       20,
		PersonalRollupRebuildMaxUnits: 5000,
		PersonalRollupMaxTeamFacts:    500,
		PersonalRollupFailThreshold:   5,
		PersonalRollupEnabled:         true,
	}
}

// rollupTestTenant is one person's tenant.
func rollupTestTenant() tenant.Tenant {
	return tenant.Tenant{ID: "alice", Schema: "alice"}
}

const (
	rollupFactOrders  = "The orders service deploys on port 15432."
	rollupFactBilling = "Billing stores amounts in integer cents."
)

// rollupTestInput is a round with two new facts from two different banks: one
// world and one experience, so a projection must keep both kinds.
func rollupTestInput() storage.PersonalRoundInput {
	now := time.Now().UTC()
	return storage.PersonalRoundInput{
		TenantID: "alice", Schema: "alice", BankID: "alice::myself",
		Sources: []storage.PersonalSourceBank{
			{BankID: "alice::orders", LastMergedAt: nil},
			{BankID: "alice::billing", LastMergedAt: nil},
		},
		Facts: []storage.PersonalSourceFact{
			{BankID: "alice::orders", UnitID: "00000000-0000-0000-0000-000000000001",
				Text: rollupFactOrders, FactType: "world",
				Tags: []string{"topic:deploy", "session:1"}, UpdatedAt: now},
			{BankID: "alice::billing", UnitID: "00000000-0000-0000-0000-000000000002",
				Text: rollupFactBilling, FactType: "experience",
				Tags: []string{"topic:billing"}, UpdatedAt: now},
		},
	}
}

// runOnePersonalRound runs one round for the fake and reports the error.
func runOnePersonalRound(t *testing.T, personals *fakePersonalStore, model provider.Provider, cfg config.MaintenanceConfig) error {
	t.Helper()
	runner := &personalRunner{store: personals, models: func(string) provider.Provider { return model }, cfg: cfg}
	return runner.runPersonalRounds(context.Background())
}

// TestPersonalRollupProjectsEveryFact is the job's happy path: one projected row
// per source fact, world and experience alike, a ledger row per fact, both
// watermarks advanced, and no model call at all.
func TestPersonalRollupProjectsEveryFact(t *testing.T) {
	personals := &fakePersonalStore{
		tenants: []tenant.Tenant{rollupTestTenant()},
		input:   rollupTestInput(),
	}
	model := &answerProvider{name: "fake"}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, the projection must spend none", model.calls)
	}
	if personals.finished == nil {
		t.Fatal("the round wrote nothing")
	}
	req := personals.finished
	if len(req.Insert) != 2 {
		t.Fatalf("inserted %d rows, want one per source fact", len(req.Insert))
	}
	if req.Insert[0].FactType != "world" || req.Insert[1].FactType != "experience" {
		t.Fatalf("kinds = %q/%q, want world/experience", req.Insert[0].FactType, req.Insert[1].FactType)
	}
	if len(req.Insert[0].Tags) != 2 || req.Insert[0].Tags[0] != "topic:deploy" {
		t.Fatalf("tags = %v, want the source fact's tags", req.Insert[0].Tags)
	}
	if req.Insert[0].SourceUnitID != "00000000-0000-0000-0000-000000000001" {
		t.Fatalf("source unit = %q, want the source unit id", req.Insert[0].SourceUnitID)
	}
	if req.BankID != "alice::myself" || req.TenantID != "alice" || req.Schema != "alice" {
		t.Fatalf("the write names %+v, want alice's aggregate", req)
	}
	if len(req.Sources) != 2 {
		t.Fatalf("recorded %d ledger rows, want 2", len(req.Sources))
	}
	banks := map[string]bool{}
	for _, source := range req.Sources {
		if source.MergeKey == tenant.DroppedMergeKey {
			t.Fatalf("a projected source was recorded as dropped: %+v", source)
		}
		banks[source.BankID] = true
	}
	if !banks["alice::orders"] || !banks["alice::billing"] {
		t.Fatalf("ledger banks = %v, want both", banks)
	}
	for _, bank := range []string{"alice::orders", "alice::billing"} {
		if _, ok := req.Cursors[bank]; !ok {
			t.Fatalf("the watermark of %s did not advance", bank)
		}
	}
	if req.Stamp.IsZero() {
		t.Fatal("the write carries no claim stamp")
	}
	// The projection keeps the navigation links to the source banks but invents
	// no topic vocabulary: only the old model call could produce topics.
	if len(req.Topics) != 0 {
		t.Fatalf("topics = %+v, want none", req.Topics)
	}
	if len(req.FactBanks) != 2 {
		t.Fatalf("fact banks = %+v, want one entry per projected row", req.FactBanks)
	}
}

// TestPersonalRollupSpendsNoWriteWhenNothingChanged pins the watermark's point:
// a tenant whose banks have not moved writes nothing and spends no model call.
func TestPersonalRollupSpendsNoWriteWhenNothingChanged(t *testing.T) {
	personals := &fakePersonalStore{
		tenants: []tenant.Tenant{rollupTestTenant()},
		input: storage.PersonalRoundInput{
			Previous: []storage.PersonalPreviousFact{{ID: "1", Text: "a fact"}},
		},
	}
	model := &answerProvider{name: "fake"}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 {
		t.Fatalf("model calls = %d, want 0", model.calls)
	}
	if personals.finished != nil {
		t.Fatal("an unchanged tenant was written")
	}
	if personals.abandoned == nil || personals.abandoned.Error != "" {
		t.Fatalf("abandoned = %+v, want a silent heartbeat", personals.abandoned)
	}
}

// TestPersonalRollupSkipsAClaimedTenant pins that a round another process holds
// is skipped without counting as a failure.
func TestPersonalRollupSkipsAClaimedTenant(t *testing.T) {
	personals := &fakePersonalStore{
		tenants:   []tenant.Tenant{rollupTestTenant()},
		skipBegin: true,
	}
	model := &answerProvider{name: "fake"}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 || personals.abandoned != nil || personals.finished != nil {
		t.Fatal("a claimed tenant must be left entirely alone")
	}
}

// TestPersonalRollupTreatsALostClaimAsAnotherRoundSWin pins that losing the claim
// is not reported as a failure of the tenant.
func TestPersonalRollupTreatsALostClaimAsAnotherRoundSWin(t *testing.T) {
	personals := &fakePersonalStore{
		tenants:   []tenant.Tenant{rollupTestTenant()},
		input:     rollupTestInput(),
		finishErr: storage.ErrPersonalClaimLost,
	}
	if err := runOnePersonalRound(t, personals, &answerProvider{name: "fake"}, rollupTestCfg()); err != nil {
		t.Fatalf("a lost claim must not surface as an error: %v", err)
	}
}

// TestPersonalRollupSkipsWithoutAModel pins the one thing that keeps the job
// quiet: a tenant with no model is not a failure and spends nothing.
func TestPersonalRollupSkipsWithoutAModel(t *testing.T) {
	personals := &fakePersonalStore{
		tenants: []tenant.Tenant{rollupTestTenant()},
		input:   rollupTestInput(),
	}
	if err := runOnePersonalRound(t, personals, &answerProvider{name: "none"}, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if personals.beginCalls != 0 {
		t.Fatalf("begin calls = %d, want 0", personals.beginCalls)
	}
}

// TestPersonalRollupContinuesAfterOneTenantFails pins that one tenant's failure
// does not stop the others.
func TestPersonalRollupContinuesAfterOneTenantFails(t *testing.T) {
	personals := &fakePersonalStore{
		tenants:  []tenant.Tenant{{ID: "alice", Schema: "alice"}, {ID: "bob", Schema: "bob"}},
		input:    rollupTestInput(),
		beginErr: errors.New("database down"),
	}
	if err := runOnePersonalRound(t, personals, &answerProvider{name: "fake"}, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if personals.beginCalls != 2 {
		t.Fatalf("begin calls = %d, want both tenants attempted", personals.beginCalls)
	}
}

// TestPlanPersonalRoundProjectsEveryFreshFact pins that a fact with nothing
// before it is still projected, and that identity is the source unit.
func TestPlanPersonalRoundProjectsEveryFreshFact(t *testing.T) {
	plan, err := planPersonalRound(rollupTestInput(), rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.merge {
		t.Fatal("facts with nothing before them must still be written")
	}
	if len(plan.diff.Insert) != 2 || len(plan.diff.Remove) != 0 {
		t.Fatalf("diff = %+v, want two inserts", plan.diff)
	}
	first := personalrollup.ProjectionKey("alice::orders", "00000000-0000-0000-0000-000000000001")
	if plan.diff.Insert[0].Key != first {
		t.Fatalf("first key = %q, want the source unit's projection key", plan.diff.Insert[0].Key)
	}
	if plan.diff.Insert[0].Text != rollupFactOrders || plan.diff.Insert[1].Text != rollupFactBilling {
		t.Fatalf("projected texts = %q/%q, want the source texts verbatim",
			plan.diff.Insert[0].Text, plan.diff.Insert[1].Text)
	}
}

// TestPlanPersonalRoundKeepsAnUnchangedRow pins that a fact the round already
// projected does not get a second row.
func TestPlanPersonalRoundKeepsAnUnchangedRow(t *testing.T) {
	input := rollupTestInput()
	fact := input.Facts[0]
	input.Previous = []storage.PersonalPreviousFact{
		{ID: "row-1", Text: fact.Text, MergeKey: personalrollup.ProjectionKey(fact.BankID, fact.UnitID),
			FactType: fact.FactType, Tags: fact.Tags},
	}
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	for _, inserted := range plan.diff.Insert {
		if inserted.Key == input.Previous[0].MergeKey {
			t.Fatal("an unchanged row was inserted again")
		}
	}
	if len(plan.diff.Remove) != 0 {
		t.Fatalf("remove = %+v, want the row kept", plan.diff.Remove)
	}
}

// TestPlanPersonalRoundReplacesAnEditedFact pins that an edited source fact
// replaces its projected row rather than creating a second one.
func TestPlanPersonalRoundReplacesAnEditedFact(t *testing.T) {
	input := rollupTestInput()
	fact := input.Facts[0]
	key := personalrollup.ProjectionKey(fact.BankID, fact.UnitID)
	input.Previous = []storage.PersonalPreviousFact{
		{ID: "row-1", Text: "the old wording", MergeKey: key, FactType: fact.FactType},
	}
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.diff.Remove) != 1 || plan.diff.Remove[0].ID != "row-1" {
		t.Fatalf("remove = %+v, want the old row", plan.diff.Remove)
	}
	if len(plan.diff.Insert) != 2 {
		t.Fatalf("insert = %+v, want the edited row plus the other bank's fact", plan.diff.Insert)
	}
}

// TestPlanPersonalRoundDoesNotSkipAnOversizedAggregate pins that the old prompt
// capacity limit no longer stops a projection: the number of rows can exceed the
// config value because there is no prompt to bound.
func TestPlanPersonalRoundDoesNotSkipAnOversizedAggregate(t *testing.T) {
	cfg := rollupTestCfg()
	cfg.PersonalRollupMaxTeamFacts = 1
	input := rollupTestInput()
	input.Previous = []storage.PersonalPreviousFact{
		{ID: "1", Text: "one"}, {ID: "2", Text: "two"},
	}
	plan, err := planPersonalRound(input, cfg)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.merge {
		t.Fatal("a projection must not be skipped for aggregate size")
	}
	if len(plan.diff.Insert) != 2 {
		t.Fatalf("insert = %+v, want both facts", plan.diff.Insert)
	}
}

// TestPlanPersonalRoundTreatsFirstFillAsARebuild pins the first-fill rule: a
// tenant with no watermark ledger reads every source fact in one round, so the
// aggregate starts as a complete projection instead of growing one batch per
// tick.
func TestPlanPersonalRoundTreatsFirstFillAsARebuild(t *testing.T) {
	input := rollupTestInput()
	input.FirstFill = true
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.rebuild {
		t.Fatalf("plan = %+v, want a first fill treated as a rebuild", plan)
	}
	if len(plan.diff.Insert) != 2 {
		t.Fatalf("insert = %+v, want every source fact", plan.diff.Insert)
	}
}

// TestPlanPersonalRoundFailsClosedOnAnOversizedRebuild pins the safety answer:
// a rebuild that cannot fit is refused rather than approximated.
func TestPlanPersonalRoundFailsClosedOnAnOversizedRebuild(t *testing.T) {
	cfg := rollupTestCfg()
	cfg.PersonalRollupRebuildMaxUnits = 1
	input := rollupTestInput()
	input.SourceDeleted = true
	if _, err := planPersonalRound(input, cfg); err == nil {
		t.Fatal("an oversized rebuild must be refused")
	}
}

// TestPlanPersonalRoundRebuildsOnADeletedSource pins the deletion path: a source
// that vanished from its bank makes the round recompute everything.
func TestPlanPersonalRoundRebuildsOnADeletedSource(t *testing.T) {
	input := rollupTestInput()
	input.SourceDeleted = true
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.rebuild {
		t.Fatalf("plan = %+v, want a rebuild", plan)
	}
}

// TestPlanPersonalRoundRebuildDropsAStoredRowWhoseSourceIsGone pins that a
// rebuild removes the projected rows whose source units are no longer read.
func TestPlanPersonalRoundRebuildDropsAStoredRowWhoseSourceIsGone(t *testing.T) {
	input := rollupTestInput()
	input.SourceDeleted = true
	input.Facts = input.Facts[:1]
	input.Previous = []storage.PersonalPreviousFact{
		{ID: "gone-row", Text: "a fact whose source was deleted", MergeKey: "stale-key"},
	}
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.diff.Remove) != 1 || plan.diff.Remove[0].ID != "gone-row" {
		t.Fatalf("remove = %+v, want the stale row", plan.diff.Remove)
	}
}

// TestPlanPersonalRoundAdvancesEachWatermarkToItsLastFact pins the cursor rule:
// per bank, to the last fact actually consumed.
func TestPlanPersonalRoundAdvancesEachWatermarkToItsLastFact(t *testing.T) {
	input := rollupTestInput()
	first := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	input.Facts = []storage.PersonalSourceFact{
		{BankID: "alice::orders", UnitID: "u1", Text: "a", FactType: "world", UpdatedAt: first},
		{BankID: "alice::orders", UnitID: "u2", Text: "b", FactType: "experience", UpdatedAt: second},
	}
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	cursor := plan.cursors["alice::orders"]
	if cursor.UnitID != "u2" || !cursor.UpdatedAt.Equal(second) {
		t.Fatalf("cursor = %+v, want the last fact", cursor)
	}
	if _, ok := plan.cursors["alice::billing"]; ok {
		t.Fatal("a bank with no consumed fact advanced its watermark")
	}
}

// TestBuildPersonalFinishRecordsEverySource pins that the ledger records one row
// per consumed source, with the projection key, and drops nothing.
func TestBuildPersonalFinishRecordsEverySource(t *testing.T) {
	input := rollupTestInput()
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	req := buildPersonalFinish(input, plan, time.Now().UTC())
	keys := map[string]string{}
	for _, source := range req.Sources {
		keys[source.UnitID] = source.MergeKey
	}
	for _, fact := range input.Facts {
		want := personalrollup.ProjectionKey(fact.BankID, fact.UnitID)
		if keys[fact.UnitID] != want {
			t.Fatalf("source %s = %q, want %q", fact.UnitID, keys[fact.UnitID], want)
		}
	}
	if len(req.Insert) != len(input.Facts) || len(req.Remove) != 0 {
		t.Fatalf("insert/remove = %d/%d, want one insert per fact", len(req.Insert), len(req.Remove))
	}
}

// TestPersonalRollupGateIsAboutTheProcessOnly pins the gate's shape: the aggregate
// has no configuration switch, so only a missing store keeps it out of the loop.
func TestPersonalRollupGateIsAboutTheProcessOnly(t *testing.T) {
	if personalRollupEnabled(nil) {
		t.Fatal("no store must keep the job out")
	}
	if !personalRollupEnabled(&fakePersonalStore{}) {
		t.Fatal("a wired store must register the job, whatever the interval is")
	}
	cfg := rollupTestCfg()
	cfg.PersonalRollupEnabled = true
	jobs := maintenanceJobsWithStores(storage.NewMemoryStore("public"), &stubProvider{name: "stub"},
		nil, &tenant.Default{}, cfg, config.VectorIndexConfig{}, nil, &fakePersonalStore{})
	names := map[string]bool{}
	for _, job := range jobs {
		names[job.Name] = true
	}
	if !names["personal.rollup"] {
		t.Fatal("the aggregate job was not registered for a wired store")
	}
}

// TestPersonalRollupIntervalNeverDisablesTheJob pins the product decision that the
// feature cannot be switched off: an operator setting an interval to zero gets the
// default rather than a silent stop.
func TestPersonalRollupIntervalNeverDisablesTheJob(t *testing.T) {
	cfg := config.FromEnv(func(key string) (string, bool) {
		if key == config.EnvPersonalRollupIntervalSeconds {
			return "0", true
		}
		return "", false
	})
	if cfg.Maintenance.PersonalRollupInterval <= 0 {
		t.Fatalf("interval = %v, want the default", cfg.Maintenance.PersonalRollupInterval)
	}
}
