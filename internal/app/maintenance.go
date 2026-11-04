package app

import (
	"context"
	"errors"
	"strings"

	"github.com/studyzy/wenyuan/internal/bankapi"
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
	return NewMaintenanceLoopWithStores(store, model, reflection, tenants, cfg, vectorIndex, merges, nil)
}

// NewMaintenanceLoopWithStores builds the loop with both cross-schema stores.
//
// It is what the server and the worker use once both planes are wired; the
// MergeStore-only variant is kept for callers that never run the aggregate.
func NewMaintenanceLoopWithStores(
	store storage.Store,
	model provider.Provider,
	reflection storage.ReflectRunner,
	tenants tenant.Extension,
	cfg config.MaintenanceConfig,
	vectorIndex config.VectorIndexConfig,
	merges storage.MergeStore,
	personals storage.PersonalStore,
) *maintenance.Loop {
	jobs := maintenanceJobsWithStores(store, model, reflection, tenants, cfg, vectorIndex, merges, personals)
	loop := maintenance.New(jobs, nil)
	loop.StartJitter = cfg.StartJitter
	return loop
}

// NewMaintenanceLoopWithBankConfig builds the loop with the deployment's own
// configuration, which is the process layer of every bank's effective
// configuration.
//
// It exists for the consolidation reconcile, whose gate is the *resolved* bank
// configuration — the deployment default overlaid by the bank's own overrides —
// not the discovery routine's answer alone (engine/maintenance.py:483-486).
func NewMaintenanceLoopWithBankConfig(
	store storage.Store,
	model provider.Provider,
	reflection storage.ReflectRunner,
	tenants tenant.Extension,
	cfg config.MaintenanceConfig,
	vectorIndex config.VectorIndexConfig,
	merges storage.MergeStore,
	personals storage.PersonalStore,
	process config.Config,
) *maintenance.Loop {
	jobs := maintenanceJobsWithBankConfig(store, model, reflection, tenants, cfg, vectorIndex, merges, personals,
		bankObservationsGate(store, process))
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

// bankObservationsGate resolves whether a bank's effective configuration enables
// observations.
//
// It uses the same layering a configuration read serves: the deployment's
// process defaults, the bank's own stored overrides on top
// (config.ResolveLayers). A bank that overrides nothing reads the deployment's
// resolved value, which is what the reference's resolve_full_config returns.
func bankObservationsGate(store storage.Store, process config.Config) bankConfigAnswers {
	defaults := bankapi.PublicConfigDefaults(process)
	return func(ctx context.Context, _ string, bankID, key string) (bool, error) {
		bank, err := store.GetBank(ctx, bankID)
		if err != nil {
			return false, err
		}
		resolved := config.ResolveLayers(bankConfigOverrides(bank), nil, defaults)
		value, _ := resolved[key].(bool)
		return value, nil
	}
}

// bankConfigOverrides is a bank's stored enable_observations override, keyed the
// way a configuration read keys it.
//
// The key is normalized the way the read path normalizes it (the
// environment-style prefix dropped, lower-cased), and only a boolean is taken: a
// stored value of another type is not an override of a boolean field, so the
// bank falls back to the deployment default exactly as a configuration read does.
func bankConfigOverrides(bank storage.Bank) map[string]any {
	overrides := make(map[string]any, 1)
	for rawKey, value := range bank.Config {
		if strings.TrimPrefix(strings.TrimPrefix(strings.ToLower(rawKey), "wenyuan_api_"), "hindsight_api_") != "enable_observations" {
			continue
		}
		if flag, ok := value.(bool); ok {
			overrides["enable_observations"] = flag
		}
	}
	return overrides
}
