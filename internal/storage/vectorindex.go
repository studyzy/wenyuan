package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// VectorIndexReconciler is the optional capability a backend implements when it
// holds per-(bank, fact_type) partial vector indexes that need reconciling. A
// backend without them — the in-memory one — simply does not implement it.
type VectorIndexReconciler interface {
	// BanksNeedingVectorIndexes lists the banks whose coverage the sweep must
	// visit: those holding at least one partition at or above the threshold,
	// plus those still carrying one of our indexes below it, so the sweep can
	// drop it.
	BanksNeedingVectorIndexes(ctx context.Context, minRows int) ([]string, error)
	// ReconcileBankVectorIndexes brings one bank's coverage to the plan and
	// returns the changes it made.
	ReconcileBankVectorIndexes(ctx context.Context, bankID string, minRows int) ([]BankVectorIndexAction, error)
}

// VectorIndexPlanner is the optional capability a backend implements when the
// coverage plan can be asked for without applying it, and when indexes left
// behind by banks that no longer exist can be found and removed. It is what the
// admin repair command's dry run and orphan sweep are built on.
type VectorIndexPlanner interface {
	// BankVectorIndexStates reports each partition's row count and whether its
	// index exists and is usable, which is what the plan is derived from.
	BankVectorIndexStates(ctx context.Context, bankID string) ([]BankVectorIndexState, error)
	// OrphanedVectorIndexes lists the per-bank vector indexes whose bank no
	// longer exists.
	OrphanedVectorIndexes(ctx context.Context) ([]string, error)
	// DropVectorIndexes removes the named indexes. It is idempotent.
	DropVectorIndexes(ctx context.Context, names []string) error
}

// BankVectorIndexFactTypes are the fact types that earn a per-(bank, fact_type)
// partial vector index, mapped to the short suffix in the index name.
//
// The set is the reference's: raw world and experience facts plus observations.
// A fact_type outside the set has no partial index because the reference does
// not build one, and the global index still serves it.
var BankVectorIndexFactTypes = map[string]string{
	"world":       "worl",
	"experience":  "expr",
	"observation": "obsv",
}

// bankVectorIndexPrefix is the leading segment every per-(bank, fact_type)
// partial vector index shares, the store's way of recognising its own indexes in
// the catalogue.
const bankVectorIndexPrefix = "idx_mu_emb_"

// BankVectorIndexName returns the deterministic, schema-safe index name of one
// (bank, fact_type) pair.
//
// The bank identifier is folded through a digest to sixteen hex characters: an
// identifier may be arbitrary UTF-8 (up to 192 bytes), and PostgreSQL truncates
// identifiers at 63 bytes, which can split a multi-byte character and leave an
// invalid index name. A hex digest is ASCII, always within the byte budget, and
// collision-resistant in practice.
func BankVectorIndexName(factType, bankID string) string {
	suffix, ok := BankVectorIndexFactTypes[factType]
	if !ok {
		suffix = "misc"
	}
	sum := sha256.Sum256([]byte(bankID))
	uid := hex.EncodeToString(sum[:8])
	return fmt.Sprintf("%s%s_%s", bankVectorIndexPrefix, suffix, uid)
}

// BankVectorIndexState is one (bank, fact_type) partition's index situation.
type BankVectorIndexState struct {
	FactType string
	// Rows is how many memory units of this type the bank holds.
	Rows int64
	// IndexName is the name the partial index would carry.
	IndexName string
	// Present reports whether a usable index of that name exists. An index that
	// exists but is not usable is not present: see Invalid.
	Present bool
	// Invalid reports whether an index of that name exists but is unusable — an
	// interrupted CREATE INDEX CONCURRENTLY leaves a row with indisvalid false,
	// which CREATE ... IF NOT EXISTS would otherwise take as the requested index
	// and never rebuild. It is treated as absent for coverage, and the planner
	// drops it before rebuilding.
	Invalid bool
}

// BankVectorIndexAction is one planned change to a bank's index coverage.
type BankVectorIndexAction struct {
	FactType  string
	IndexName string
	// Create is true to build the index, false to drop it.
	Create bool
}

// PlanBankVectorIndexes decides the coverage changes a bank's partitions need.
//
// The rule is the reference's. With minRows at zero nothing is planned: at that
// threshold the indexes are created with the bank and dropped with it, which is
// the right behaviour for a deployment whose bank count is not the problem.
// Above zero, a partition holding at least minRows units earns its own index
// and one below the threshold loses it, because these indexes live on the
// shared memory_units table and PostgreSQL charges every index on a relation to
// every statement that names it.
//
// An unusable index is treated as absent, so a partition that still qualifies is
// rebuilt; the leftover is dropped first in the same plan, because CREATE ...
// IF NOT EXISTS would otherwise accept the invalid row as the index asked for
// and leave the partition uncovered forever.
func PlanBankVectorIndexes(states []BankVectorIndexState, minRows int) []BankVectorIndexAction {
	if minRows <= 0 {
		return nil
	}
	var actions []BankVectorIndexAction
	for _, state := range states {
		want := state.Rows >= int64(minRows)
		if state.Invalid {
			actions = append(actions, BankVectorIndexAction{FactType: state.FactType, IndexName: state.IndexName, Create: false})
			if want {
				actions = append(actions, BankVectorIndexAction{FactType: state.FactType, IndexName: state.IndexName, Create: true})
			}
			continue
		}
		switch {
		case want && !state.Present:
			actions = append(actions, BankVectorIndexAction{FactType: state.FactType, IndexName: state.IndexName, Create: true})
		case !want && state.Present:
			actions = append(actions, BankVectorIndexAction{FactType: state.FactType, IndexName: state.IndexName, Create: false})
		}
	}
	sort.Slice(actions, func(i, j int) bool {
		if actions[i].FactType != actions[j].FactType {
			return actions[i].FactType < actions[j].FactType
		}
		// Within one partition a drop must run before the rebuild: the invalid
		// leftover is what the CREATE ... IF NOT EXISTS would otherwise accept.
		return !actions[i].Create && actions[j].Create
	})
	return actions
}

// BankVectorIndexDDL returns the statements that realize one planned action in
// the given schema.
//
// The statements are CONCURRENTLY on purpose: a plain CREATE INDEX takes a
// write-blocking lock on the shared memory_units table, and this runs outside
// any transaction for exactly that reason. A DROP INDEX is a utility statement
// that locks only its own index and the table, which is what keeps it usable on
// an instance that already carries too many indexes.
func BankVectorIndexDDL(schema, vectorExtension string, bankID string, action BankVectorIndexAction) string {
	qualified := quoteIdent(schema)
	using := vectorIndexUsingClause(vectorExtension)
	if !action.Create {
		return fmt.Sprintf("DROP INDEX CONCURRENTLY IF EXISTS %s.%s", qualified, quoteIdent(action.IndexName))
	}
	// The access method and its operator class are one clause: the builder
	// returns both, so the template must not repeat the operator class.
	return fmt.Sprintf(
		"CREATE INDEX CONCURRENTLY IF NOT EXISTS %s ON %s.memory_units USING %s WHERE fact_type = '%s' AND bank_id = '%s'",
		quoteIdent(action.IndexName), qualified, using, action.FactType, strings.ReplaceAll(bankID, "'", "''"),
	)
}

// vectorIndexUsingClause maps the configured vector extension to its index
// access method, so a deployment on another vector backend still gets a usable
// index rather than pgvector's operator class.
func vectorIndexUsingClause(vectorExtension string) string {
	switch strings.ToLower(strings.TrimSpace(vectorExtension)) {
	case "vchord":
		return "vchordrq (embedding vector_cosine_ops)"
	case "diskann":
		return "diskann (embedding vector_cosine_ops)"
	case "pgvector", "":
		return "hnsw (embedding vector_cosine_ops)"
	default:
		return "hnsw (embedding vector_cosine_ops)"
	}
}
