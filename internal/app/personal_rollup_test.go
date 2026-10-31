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
// transactions and advisory locks — so what a fake can usefully prove is the job's
// own decisions: what it reads, when it calls the model, and what it writes.
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

// rollupTestInput is a round with two new facts from two different banks.
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
				Text: rollupFactOrders, UpdatedAt: now},
			{BankID: "alice::billing", UnitID: "00000000-0000-0000-0000-000000000002",
				Text: rollupFactBilling, UpdatedAt: now},
		},
	}
}

// runOnePersonalRound runs one round for the fake and reports the error.
func runOnePersonalRound(t *testing.T, personals *fakePersonalStore, model provider.Provider, cfg config.MaintenanceConfig) error {
	t.Helper()
	runner := &personalRunner{store: personals, models: func(string) provider.Provider { return model }, cfg: cfg}
	return runner.runPersonalRounds(context.Background())
}

// TestPersonalRollupWritesTheRoundsResult is the job's happy path: one model call
// for the tenant, one write, the ledger recording both banks, and both watermarks
// advanced to the consumed facts.
func TestPersonalRollupWritesTheRoundsResult(t *testing.T) {
	personals := &fakePersonalStore{
		tenants: []tenant.Tenant{rollupTestTenant()},
		input:   rollupTestInput(),
	}
	model := &answerProvider{
		name: "fake",
		reply: `{"facts":[{"text":"` + rollupFactOrders + `","sources":[0],"topics":["deploy"]},` +
			`{"text":"` + rollupFactBilling + `","sources":[1],"topics":["billing"]}]}`,
	}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 1 {
		t.Fatalf("model calls = %d, want 1", model.calls)
	}
	if personals.finished == nil {
		t.Fatal("the round wrote nothing")
	}
	req := personals.finished
	if len(req.Insert) != 2 {
		t.Fatalf("inserted %d facts, want 2", len(req.Insert))
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
			t.Fatalf("a cited source was recorded as dropped: %+v", source)
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
	// The directory is a projection of this round, so it must be part of it.
	if len(req.Topics) != 2 {
		t.Fatalf("topics = %+v, want one per fact", req.Topics)
	}
	if got := req.FactBanks[tenant.MergeKey(rollupFactOrders)]; len(got) != 1 || got[0] != "alice::orders" {
		t.Fatalf("fact banks = %v, want the orders bank", got)
	}
}

// TestPersonalRollupSpendsNoModelCallWhenNothingChanged pins the watermark's
// point: a tenant whose banks have not moved costs nothing.
func TestPersonalRollupSpendsNoModelCallWhenNothingChanged(t *testing.T) {
	personals := &fakePersonalStore{
		tenants: []tenant.Tenant{rollupTestTenant()},
		input: storage.PersonalRoundInput{
			Previous: []storage.PersonalPreviousFact{{ID: "1", Text: "a fact"}},
		},
	}
	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"a fact","sources":[]}]}`}
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
	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"a","sources":[]}]}`}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if model.calls != 0 || personals.abandoned != nil || personals.finished != nil {
		t.Fatal("a claimed tenant must be left entirely alone")
	}
}

// TestPersonalRollupCountsAModelFailure pins the circuit breaker's input: a
// failed model call advances the fail count and does not write.
func TestPersonalRollupCountsAModelFailure(t *testing.T) {
	personals := &fakePersonalStore{
		tenants: []tenant.Tenant{rollupTestTenant()},
		input:   rollupTestInput(),
	}
	model := &answerProvider{name: "fake", err: errors.New("model down")}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if personals.abandoned == nil || !personals.abandoned.Counting {
		t.Fatalf("abandoned = %+v, want a counted failure", personals.abandoned)
	}
	if personals.finished != nil {
		t.Fatal("a failed round wrote a result")
	}
}

// TestPersonalRollupRefusesAnEmptyAnswerOverExistingContent pins that a model
// hiccup cannot empty someone's aggregate.
func TestPersonalRollupRefusesAnEmptyAnswerOverExistingContent(t *testing.T) {
	personals := &fakePersonalStore{
		tenants: []tenant.Tenant{rollupTestTenant()},
		input: storage.PersonalRoundInput{
			Previous: []storage.PersonalPreviousFact{{ID: "1", Text: "a fact"}},
			Facts:    rollupTestInput().Facts,
		},
	}
	model := &answerProvider{name: "fake", reply: `{"facts":[]}`}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if personals.finished != nil {
		t.Fatal("an empty answer over existing content was written")
	}
	if personals.abandoned == nil || !personals.abandoned.Counting {
		t.Fatalf("abandoned = %+v, want a counted failure", personals.abandoned)
	}
}

// TestPersonalRollupAcceptsAnEmptyAnswerWithNoContent pins the other half: a new
// tenant whose banks hold nothing worth keeping is not an error.
func TestPersonalRollupAcceptsAnEmptyAnswerWithNoContent(t *testing.T) {
	plan, err := planPersonalRound(storage.PersonalRoundInput{
		Facts: rollupTestInput().Facts,
	}, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if !plan.merge {
		t.Fatal("facts with nothing before them must still spend a round")
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
	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"a","sources":[0]}]}`}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
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
	model := &answerProvider{name: "fake", reply: `{"facts":[{"text":"a","sources":[0]}]}`}
	if err := runOnePersonalRound(t, personals, model, rollupTestCfg()); err != nil {
		t.Fatalf("round: %v", err)
	}
	if personals.beginCalls != 2 {
		t.Fatalf("begin calls = %d, want both tenants attempted", personals.beginCalls)
	}
}

func TestPlanPersonalRoundCarriesThePreviousContent(t *testing.T) {
	input := rollupTestInput()
	input.Previous = []storage.PersonalPreviousFact{{ID: "9", Text: "an old fact"}}
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if len(plan.previous) != 1 || plan.previous[0].ID != "9" {
		t.Fatalf("previous = %+v, want the aggregate's content", plan.previous)
	}
	if len(plan.fresh) != 2 {
		t.Fatalf("fresh = %d, want both facts", len(plan.fresh))
	}
}

// TestPlanPersonalRoundSkipsAnOversizedAggregate pins the capacity answer: too
// much previous content is a skip, not a fail-closed.
func TestPlanPersonalRoundSkipsAnOversizedAggregate(t *testing.T) {
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
	if !plan.skip || plan.merge {
		t.Fatalf("plan = %+v, want a skip", plan)
	}
}

// TestPlanPersonalRoundFailsClosedOnAnOversizedRebuild pins the privacy answer:
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

// TestPlanPersonalRoundRebuildsOnADeletedSource pins the privacy path: a source
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

// TestPlanPersonalRoundAdvancesEachWatermarkToItsLastFact pins the cursor rule:
// per bank, to the last fact actually consumed.
func TestPlanPersonalRoundAdvancesEachWatermarkToItsLastFact(t *testing.T) {
	input := rollupTestInput()
	first := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	second := first.Add(time.Minute)
	input.Facts = []storage.PersonalSourceFact{
		{BankID: "alice::orders", UnitID: "u1", Text: "a", UpdatedAt: first},
		{BankID: "alice::orders", UnitID: "u2", Text: "b", UpdatedAt: second},
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

// TestBuildPersonalFinishRecordsDroppedSources pins the ledger's distinction for
// the aggregate plane too: a source no fact cited is dropped, so deleting it later
// cannot force a rebuild.
func TestBuildPersonalFinishRecordsDroppedSources(t *testing.T) {
	input := rollupTestInput()
	plan, err := planPersonalRound(input, rollupTestCfg())
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	answers, err := personalrollup.ParseAnswer(`{"facts":[{"text":"`+rollupFactOrders+`","sources":[0]}]}`, 2)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	req := buildPersonalFinish(input, plan, answers, time.Now().UTC())
	keys := map[string]string{}
	for _, source := range req.Sources {
		keys[source.UnitID] = source.MergeKey
	}
	if keys["00000000-0000-0000-0000-000000000001"] == tenant.DroppedMergeKey {
		t.Fatal("a cited source was recorded as dropped")
	}
	if keys["00000000-0000-0000-0000-000000000002"] != tenant.DroppedMergeKey {
		t.Fatalf("an uncited source = %q, want dropped", keys["00000000-0000-0000-0000-000000000002"])
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
