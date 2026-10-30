package app

import (
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The single-tenant compatibility clause from the design's §13.4: a deployment
// with no teams must not register the merge job, and its maintenance loop must not
// come alive because a merge interval happens to have a default.
//
// Both are easy to break later — the config value has a non-zero default, and the
// interval is the obvious knob to gate on — so they are pinned rather than left
// to review.

// jobNames returns the names of the jobs a configuration builds.
func jobNames(cfg config.MaintenanceConfig) []string {
	jobs := maintenanceJobsWithMerge(
		storage.NewMemoryStore("public"), &stubProvider{name: "stub"}, nil, &tenant.Default{}, cfg,
		config.VectorIndexConfig{}, nil)
	names := make([]string, 0, len(jobs))
	for _, job := range jobs {
		names = append(names, job.Name)
	}
	return names
}

// TestProjectMergeIsAbsentWithoutAMergeStore pins that the job needs a store to
// exist at all. This is what a single-tenant process and every offline test
// get.
func TestProjectMergeIsAbsentWithoutAMergeStore(t *testing.T) {
	cfg := config.MaintenanceConfig{
		ProjectMergeInterval:  60 * time.Second,
		ProjectMergeEnabled:   true,
		ProjectMergeBatchSize: 10,
	}
	for _, name := range jobNames(cfg) {
		if name == "project.merge" {
			t.Fatal("the merge job was registered without a merge store")
		}
	}
}

// TestProjectMergeIsAbsentWhenNotEnabled pins the explicit switch: a store alone
// does not turn the job on.
func TestProjectMergeIsAbsentWhenNotEnabled(t *testing.T) {
	cfg := config.MaintenanceConfig{
		ProjectMergeInterval:  60 * time.Second,
		ProjectMergeBatchSize: 10,
		ProjectMergeEnabled:   false,
	}
	merges := &fakeMergeStore{}
	jobs := maintenanceJobsWithMerge(
		storage.NewMemoryStore("public"), &stubProvider{name: "stub"}, nil, &tenant.Default{}, cfg,
		config.VectorIndexConfig{}, merges)
	for _, job := range jobs {
		if job.Name == "project.merge" {
			t.Fatal("the merge job ran while ProjectMergeEnabled was false")
		}
	}
}

// TestProjectMergeIsAbsentAtZeroInterval pins that the interval still disables
// the job even with a store and the switch on, which is what an operator who
// wants merging paused sets.
func TestProjectMergeIsAbsentAtZeroInterval(t *testing.T) {
	cfg := config.MaintenanceConfig{
		ProjectMergeInterval:  0,
		ProjectMergeEnabled:   true,
		ProjectMergeBatchSize: 10,
	}
	jobs := maintenanceJobsWithMerge(
		storage.NewMemoryStore("public"), &stubProvider{name: "stub"}, nil, &tenant.Default{}, cfg,
		config.VectorIndexConfig{}, &fakeMergeStore{})
	for _, job := range jobs {
		if job.Name == "project.merge" {
			t.Fatal("the merge job ran at a zero interval")
		}
	}
}

// TestAnyJobEnabledIgnoresMergeWithoutAStore is GAP-3: the merge interval has a
// default, so keying the loop's liveness on it alone would start a single-tenant
// deployment's maintenance loop over work it has no store to do.
func TestAnyJobEnabledIgnoresMergeWithoutAStore(t *testing.T) {
	cfg := config.MaintenanceConfig{
		ProjectMergeInterval: config.DefaultProjectMergeIntervalSeconds * time.Second,
		ProjectMergeEnabled:  false,
	}
	if cfg.AnyJobEnabled() {
		t.Fatal("a merge interval with no merge store must not make the loop live")
	}
	cfg.ProjectMergeEnabled = true
	if !cfg.AnyJobEnabled() {
		t.Fatal("a wired merge store with a live interval must make the loop live")
	}
}

// TestMergeStoreIsNilInSingleTenantMode pins composition: a single-tenant
// application carries no merge store, which is the single fact every other
// clause rests on.
func TestMergeStoreIsNilInSingleTenantMode(t *testing.T) {
	cfg := testConfig()
	if cfg.TenantMode != config.TenantModeSingle {
		t.Fatalf("the test default tenant mode = %q, want single", cfg.TenantMode)
	}
	if mergeEnabled(cfg, &fakeMergeStore{}, &stubProvider{name: "stub"}) {
		t.Fatal("a single-tenant deployment must never merge")
	}
}

// TestDefaultMergeIntervalDoesNotEnableTheJob is the end-to-end form of the
// compatibility clause on a default configuration: the interval carries a
// positive default, so only the store-backed switch may turn the job on.
func TestDefaultMergeIntervalDoesNotEnableTheJob(t *testing.T) {
	cfg := config.MaintenanceConfig{
		// What a deployment that configured nothing resolves to for the interval.
		ProjectMergeInterval: config.DefaultProjectMergeIntervalSeconds * time.Second,
	}
	if cfg.ProjectMergeInterval <= 0 {
		t.Fatalf("the merge interval's default = %v, want a positive default", cfg.ProjectMergeInterval)
	}
	if cfg.ProjectMergeEnabled {
		t.Fatal("a configuration that wired no store must not have merging enabled")
	}
	for _, name := range jobNames(cfg) {
		if name == "project.merge" {
			t.Fatal("a default configuration registered the merge job")
		}
	}
}

// mergeModelName is the provider name this build reports when nothing is
// configured, which mergeEnabled must treat as "cannot merge".
func TestNoModelProviderCannotMerge(t *testing.T) {
	cfg := testConfig()
	cfg.TenantMode = config.TenantModeMulti
	var model provider.Provider = &stubProvider{name: "none"}
	if mergeEnabled(cfg, &fakeMergeStore{}, model) {
		t.Fatal("the no-model selection must not merge")
	}
}
