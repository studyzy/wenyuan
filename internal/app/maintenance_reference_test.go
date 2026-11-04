package app

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// multiTenantExtension is a tenant extension serving several schemas, which is
// what a multi-tenant deployment looks like to a maintenance job.
type multiTenantExtension struct {
	schemas []string
}

func (m multiTenantExtension) ListTenants(context.Context) ([]tenant.Tenant, error) {
	out := make([]tenant.Tenant, 0, len(m.schemas))
	for _, schema := range m.schemas {
		out = append(out, tenant.Tenant{Schema: schema})
	}
	return out, nil
}

func (m multiTenantExtension) Authenticate(context.Context, tenant.Request) (tenant.Context, error) {
	if len(m.schemas) == 0 {
		return tenant.Context{}, errStubFailure
	}
	return tenant.Context{Schema: m.schemas[0]}, nil
}

// reconcileRejectingStore reports several stranded banks and refuses one of
// them, which is what a bank deleted between the discovery query and the insert
// looks like from the sweep's side.
type reconcileRejectingStore struct {
	storage.Store
	reject string
}

func (s reconcileRejectingStore) BanksNeedingConsolidation(context.Context, string) ([]storage.ReconcileTarget, error) {
	return []storage.ReconcileTarget{
		{Schema: "public", BankID: s.reject},
		{Schema: "public", BankID: "recoverable"},
	}, nil
}

func (s reconcileRejectingStore) EnqueueOperation(ctx context.Context, req storage.EnqueueRequest) (storage.EnqueueResult, error) {
	if req.Operation.BankID == s.reject {
		return storage.EnqueueResult{}, errStubFailure
	}
	return s.Store.EnqueueOperation(ctx, req)
}

// A schema whose purge fails must not stop the schemas after it: the reference
// catches per schema and continues (engine/maintenance.py:294-298), because one
// schema's failure would otherwise strand every later tenant's expired rows for
// a whole sweep interval.
func TestRetentionSweepContinuesPastAFailingSchema(t *testing.T) {
	ctx := context.Background()
	store := newJobStore(t)
	now := time.Now().UTC()
	for _, schema := range []string{"fail_first", "still_swept"} {
		store.SeedRetentionRow(schema, "audit_log", schema+"-old", now.AddDate(0, 0, -60))
	}
	purger := &schemaFailingPurger{inner: store, failOn: "fail_first"}
	err := purgeExpired(ctx, purgerStore{MemoryStore: store, purger: purger},
		multiTenantExtension{schemas: []string{"fail_first", "still_swept"}}, "audit_log", "started_at", 30)
	if err == nil {
		t.Fatal("a schema whose sweep failed must be reported")
	}
	if got := store.RetentionRowCount("still_swept", "audit_log"); got != 0 {
		t.Fatalf("rows left in the schema after the failing one = %d, want the sweep to have reached it", got)
	}
	if got := store.RetentionRowCount("fail_first", "audit_log"); got != 1 {
		t.Fatalf("rows in the failing schema = %d, want them left for the next sweep", got)
	}
}

// schemaFailingPurger delegates to the store but refuses one schema's purge.
type schemaFailingPurger struct {
	inner  storage.RetentionPurger
	failOn string
}

func (p *schemaFailingPurger) PurgeExpiredRows(ctx context.Context, schema, table, tsColumn string, cutoff time.Time, batchSize, maxBatches int) (int, error) {
	if schema == p.failOn {
		return 0, errStubFailure
	}
	return p.inner.PurgeExpiredRows(ctx, schema, table, tsColumn, cutoff, batchSize, maxBatches)
}

// purgerStore overrides the retention capability of the in-memory store so one
// schema's failure can be injected while the rest behaves normally.
type purgerStore struct {
	*storage.MemoryStore
	purger *schemaFailingPurger
}

func (s purgerStore) PurgeExpiredRows(ctx context.Context, schema, table, tsColumn string, cutoff time.Time, batchSize, maxBatches int) (int, error) {
	return s.purger.PurgeExpiredRows(ctx, schema, table, tsColumn, cutoff, batchSize, maxBatches)
}

// SchemasWithExpiredRows reports both schemas as holding expired rows, which is
// what the reference's discovery routine answers for them.
func (s purgerStore) SchemasWithExpiredRows(context.Context, string, string, string, int) ([]string, error) {
	return []string{"fail_first", "still_swept"}, nil
}

// A bank's enqueue failing must not strand the banks discovered after it: the
// reference catches per target (engine/maintenance.py:489-490).
func TestReconcileContinuesPastABankThatCannotBeQueued(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	for _, bank := range []string{"gone", "recoverable"} {
		if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: bank}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reconcileConsolidation(ctx, reconcileRejectingStore{Store: store, reject: "gone"},
		tenant.NewDefault("public")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "recoverable", Type: storage.OperationTypeConsolidate})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("the bank after the failing one was never scheduled (total %d)", page.Total)
	}
}

// A reconcile target in a schema no tenant serves is skipped rather than
// queued: the operation would never be claimed, and would block every later
// reconcile of that bank (engine/maintenance.py:474-478).
func TestReconcileSkipsUnknownSchemas(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "known", Name: "known"}); err != nil {
		t.Fatal(err)
	}
	discovery := reconcileTargetStore{MemoryStore: store, targets: []storage.ReconcileTarget{
		{Schema: "unserved", BankID: "stranger"},
		{Schema: "public", BankID: "known"},
	}}
	if err := reconcileConsolidation(ctx, discovery, multiTenantExtension{schemas: []string{"public"}}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	stranger, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "stranger"})
	if err != nil {
		t.Fatal(err)
	}
	if stranger.Total != 0 {
		t.Fatalf("queued %d operations into an unserved schema, want none", stranger.Total)
	}
	known, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "known"})
	if err != nil {
		t.Fatal(err)
	}
	if known.Total != 1 {
		t.Fatalf("the served schema's bank was not scheduled (total %d)", known.Total)
	}
}

// reconcileTargetStore answers the cross-schema consolidation discovery
// directly, which is what the database backend does through the reference's
// routine.
type reconcileTargetStore struct {
	*storage.MemoryStore
	targets []storage.ReconcileTarget
}

func (s reconcileTargetStore) BanksNeedingConsolidation(context.Context, string) ([]storage.ReconcileTarget, error) {
	return s.targets, nil
}

// Operation cleanup prunes only schemas the deployment serves: the discovery
// routine reports every schema owning an operations table, including ones
// tenant discovery does not claim (engine/maintenance.py:379-387).
func TestOperationCleanupPrunesOnlyServedSchemas(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -30)
	for _, id := range []string{"served", "unserved"} {
		if _, err := store.PutOperation(ctx, storage.Operation{
			OperationID: id, BankID: "b1", OperationType: storage.OperationTypeConsolidate, Status: storage.StatusCompleted,
		}); err != nil {
			t.Fatal(err)
		}
		ageOperation(t, store, id, old)
	}
	if err := pruneTerminalOperations(ctx, operationDiscoveryStore{MemoryStore: store},
		multiTenantExtension{schemas: []string{"public"}}, 7, 100); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, op := range page.Operations {
		remaining[op.OperationID] = true
	}
	if remaining["served"] {
		t.Error("the served schema's terminal operation was not pruned")
	}
}

// operationDiscoveryStore names one schema as holding expired operations and
// reports nothing for the other, through the capability the database backend
// implements.
type operationDiscoveryStore struct {
	*storage.MemoryStore
}

func (s operationDiscoveryStore) SchemasWithExpiredOperations(context.Context, string, int) ([]string, error) {
	return []string{"public"}, nil
}

// An archive sweep failure is not the prune's failure: the reference treats the
// archive delete as best-effort so a blob-store problem cannot stop rows from
// being pruned (engine/memory_engine.py:4321-4361), and the two walk the same
// window.
func TestOperationCleanupSweepsArchivesBeforePruning(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -30)
	key := storage.StoredFileKeyForBank("b1", "expired.zip")
	if err := store.PutStoredFile(ctx, storage.StoredFile{Key: key, Data: []byte("PK\x03\x04")}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "exported", BankID: "b1", OperationType: storage.OperationTypeTransferExport,
		Status: storage.StatusCompleted, ResultMetadata: map[string]any{"storage_key": key},
	}); err != nil {
		t.Fatal(err)
	}
	ageOperation(t, store, "exported", old)
	if err := pruneTerminalOperations(ctx, store, tenant.NewDefault("public"), 7, 100); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	if _, err := store.GetStoredFile(ctx, key); err == nil {
		t.Error("the export archive outlived the row that was its only handle")
	}
}

// The job set is the reference's four jobs in its tick order, with this
// service's own jobs after them (engine/maintenance.py:225-242).
func TestMaintenanceJobOrderMatchesReference(t *testing.T) {
	store := newJobStore(t)
	cfg := config.MaintenanceConfig{
		RetentionSweepInterval:         time.Hour,
		AuditLogRetentionDays:          30,
		LLMTraceEnabled:                true,
		LLMTraceRetentionDays:          7,
		ConsolidationReconcileInterval: time.Minute,
		MentalModelRefreshTick:         time.Minute,
		OperationCleanupInterval:       time.Minute,
		OperationRetentionDays:         7,
	}
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), cfg, config.VectorIndexConfig{})
	want := []string{
		"retention.audit_log", "retention.llm_requests", "consolidation.reconcile",
		"mental_model.scheduled_refresh", "operations.cleanup",
	}
	if len(jobs) < len(want) {
		t.Fatalf("jobs = %d, want at least the reference's %d", len(jobs), len(want))
	}
	for i, name := range want {
		if jobs[i].Name != name {
			t.Fatalf("job %d = %q, want %q (the reference's tick order)", i, jobs[i].Name, name)
		}
	}
}

// An observation-disabled bank is not given consolidation work, however the
// discovery routine reports it: the reference mirrors the retain-time gate on
// the *resolved* bank configuration before it schedules
// (engine/maintenance.py:483-486). Reverting the gate — scheduling on the
// discovery answer alone — makes this test queue one operation.
func TestReconcileSkipsObservationDisabledBank(t *testing.T) {
	ctx := context.Background()
	store := newJobStore(t)
	for _, bank := range []string{"observers_on", "observers_off"} {
		if _, err := store.CreateBank(ctx, storage.Bank{BankID: bank, Name: bank}); err != nil {
			t.Fatal(err)
		}
	}
	observationsOff := map[string]any{"enable_observations": false}
	if _, err := store.UpdateBank(ctx, "observers_off", storage.BankPatch{Config: &observationsOff}); err != nil {
		t.Fatal(err)
	}
	discovery := reconcileTargetStore{MemoryStore: store, targets: []storage.ReconcileTarget{
		{Schema: "public", BankID: "observers_on"},
		{Schema: "public", BankID: "observers_off"},
	}}
	enabled := func(_ context.Context, _ string, bankID, key string) (bool, error) {
		if key != "enable_observations" {
			t.Fatalf("unexpected config key %q", key)
		}
		return bankID != "observers_off", nil
	}
	if err := reconcileConsolidationWithConfig(ctx, discovery,
		tenant.NewDefault("public"), enabled); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	on, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "observers_on", Type: storage.OperationTypeConsolidate})
	if err != nil {
		t.Fatal(err)
	}
	if on.Total != 1 {
		t.Fatalf("the observation-enabled bank was not scheduled (total %d)", on.Total)
	}
	off, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "observers_off", Type: storage.OperationTypeConsolidate})
	if err != nil {
		t.Fatal(err)
	}
	if off.Total != 0 {
		t.Fatalf("queued %d consolidations for an observation-disabled bank, want none", off.Total)
	}
}

// The gate resolves the deployment default overlaid by the bank's own override,
// which is the layering a configuration read serves.
func TestBankObservationsGateResolvesOverrideOverDefault(t *testing.T) {
	ctx := context.Background()
	store := newJobStore(t)
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "quiet", Name: "quiet"}); err != nil {
		t.Fatal(err)
	}
	quiet := map[string]any{"hindsight_api_enable_observations": false}
	if _, err := store.UpdateBank(ctx, "quiet", storage.BankPatch{Config: &quiet}); err != nil {
		t.Fatal(err)
	}
	process := config.FromEnv(func(string) (string, bool) { return "", false })
	process.EnableObservations = true
	gate := bankObservationsGate(store, process)

	// A bank that overrides nothing reads the deployment default.
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "plain", Name: "plain"}); err != nil {
		t.Fatal(err)
	}
	enabled, err := gate(ctx, "public", "plain", "enable_observations")
	if err != nil || !enabled {
		t.Fatalf("plain bank: enabled=%v err=%v, want the deployment default", enabled, err)
	}
	// The bank's own switch wins, whatever prefix its stored key carries.
	enabled, err = gate(ctx, "public", "quiet", "enable_observations")
	if err != nil {
		t.Fatal(err)
	}
	if enabled {
		t.Fatal("the bank's enable_observations=false override was ignored")
	}
}
