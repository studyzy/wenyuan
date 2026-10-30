package app

import (
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newMergeStore returns the project merge store for a store that can address the
// admin schema, or nil when this build or this deployment cannot merge.
//
// Composition asks for it unconditionally and gets nil in the builds and modes
// that have no such store, which is what keeps the project.merge job out of the
// loop rather than merely idle inside it.
func newMergeStore(store storage.Store) storage.MergeStore {
	return newMergeStoreTagged(store)
}

// NewMergeStore exposes the composition's merge-store selection to a second
// process — the standalone worker — so it starts the same job the server does.
func NewMergeStore(store storage.Store) storage.MergeStore { return newMergeStore(store) }

// newPersonalStore returns the personal aggregate store, or nil in a build or a
// deployment that cannot aggregate.
//
// Unlike the merge store this one is not gated on the tenant mode: a single-tenant
// deployment aggregates its one schema's banks too.
func newPersonalStore(store storage.Store) storage.PersonalStore {
	return newPersonalStoreTagged(store)
}

// NewPersonalStore exposes the aggregate-store selection to the worker process.
func NewPersonalStore(store storage.Store) storage.PersonalStore { return newPersonalStore(store) }

// PersonalRollupEnabled reports whether this process starts the aggregate job.
//
// The model is accepted so the worker keeps one call shape with the merge job's
// gate; whether a tenant has a model is decided per round, not here.
func PersonalRollupEnabled(personals storage.PersonalStore, _ provider.Provider) bool {
	return personalRollupEnabled(personals)
}

// MergeEnabled reports whether a deployment can merge at all.
func MergeEnabled(cfg config.Config, merges storage.MergeStore, model provider.Provider) bool {
	return mergeEnabled(cfg, merges, model)
}
