//go:build pgx

package storage

import (
	"context"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// Reading one member's contribution to a round, and detecting that a source the
// round once consumed has gone.
//
// These live apart from the store's round claim because they are the part that
// decides scope: which fact kinds count as contribution, how far back a read
// reaches, and whether a deletion forces a rebuild.

// loadMemberInput reads one member's contribution for this round.
func (s *PostgresMergeStore) loadMemberInput(
	ctx context.Context, tx Tx, member MergeProjectMember, limit int, includeExperience bool,
) (MergeMemberInput, error) {
	out := MergeMemberInput{Member: member}
	// A project whose id is "myself" points at the member's aggregate bank, and
	// that bank holds conclusions the personal rollup drew from every bank its
	// owner has — not facts the member contributed to this project. Folding them
	// in would let one person's cross-project summary pass as their input to a
	// team, which is exactly what the aggregate must never do. The exclusion is a
	// decision here rather than a predicate in SQL so that the merge queries keep
	// saying only what they are about.
	if tenant.IsPersonalBankID(memberBankID(member)) {
		return out, nil
	}
	memberCtx, err := WithSchema(ctx, member.Schema)
	if err != nil {
		return out, err
	}
	qualified := quoteIdent(member.Schema)
	// A member with no bank row yet has contributed nothing; the team's content
	// is kept rather than rebuilt (the onboarding path).
	if err := tx.QueryRowContext(memberCtx,
		`SELECT EXISTS (SELECT 1 FROM `+qualified+`.banks WHERE bank_id = $1)`,
		memberBankID(member)).Scan(&out.BankExists); err != nil {
		return out, err
	}
	if !out.BankExists {
		return out, nil
	}
	// A rebuild reads the member's whole bank, not the delta. That is the point of
	// one: the round recomputes the team's content from what the members hold now,
	// so a member who left — or whose facts were deleted — stops contributing.
	// Reading from the cursor here would make a rebuild see only new facts and
	// leave every existing team fact standing, which is exactly the stale content a
	// withdrawal exists to remove.
	// The input is every fact kind a member contributes: what they know about
	// the project, what they did about it, and what consolidation concluded from
	// both. Reading only `world` shared a fraction of a member's knowledge.
	inputTypes := mergedFactTypesFor(includeExperience)
	var fresh []MergeFact
	var statement string
	var args []any
	if member.State.RebuildRequested {
		// A rebuild reads the member's whole bank — that is what makes it a
		// rebuild rather than an incremental round. It is still bounded by the
		// batch, because the round feeds all of it to one model call: a member
		// holding tens of thousands of facts would otherwise build a prompt the
		// model cannot accept, and the rebuild would fail every round forever.
		// The cursor the round advances to is the last fact read, so the next
		// round continues where this one stopped, and the rebuild completes over
		// several rounds instead of in one.
		statement = `
			SELECT id::text, text, fact_type, updated_at
			FROM ` + qualified + `.memory_units
			WHERE bank_id = $1 AND fact_type = ANY($2)
			ORDER BY updated_at, id
			LIMIT $3`
		args = []any{memberBankID(member), inputTypes, limit}
	} else {
		cursor := CursorOf(member.State)
		statement = `
			SELECT id::text, text, fact_type, updated_at
			FROM ` + qualified + `.memory_units
			WHERE bank_id = $1 AND fact_type = ANY($2)
			  AND (updated_at, id) > ($3::timestamptz, $4::uuid)
			ORDER BY updated_at, id
			LIMIT $5`
		args = []any{memberBankID(member), inputTypes, cursor.UpdatedAt, zeroUUID, limit}
		if !cursor.Before() {
			args = []any{memberBankID(member), inputTypes, cursor.UpdatedAt, cursor.UnitID, limit}
		}
	}
	rows, err := tx.QueryContext(memberCtx, statement, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var fact MergeFact
		var updatedAt time.Time
		if err := rows.Scan(&fact.UnitID, &fact.Text, &fact.FactType, &updatedAt); err != nil {
			_ = rows.Close()
			return out, err
		}
		fact.TenantID = member.TenantID
		fact.UpdatedAt = updatedAt
		fresh = append(fresh, fact)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Fresh = fresh
	// A rebuild is requested when a source that still supports team content has
	// gone from this member's bank.
	if member.HasLedger {
		deleted, err := s.sourceDeleted(memberCtx, tx, member, qualified)
		if err != nil {
			return out, err
		}
		out.Deleted = deleted
	}
	return out, nil
}

// sourceDeleted reports whether any live ledger source of this member's bank is
// gone.
//
// Only rows that still support team content are checked. A source the model
// folded away supports nothing, so its deletion cannot invalidate the team's
// content and must not trigger a rebuild — which is the whole reason the ledger
// records the supported key rather than just "consumed".
func (s *PostgresMergeStore) sourceDeleted(ctx context.Context, tx Tx, member MergeProjectMember, qualified string) (bool, error) {
	var deleted bool
	err := tx.QueryRowContext(ctx, `
		SELECT EXISTS (
		    SELECT 1
		    FROM `+adminTable("merge_source_unit")+` l
		    LEFT JOIN `+qualified+`.memory_units m ON m.id = l.source_unit_id
		    WHERE l.team_tenant_id = $1 AND l.project_id = $2 AND l.tenant_id = $3
		      AND l.merge_key <> $4
		      AND m.id IS NULL
		)`, member.State.TeamID, member.State.ProjectID, member.TenantID,
		tenant.DroppedMergeKey).Scan(&deleted)
	return deleted, err
}
