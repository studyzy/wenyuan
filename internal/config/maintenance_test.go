package config

import (
	"testing"
	"time"
)

// The maintenance cadences default to the reference values, so a deployment
// that configures nothing behaves as expected.
func TestMaintenanceDefaults(t *testing.T) {
	cfg := resolveMaintenance(func(string) (string, bool) { return "", false })
	if cfg.RetentionSweepInterval != 3600*time.Second {
		t.Errorf("retention sweep = %v, want 1h", cfg.RetentionSweepInterval)
	}
	if cfg.ConsolidationReconcileInterval != 300*time.Second {
		t.Errorf("reconcile = %v, want 5m", cfg.ConsolidationReconcileInterval)
	}
	if cfg.MentalModelRefreshTick != 300*time.Second {
		t.Errorf("refresh tick = %v, want 5m", cfg.MentalModelRefreshTick)
	}
	if cfg.OperationCleanupInterval != 900*time.Second {
		t.Errorf("operation cleanup = %v, want 15m", cfg.OperationCleanupInterval)
	}
	if cfg.StartJitter != 60*time.Second {
		t.Errorf("jitter = %v, want 60s", cfg.StartJitter)
	}
	if cfg.AuditLogRetentionDays != -1 {
		t.Errorf("audit retention = %d, want -1 (keep forever)", cfg.AuditLogRetentionDays)
	}
	if cfg.OperationRetentionDays != 0 {
		t.Errorf("operation retention = %d, want 0 (disabled)", cfg.OperationRetentionDays)
	}
	if cfg.OperationCleanupBatchSize != 1000 {
		t.Errorf("cleanup batch = %d, want 1000", cfg.OperationCleanupBatchSize)
	}
}

// A deployment that has switched every job off reports no work, so the loop
// does not start and does not wake periodically for nothing.
func TestMaintenanceDisabledReportsNoJobs(t *testing.T) {
	cfg := MaintenanceConfig{
		RetentionSweepInterval:         0,
		AuditLogRetentionDays:          -1,
		ConsolidationReconcileInterval: 0,
		MentalModelRefreshTick:         0,
		OperationCleanupInterval:       0,
		OperationRetentionDays:         0,
	}
	if cfg.AnyJobEnabled() {
		t.Error("a fully disabled configuration must report no jobs")
	}
	if !(MaintenanceConfig{ConsolidationReconcileInterval: time.Minute}).AnyJobEnabled() {
		t.Error("one enabled job is enough to start the loop")
	}
}

// A retention window of zero means "keep forever" rather than "delete
// everything", so it must not count as a job.
func TestRetentionWindowOfZeroIsNotAJob(t *testing.T) {
	cfg := MaintenanceConfig{
		RetentionSweepInterval: time.Hour,
		AuditLogRetentionDays:  0,
	}
	if cfg.AnyJobEnabled() {
		t.Error("a zero retention window must not enable a sweep")
	}
	// The sweep interval alone is enough to run the sweep, which then finds
	// nothing to remove.
	cfg.AuditLogRetentionDays = 30
	if !cfg.AnyJobEnabled() {
		t.Error("a positive retention window with a sweep interval must enable it")
	}
}

// A configured value overrides its default.
func TestMaintenanceEnvironmentOverrides(t *testing.T) {
	values := map[string]string{
		EnvRetentionSweepIntervalSeconds: "120",
		EnvAuditLogRetentionDays:         "7",
		EnvOperationCleanupBatchSize:     "50",
	}
	cfg := resolveMaintenance(func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	})
	if cfg.RetentionSweepInterval != 120*time.Second {
		t.Errorf("retention sweep = %v", cfg.RetentionSweepInterval)
	}
	if cfg.AuditLogRetentionDays != 7 {
		t.Errorf("audit retention = %d", cfg.AuditLogRetentionDays)
	}
	if cfg.OperationCleanupBatchSize != 50 {
		t.Errorf("cleanup batch = %d", cfg.OperationCleanupBatchSize)
	}
}

// A non-positive batch size falls back to the default, because a batch of zero
// would make the prune loop forever without removing anything.
func TestCleanupBatchSizeFallsBack(t *testing.T) {
	for _, raw := range []string{"0", "-5", "not-a-number"} {
		cfg := resolveMaintenance(func(key string) (string, bool) {
			if key == EnvOperationCleanupBatchSize {
				return raw, true
			}
			return "", false
		})
		if cfg.OperationCleanupBatchSize != DefaultOperationCleanupBatchSize {
			t.Errorf("batch size from %q = %d, want the default", raw, cfg.OperationCleanupBatchSize)
		}
	}
}

// The environment key names are the deployment contract and must match the
// reference verbatim, so an operator's existing configuration keeps working.
func TestMaintenanceEnvNamesMatchTheContract(t *testing.T) {
	want := map[string]string{
		"WENYUAN_API_RETENTION_SWEEP_INTERVAL_SECONDS":         EnvRetentionSweepIntervalSeconds,
		"WENYUAN_API_AUDIT_LOG_RETENTION_DAYS":                 EnvAuditLogRetentionDays,
		"WENYUAN_API_CONSOLIDATION_RECONCILE_INTERVAL_SECONDS": EnvConsolidationReconcileIntervalSeconds,
		"WENYUAN_API_MENTAL_MODEL_REFRESH_TICK_SECONDS":        EnvMentalModelRefreshTickSeconds,
		"WENYUAN_API_OPERATION_CLEANUP_INTERVAL_SECONDS":       EnvOperationCleanupIntervalSeconds,
		"WENYUAN_API_OPERATION_RETENTION_DAYS":                 EnvOperationRetentionDays,
		"WENYUAN_API_OPERATION_CLEANUP_BATCH_SIZE":             EnvOperationCleanupBatchSize,
		"WENYUAN_API_MAINTENANCE_START_JITTER_SECONDS":         EnvMaintenanceStartJitterSeconds,
	}
	for name, actual := range want {
		if actual != name {
			t.Errorf("env key = %q, want %q", actual, name)
		}
	}
}
