package app

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The worker role is enabled by default, matching the reference's standalone
// mode where one process serves and drains, and it is advertised as a feature.
func TestWorkerEnabledByDefault(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	if !cfg.WorkerEnabled {
		t.Fatal("the worker role must default to enabled")
	}
	if !cfg.FeatureMap()["worker"] {
		t.Fatal("the worker feature flag must be reported by the version endpoint")
	}
	cfg.WorkerEnabled = false
	if cfg.FeatureMap()["worker"] {
		t.Fatal("a disabled worker must not be reported as a feature")
	}
}

// The background roles start and stop with their context, so an in-process
// worker does not outlive the server.
func TestBackgroundRolesStopWithContext(t *testing.T) {
	application, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	roles := NewBackgroundRoles(application)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := roles.Run(ctx); err != nil {
		t.Fatalf("a cancelled context must be a clean stop: %v", err)
	}
}
