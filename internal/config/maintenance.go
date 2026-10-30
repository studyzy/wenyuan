package config

import "time"

// Maintenance defaults, matching the reference cadences.
//
// Every job's interval is configuration rather than a constant because the jobs
// that delete rows whose age is measured in days have no reason to probe every
// schema every minute, and a deployment with many schemas pays for that probing
// on every process.
const (
	DefaultRetentionSweepIntervalSeconds      = 3600
	DefaultConsolidationReconcileIntervalSecs = 300
	DefaultMentalModelRefreshTickSeconds      = 300
	DefaultOperationCleanupIntervalSeconds    = 900
	DefaultMaintenanceStartJitterSeconds      = 60
	DefaultOperationRetentionDays             = 0
	DefaultOperationCleanupBatchSize          = 1000
	DefaultAuditLogRetentionDays              = -1
	DefaultProjectMergeIntervalSeconds        = 60
	DefaultProjectMergeBatchSize              = 10
	DefaultProjectMergeRebuildMaxUnits        = 5000
	DefaultProjectMergeMaxTeamFacts           = 500
	DefaultProjectMergeFailThreshold          = 5
	DefaultPersonalRollupIntervalSeconds      = 300
	DefaultPersonalRollupBatchSize            = 20
	DefaultPersonalRollupRebuildMaxUnits      = 5000
	DefaultPersonalRollupMaxTeamFacts         = 500
	DefaultPersonalRollupFailThreshold        = 5
)

// MaintenanceConfig tunes the background maintenance loop.
type MaintenanceConfig struct {
	// RetentionSweepInterval is how often the retention jobs run. Zero or
	// negative disables the sweep entirely.
	RetentionSweepInterval time.Duration
	// AuditLogRetentionDays is how long an audit row is kept. A negative value
	// keeps them forever, which is why the sweep consults this rather than
	// treating a zero as "delete everything".
	AuditLogRetentionDays int
	// ConsolidationReconcileInterval is how often banks holding stranded facts
	// are re-scheduled. Zero or negative disables the job.
	ConsolidationReconcileInterval time.Duration
	// MentalModelRefreshTick is how often scheduled mental models are checked
	// for due-ness. It is the *check* cadence, not the refresh cadence: each
	// model's own cron expression decides when it is due.
	MentalModelRefreshTick time.Duration
	// OperationCleanupInterval is how often terminal operations are pruned.
	OperationCleanupInterval time.Duration
	// OperationRetentionDays is how long a terminal operation is kept. Zero
	// disables the prune, so a deployment that wants its operation history
	// keeps it.
	OperationRetentionDays int
	// OperationCleanupBatchSize bounds how many operations one prune removes
	// per schema per run.
	OperationCleanupBatchSize int
	// StartJitter spreads the first tick of each process.
	//
	// Every job is due on the first tick, so a fleet started together — a
	// deploy, a rolling restart — would otherwise fire every cross-schema probe
	// in every process at the same instant. Spreading only the first tick is
	// enough: from then on each process keeps its own phase.
	StartJitter time.Duration
	// LLMTraceEnabled gates the model-request retention sweep, because the rows
	// only exist when tracing is on.
	LLMTraceEnabled bool
	// LLMTraceRetentionDays is how long a model-request trace row is kept.
	LLMTraceRetentionDays int
	// ProjectMergeInterval is how often a project's contributed facts are merged
	// into its team's bank. Zero or negative disables the job.
	ProjectMergeInterval time.Duration
	// ProjectMergeBatchSize bounds the new member facts one round consumes. The
	// team's previous content is always part of the input.
	ProjectMergeBatchSize int
	// ProjectMergeRebuildMaxUnits bounds a rebuild's input before the project
	// fails closed.
	ProjectMergeRebuildMaxUnits int
	// ProjectMergeMaxTeamFacts bounds the team content one round's prompt
	// carries; beyond it the round is skipped rather than failed closed.
	ProjectMergeMaxTeamFacts int
	// ProjectMergeFailThreshold is how many consecutive failed rounds halt a
	// project until an operator resets it.
	ProjectMergeFailThreshold int
	// ProjectMergeEnabled reports that a merge store was wired, which is what a
	// multi-tenant process with teams has and a single-tenant one never does.
	//
	// It is separate from the interval on purpose: the interval has a default,
	// so keying the job on it alone would start the loop on every deployment and
	// leave it ticking over work it has no store to do.
	ProjectMergeEnabled bool
	// PersonalRollupInterval is how often one personal tenant's bank watermarks
	// are folded into its aggregate bank `<tenant>::myself`.
	//
	// There is no switch for this job and no default that turns it off: a
	// deployment with a chat model always runs it. Setting the interval to zero
	// or below falls back to the default rather than disabling the work, which
	// is what "the feature cannot be switched off" means in configuration terms.
	PersonalRollupInterval time.Duration
	// PersonalRollupBatchSize bounds the new source facts one round consumes.
	// The aggregate's previous content is always part of the input.
	PersonalRollupBatchSize int
	// PersonalRollupRebuildMaxUnits bounds a rebuild's input before the
	// aggregate fails closed and empties its derived content.
	PersonalRollupRebuildMaxUnits int
	// PersonalRollupMaxTeamFacts bounds the previous aggregate content one
	// round's prompt carries; beyond it the round is skipped, not failed closed.
	PersonalRollupMaxTeamFacts int
	// PersonalRollupFailThreshold is how many consecutive failed rounds halt a
	// tenant's rollup until an operator resets it.
	PersonalRollupFailThreshold int
	// PersonalRollupEnabled reports that a rollup store was wired, which is what
	// a process with a chat model on a PostgreSQL store has. It is derived the
	// same way ProjectMergeEnabled is, and it is not a configuration switch.
	PersonalRollupEnabled bool
}

// AnyJobEnabled reports whether at least one job could actually do work, so the
// loop can skip starting on a deployment that has switched everything off.
//
// The retention sweep counts as a job only when there is a window to sweep:
// an interval alone would wake the loop on every tick to delete nothing, which
// is the cost the interval exists to bound rather than a reason to pay it.
func (m MaintenanceConfig) AnyJobEnabled() bool {
	sweepOn := m.RetentionSweepInterval > 0
	auditOn := sweepOn && m.AuditLogRetentionDays > 0
	llmOn := sweepOn && m.LLMTraceEnabled && m.LLMTraceRetentionDays > 0
	reconcileOn := m.ConsolidationReconcileInterval > 0
	refreshOn := m.MentalModelRefreshTick > 0
	cleanupOn := m.OperationCleanupInterval > 0 && m.OperationRetentionDays > 0
	mergeOn := m.ProjectMergeEnabled && m.ProjectMergeInterval > 0
	rollupOn := m.PersonalRollupEnabled && m.PersonalRollupInterval > 0
	return auditOn || llmOn || reconcileOn || refreshOn || cleanupOn || mergeOn || rollupOn
}

// resolveMaintenance reads the maintenance tuning through the shared resolution
// rules: a blank or unparseable value falls back to the default rather than
// failing startup, matching the rest of the configuration surface.
func resolveMaintenance(lookup func(string) (string, bool)) MaintenanceConfig {
	cfg := MaintenanceConfig{
		RetentionSweepInterval:         time.Duration(intFromEnv(lookup, EnvRetentionSweepIntervalSeconds, DefaultRetentionSweepIntervalSeconds)) * time.Second,
		AuditLogRetentionDays:          intFromEnv(lookup, EnvAuditLogRetentionDays, DefaultAuditLogRetentionDays),
		ConsolidationReconcileInterval: time.Duration(intFromEnv(lookup, EnvConsolidationReconcileIntervalSeconds, DefaultConsolidationReconcileIntervalSecs)) * time.Second,
		MentalModelRefreshTick:         time.Duration(intFromEnv(lookup, EnvMentalModelRefreshTickSeconds, DefaultMentalModelRefreshTickSeconds)) * time.Second,
		OperationCleanupInterval:       time.Duration(intFromEnv(lookup, EnvOperationCleanupIntervalSeconds, DefaultOperationCleanupIntervalSeconds)) * time.Second,
		OperationRetentionDays:         intFromEnv(lookup, EnvOperationRetentionDays, DefaultOperationRetentionDays),
		OperationCleanupBatchSize:      intFromEnv(lookup, EnvOperationCleanupBatchSize, DefaultOperationCleanupBatchSize),
		StartJitter:                    time.Duration(intFromEnv(lookup, EnvMaintenanceStartJitterSeconds, DefaultMaintenanceStartJitterSeconds)) * time.Second,
		ProjectMergeInterval:           time.Duration(intFromEnv(lookup, EnvProjectMergeIntervalSeconds, DefaultProjectMergeIntervalSeconds)) * time.Second,
		ProjectMergeBatchSize:          intFromEnv(lookup, EnvProjectMergeBatchSize, DefaultProjectMergeBatchSize),
		ProjectMergeRebuildMaxUnits:    intFromEnv(lookup, EnvProjectMergeRebuildMaxUnits, DefaultProjectMergeRebuildMaxUnits),
		ProjectMergeMaxTeamFacts:       intFromEnv(lookup, EnvProjectMergeMaxTeamFacts, DefaultProjectMergeMaxTeamFacts),
		ProjectMergeFailThreshold:      intFromEnv(lookup, EnvProjectMergeFailThreshold, DefaultProjectMergeFailThreshold),
		PersonalRollupInterval:         time.Duration(intFromEnv(lookup, EnvPersonalRollupIntervalSeconds, DefaultPersonalRollupIntervalSeconds)) * time.Second,
		PersonalRollupBatchSize:        intFromEnv(lookup, EnvPersonalRollupBatchSize, DefaultPersonalRollupBatchSize),
		PersonalRollupRebuildMaxUnits:  intFromEnv(lookup, EnvPersonalRollupRebuildMaxUnits, DefaultPersonalRollupRebuildMaxUnits),
		PersonalRollupMaxTeamFacts:     intFromEnv(lookup, EnvPersonalRollupMaxTeamFacts, DefaultPersonalRollupMaxTeamFacts),
		PersonalRollupFailThreshold:    intFromEnv(lookup, EnvPersonalRollupFailThreshold, DefaultPersonalRollupFailThreshold),
	}
	for _, pair := range []struct {
		target *int
		def    int
	}{
		{&cfg.ProjectMergeBatchSize, DefaultProjectMergeBatchSize},
		{&cfg.ProjectMergeRebuildMaxUnits, DefaultProjectMergeRebuildMaxUnits},
		{&cfg.ProjectMergeMaxTeamFacts, DefaultProjectMergeMaxTeamFacts},
		{&cfg.ProjectMergeFailThreshold, DefaultProjectMergeFailThreshold},
		{&cfg.PersonalRollupBatchSize, DefaultPersonalRollupBatchSize},
		{&cfg.PersonalRollupRebuildMaxUnits, DefaultPersonalRollupRebuildMaxUnits},
		{&cfg.PersonalRollupMaxTeamFacts, DefaultPersonalRollupMaxTeamFacts},
		{&cfg.PersonalRollupFailThreshold, DefaultPersonalRollupFailThreshold},
	} {
		if *pair.target <= 0 {
			*pair.target = pair.def
		}
	}
	if cfg.OperationCleanupBatchSize <= 0 {
		cfg.OperationCleanupBatchSize = DefaultOperationCleanupBatchSize
	}
	// The personal rollup is not a switch: an interval of zero means "use the
	// default", never "stop folding the user's banks into their aggregate".
	if cfg.PersonalRollupInterval <= 0 {
		cfg.PersonalRollupInterval = DefaultPersonalRollupIntervalSeconds * time.Second
	}
	return cfg
}
