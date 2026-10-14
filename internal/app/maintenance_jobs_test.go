package app

import (
	"context"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
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
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), config.MaintenanceConfig{})
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
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), cfg)
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
	jobs := maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), base)
	for _, job := range jobs {
		if job.Name == "retention.llm_requests" {
			t.Fatal("the model-request sweep must not run when tracing is off")
		}
	}

	base.LLMTraceEnabled = true
	jobs = maintenanceJobs(store, nil, nil, tenant.NewDefault("public"), base)
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
			ID: tc.id, BankID: "b1", OperationType: storage.OperationTypeConsolidate, Status: tc.status,
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
		remaining[op.ID] = true
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
	if _, err := store.CreateBank(ctx, storage.Bank{ID: "stranded", Name: "stranded"}); err != nil {
		t.Fatal(err)
	}
	store.SeedUnit(storage.MemoryUnit{
		ID: "u1", BankID: "stranded", Text: "a fact", Type: storage.TypeWorld,
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
