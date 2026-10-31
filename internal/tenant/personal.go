package tenant

import "strings"

// The personal aggregate bank: `<tenant>::myself`, the one bank inside a person's
// own schema that the background rollup fills with conclusions drawn from every
// other bank that person owns.
//
// Three things define it here rather than in the storage layer, because the merge
// job, the composition root and the management plane all need to agree on them:
// the suffix, the bank id it produces, and the marker that tells a rollup row
// apart from anything else in the same bank.

// PersonalBankName is the suffix of the personal aggregate bank id. It is a
// reserved *suffix*, not a reserved bank name: `<tenant>::<project id>` for a
// project whose id happens to be "myself" is a perfectly ordinary project bank,
// and the aggregate is the one bank in that same schema that the rollup writes.
const PersonalBankName = "myself"

// PersonalBankID is the aggregate bank id inside one tenant's schema.
func PersonalBankID(tenantID string) string { return ProjectBankID(tenantID, PersonalBankName) }

// IsPersonalBankID reports whether bankID names a tenant's aggregate bank.
func IsPersonalBankID(bankID string) bool {
	prefix, suffix, ok := strings.Cut(bankID, "::")
	return ok && prefix != "" && suffix == PersonalBankName
}

// LiveLedgerKey reports whether a ledger merge key still supports content, as
// opposed to marking a source the model folded away.
//
// It is the predicate delete detection applies to the ledger: only rows that
// still support content can make that content stale by vanishing.
func LiveLedgerKey(mergeKey string) bool {
	return mergeKey != "" && mergeKey != DroppedMergeKey
}

// The metadata a rollup writes on every fact it creates. `origin` is the marker
// that separates the rollup's own rows from anything else in the bank (a user
// who retained into `<tenant>::myself` directly, an older client script), which
// is what lets a round replace its own rows without touching what it did not
// write.
const (
	// OriginKey is the metadata member that carries OriginAggregate.
	OriginKey = "origin"
	// OriginAggregate marks a fact a rollup round wrote.
	OriginAggregate = "aggregate"
	// MergeKeyMetadataKey is the metadata member carrying the fact's MergeKey.
	// It is the same spelling the project merge writes, so a row's identity is
	// read the same way on both planes.
	MergeKeyMetadataKey = "merge_key"
	// SourcesMetadataKey names the source facts an aggregate fact was made from:
	// a list of {unit_id, bank_id}. It is how a read fans out to the original.
	SourcesMetadataKey = "sources"
	// BanksMetadataKey names the source banks an aggregate fact spans, which is
	// what the directory groups on.
	BanksMetadataKey = "banks"
	// TopicsMetadataKey names the vocabulary topics an aggregate fact belongs
	// to.
	TopicsMetadataKey = "topics"
	// MergedAtMetadataKey is when the round wrote the fact.
	MergedAtMetadataKey = "merged_at"
)
