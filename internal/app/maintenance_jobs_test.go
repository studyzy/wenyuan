package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/maintenance"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// newJobStore builds an in-memory store for a job test. The jobs discover the
// schemas to cover through the tenant extension, and the default one reports
// the single constructed schema.
func newJobStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	return storage.NewMemoryStore("public")
}

// A disabled job is omitted from the set rather than added with a zero
// interval, so the loop carries no work it will never run.
func TestMaintenanceJobsOmittedWhenDisabled(t *testing.T) {
	store := newJobStore(t)
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), config.MaintenanceConfig{}, config.VectorIndexConfig{})
	if len(jobs) != 0 {
		t.Fatalf("jobs = %d, want none for a fully disabled configuration", len(jobs))
	}
}

// Each configured job appears exactly once, so the loop runs each cadence it
// was given and nothing else.
func TestMaintenanceJobsBuiltFromConfiguration(t *testing.T) {
	store := newJobStore(t)
	cfg := config.MaintenanceConfig{
		RetentionSweepInterval:         time.Hour,
		AuditLogRetentionDays:          30,
		ConsolidationReconcileInterval: 5 * time.Minute,
		MentalModelRefreshTick:         5 * time.Minute,
		OperationCleanupInterval:       15 * time.Minute,
		OperationRetentionDays:         7,
		OperationCleanupBatchSize:      100,
	}
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), cfg, config.VectorIndexConfig{})
	names := map[string]bool{}
	for _, job := range jobs {
		if names[job.Name] {
			t.Errorf("job %q appears twice", job.Name)
		}
		names[job.Name] = true
		if job.Interval <= 0 {
			t.Errorf("job %q has a non-positive interval", job.Name)
		}
		if job.Run == nil {
			t.Errorf("job %q has no work", job.Name)
		}
	}
	for _, want := range []string{
		"retention.audit_log",
		"consolidation.reconcile",
		"mental_model.scheduled_refresh",
		"operations.cleanup",
	} {
		if !names[want] {
			t.Errorf("job %q is missing from %v", want, jobs)
		}
	}
}

// The model-request retention job exists only when tracing is on, because the
// rows it would sweep only exist then.
func TestLLMRetentionJobRequiresTracing(t *testing.T) {
	store := newJobStore(t)
	base := config.MaintenanceConfig{RetentionSweepInterval: time.Hour, LLMTraceRetentionDays: 30}
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), base, config.VectorIndexConfig{})
	for _, job := range jobs {
		if job.Name == "retention.llm_requests" {
			t.Fatal("the model-request sweep must not run when tracing is off")
		}
	}

	base.LLMTraceEnabled = true
	jobs = maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), base, config.VectorIndexConfig{})
	found := false
	for _, job := range jobs {
		if job.Name == "retention.llm_requests" {
			found = true
		}
	}
	if !found {
		t.Error("the model-request sweep must run once tracing is on")
	}
}

// The retention job removes rows past the window and keeps the rest, which is
// the whole of what a retention window means.
func TestRetentionJobPurgesOnlyExpiredRows(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	store.SeedRetentionRow("public", "audit_log", "old", now.AddDate(0, 0, -60))
	store.SeedRetentionRow("public", "audit_log", "fresh", now)

	if err := purgeExpired(ctx, store, tenant.NewDefault("public"), "audit_log", "started_at", 30); err != nil {
		t.Fatal(err)
	}
	if got := store.RetentionRowCount("public", "audit_log"); got != 1 {
		t.Fatalf("rows = %d, want only the fresh one kept", got)
	}
}

// A retention window of zero means "keep forever", so the job removes nothing
// rather than treating zero as "delete everything".
func TestRetentionJobKeepsEverythingAtZeroWindow(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	store.SeedRetentionRow("public", "audit_log", "old", time.Now().UTC().AddDate(0, 0, -365))
	if err := purgeExpired(ctx, store, tenant.NewDefault("public"), "audit_log", "started_at", 0); err != nil {
		t.Fatal(err)
	}
	if got := store.RetentionRowCount("public", "audit_log"); got != 1 {
		t.Fatalf("rows = %d, want none removed for a zero window", got)
	}
}

// The cleanup job removes settled operations past their retention and leaves
// outstanding ones alone: pruning a pending operation would lose its work.
func TestOperationCleanupPrunesOnlyTerminalOperations(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -30)
	for _, tc := range []struct {
		id     string
		status storage.OperationStatus
	}{
		{"done", storage.StatusCompleted},
		{"failed", storage.StatusFailed},
		{"pending", storage.StatusPending},
		{"running", storage.StatusProcessing},
	} {
		if _, err := store.PutOperation(ctx, storage.Operation{
			OperationID: tc.id, BankID: "b1", OperationType: storage.OperationTypeConsolidate, Status: tc.status,
		}); err != nil {
			t.Fatal(err)
		}
		// Age the row past the window. PutOperation stamps its own clock, so
		// the age is applied afterwards.
		// The store stamps its own clock on insert, so the age is applied by
		// re-recording the row with a backdated timestamp.
		ageOperation(t, store, tc.id, old)
	}
	if err := pruneTerminalOperations(ctx, store, tenant.NewDefault("public"), 7, 100); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	remaining := map[string]bool{}
	for _, op := range page.Operations {
		remaining[op.OperationID] = true
	}
	if remaining["done"] || remaining["failed"] {
		t.Errorf("settled operations were not pruned: %v", remaining)
	}
	if !remaining["pending"] || !remaining["running"] {
		t.Errorf("outstanding operations were pruned: %v", remaining)
	}
}

// ageOperation backdates one stored operation so a retention window can be
// tested without waiting for real time to pass.
//
// PutOperation stamps the current time on every insert, so the row is aged
// through the store's own accessor rather than by re-recording it.
func ageOperation(t *testing.T, store *storage.MemoryStore, id string, at time.Time) {
	t.Helper()
	if err := store.AgeOperation(id, at); err != nil {
		t.Fatal(err)
	}
}

// The reconcile job queues consolidation for a bank whose eligible facts have
// none, and does not queue a second for one that already has work.
func TestReconcileQueuesOnlyStrandedBanks(t *testing.T) {
	store := newJobStore(t)
	ctx := context.Background()
	// A bank holding an eligible fact with no consolidation queued. The fact is
	// recorded through the real write path, because the reconcile reads the
	// authoritative unit store rather than the derived recall projection.
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "stranded", Name: "stranded"}); err != nil {
		t.Fatal(err)
	}
	store.SeedUnit(storage.MemoryUnit{
		ID: "u1", BankID: "stranded", Text: "a fact", FactType: storage.TypeWorld,
	})
	if err := reconcileConsolidation(ctx, store, tenant.NewDefault("public")); err != nil {
		t.Fatal(err)
	}
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "stranded", Type: storage.OperationTypeConsolidate})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("queued %d consolidations, want 1", page.Total)
	}

	// Running it again does not queue a second while the first is outstanding.
	if err := reconcileConsolidation(ctx, store, tenant.NewDefault("public")); err != nil {
		t.Fatal(err)
	}
	page, err = store.ListOperations(ctx, storage.OperationQuery{BankID: "stranded", Type: storage.OperationTypeConsolidate})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("queued %d consolidations after a second run, want 1", page.Total)
	}
}

// The vector index sweep exists only when a threshold is configured, and it is
// a no-op on a backend that holds no per-bank indexes.
func TestVectorIndexReconcileJobGatedOnThreshold(t *testing.T) {
	store := newJobStore(t)
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), config.MaintenanceConfig{}, config.VectorIndexConfig{})
	for _, job := range jobs {
		if job.Name == "vector_index.reconcile" {
			t.Fatal("the vector index sweep must not run at a zero threshold")
		}
	}

	jobs = maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), config.MaintenanceConfig{},
		config.VectorIndexConfig{MinRows: 100, ReconcileInterval: 60})
	var sweep *maintenance.Job
	for i := range jobs {
		if jobs[i].Name == "vector_index.reconcile" {
			job := jobs[i]
			sweep = &job
		}
	}
	if sweep == nil {
		t.Fatal("a positive threshold must register the sweep")
	}
	if sweep.Interval != time.Minute {
		t.Fatalf("interval = %v", sweep.Interval)
	}
	// The in-memory store holds no per-bank indexes, so the sweep is a no-op
	// rather than an error.
	if err := sweep.Run(context.Background()); err != nil {
		t.Fatalf("sweep over a store without indexes: %v", err)
	}
}

// graphMaintainerStub records the relink/prune work the job drives and fails
// the targets a test names, so a partial batch can be observed offline.
type graphMaintainerStub struct {
	relink []storage.MaintenanceTarget
	prune  []storage.MaintenanceTarget
	fail   map[string]bool

	relinked []string
	pruned   []string
	requeued []storage.MaintenanceTarget
}

func (s *graphMaintainerStub) DrainGraphMaintenance(context.Context, int) ([]storage.MaintenanceTarget, error) {
	return s.relink, nil
}

func (s *graphMaintainerStub) RelinkUnit(_ context.Context, _, unitID string) error {
	s.relinked = append(s.relinked, unitID)
	if s.fail[unitID] {
		return errStubFailure
	}
	return nil
}

func (s *graphMaintainerStub) DrainEntityMaintenance(context.Context, int) ([]storage.MaintenanceTarget, error) {
	return s.prune, nil
}

func (s *graphMaintainerStub) PruneEntity(_ context.Context, _, entityID string) error {
	s.pruned = append(s.pruned, entityID)
	if s.fail[entityID] {
		return errStubFailure
	}
	return nil
}

func (s *graphMaintainerStub) RequeueGraphMaintenance(_ context.Context, targets []storage.MaintenanceTarget) error {
	s.requeued = append(s.requeued, targets...)
	return nil
}

func (s *graphMaintainerStub) RequeueEntityMaintenance(_ context.Context, targets []storage.MaintenanceTarget) error {
	s.requeued = append(s.requeued, targets...)
	return nil
}

var errStubFailure = errors.New("transient maintenance failure")

// A target failing partway through a claimed batch must not discard the rest of
// the batch: the drain already removed them from the queue, so the job has to
// work through all of them and return the failed one to the queue, and still
// report the failure.
func TestGraphMaintenanceKeepsBatchOnFailure(t *testing.T) {
	stub := &graphMaintainerStub{
		relink: []storage.MaintenanceTarget{
			{BankID: "b1", ID: "u1"}, {BankID: "b1", ID: "u2"}, {BankID: "b1", ID: "u3"},
		},
		prune: []storage.MaintenanceTarget{
			{BankID: "b1", ID: "e1"}, {BankID: "b1", ID: "e2"},
		},
		fail: map[string]bool{"u2": true, "e1": true},
	}
	err := drainGraphMaintenanceInSchema(context.Background(), stub)
	if err == nil {
		t.Fatal("a failed target must still surface as a job failure")
	}
	// Every target in the claimed batch is attempted: the ones after the
	// failure are not lost with the queue row that was already deleted.
	if got, want := len(stub.relinked), 3; got != want {
		t.Fatalf("relinked %v, want %d units", stub.relinked, want)
	}
	if got, want := len(stub.pruned), 2; got != want {
		t.Fatalf("pruned %v, want %d entities", stub.pruned, want)
	}
	// The failed targets go back on the queue so a later drain retries them.
	requeued := map[string]bool{}
	for _, target := range stub.requeued {
		requeued[target.ID] = true
	}
	if !requeued["u2"] || !requeued["e1"] {
		t.Fatalf("requeued %v, want both failed targets", stub.requeued)
	}
	if requeued["u1"] || requeued["u3"] || requeued["e2"] {
		t.Fatalf("requeued %v, want only the failed targets", stub.requeued)
	}
}

// A fully successful batch reports no failure.
func TestGraphMaintenanceReportsNoFailureWhenAllSucceed(t *testing.T) {
	stub := &graphMaintainerStub{
		relink: []storage.MaintenanceTarget{{BankID: "b1", ID: "u1"}},
	}
	if err := drainGraphMaintenanceInSchema(context.Background(), stub); err != nil {
		t.Fatalf("a clean batch: %v", err)
	}
	if len(stub.requeued) != 0 {
		t.Fatalf("requeued %v, want nothing on success", stub.requeued)
	}
}

// reconcileRejectingStore reports several stranded banks and refuses one of
// them, which is what a bank deleted between the discovery query and the insert
// looks like from the sweep's side.
type reconcileRejectingStore struct {
	storage.Store
	reject string
}

func (s reconcileRejectingStore) BanksNeedingConsolidation(ctx context.Context, _ string) ([]string, error) {
	return []string{s.reject, "recoverable"}, nil
}

func (s reconcileRejectingStore) EnqueueOperation(ctx context.Context, req storage.EnqueueRequest) (storage.EnqueueResult, error) {
	if req.Operation.BankID == s.reject {
		return storage.EnqueueResult{}, errors.New("bank vanished")
	}
	return s.Store.EnqueueOperation(ctx, req)
}

// One bank whose enqueue fails must not strand the banks discovered after it.
// The reference catches per target (engine/maintenance.py:479-482): a sweep that
// returned on the first error would never reach the rest.
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
