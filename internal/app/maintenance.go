package app

import (
	"context"
	"errors"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/maintenance"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// NewMaintenanceLoop builds the background maintenance loop from the resolved
// configuration.
//
// It returns a loop with no jobs when nothing is configured, and the loop
// reports itself disabled, so a caller can start it unconditionally and a
// deployment that has switched the work off pays nothing for it.
func NewMaintenanceLoop(
	store storage.Store,
	model provider.Provider,
	reflection storage.ReflectRunner,
	tenants tenant.Extension,
	cfg config.MaintenanceConfig,
	vectorIndex config.VectorIndexConfig,
) *maintenance.Loop {
	return NewMaintenanceLoopWithMerge(store, model, reflection, tenants, cfg, vectorIndex, nil)
}

// NewMaintenanceLoopWithMerge builds the loop with the project merge store.
//
// The merge store is nil for a deployment that cannot merge — a single-tenant
// process, or one without a chat provider — which is what keeps the
// project.merge job out of the loop there. Keeping it a separate parameter
// rather than an interface method means the existing callers (and the offline
// test path) stay unchanged.
func NewMaintenanceLoopWithMerge(
	store storage.Store,
	model provider.Provider,
	reflection storage.ReflectRunner,
	tenants tenant.Extension,
	cfg config.MaintenanceConfig,
	vectorIndex config.VectorIndexConfig,
	merges storage.MergeStore,
) *maintenance.Loop {
	jobs := maintenanceJobsWithMerge(store, model, reflection, tenants, cfg, vectorIndex, merges)
	loop := maintenance.New(jobs, nil)
	loop.StartJitter = cfg.StartJitter
	return loop
}

// RunMaintenanceLoop drives the loop until the context ends, treating a
// cancelled context as a clean stop rather than a failure.
func RunMaintenanceLoop(ctx context.Context, loop *maintenance.Loop) error {
	if loop == nil {
		return nil
	}
	if err := loop.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
