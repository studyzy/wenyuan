//go:build !pgx

package app

import "github.com/studyzy/wenyuan/internal/storage"

// newMergeStoreTagged reports no merge store in a build without the PostgreSQL
// driver: the merge job's stores are cross-schema transactions and advisory
// locks, which this build cannot offer.
func newMergeStoreTagged(storage.Store) storage.MergeStore { return nil }
