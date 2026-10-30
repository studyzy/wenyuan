//go:build pgx

package app

import "github.com/studyzy/wenyuan/internal/storage"

// newMergeStoreTagged returns the merge store when the store has an admin
// database handle.
//
// The same handle reaches the admin schema and every tenant schema, which is
// what lets one round's transaction span them. A store without one — the
// in-memory backend — has no merge store, and the job is simply not registered.
func newMergeStoreTagged(store storage.Store) storage.MergeStore {
	provider, ok := store.(adminDBProvider)
	if !ok {
		return nil
	}
	return storage.NewPostgresMergeStore(provider.AdminDB())
}
