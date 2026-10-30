//go:build !pgx

package app

import "github.com/studyzy/wenyuan/internal/storage"

// newMergeStoreTagged reports no merge store in a build without the PostgreSQL
// driver: the merge job's stores are cross-schema transactions and advisory
// locks, which this build cannot offer.
func newMergeStoreTagged(storage.Store) storage.MergeStore { return nil }

// newPersonalStoreTagged reports no aggregate store in a build without the
// PostgreSQL driver: the round is a cross-schema transaction with an advisory
// lock, which this build cannot offer.
func newPersonalStoreTagged(storage.Store) storage.PersonalStore { return nil }
