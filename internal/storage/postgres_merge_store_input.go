//go:build pgx

package storage

import (
	"context"
	"database/sql"

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
	// A rebuild is the cursor rewound to the start (RequestRebuild does that),
	// after which the rounds replay forward a batch at a time like any other
	// round; the one thing an acquisition round must not do is read only past a
	// cursor, or a deletion would never be noticed.
	//
	// The input is what a member knows and what they did: `world` facts and
	// `experience` facts, both of which are append-only in a member's bank and
	// keep their text until they are deleted. An `observation` is not input —
	// it is a derived row consolidation rewrites in place, so its text drifts
	// while its identity stays, which is exactly what a copied row cannot
	// follow. The team bank derives its own observations from what is copied.
	inputTypes := mergedInputTypes(includeExperience)
	fresh := make([]MergeFact, 0, limit)
	// A rebuild's replay ignores the cursor: it is a full replay of the member's
	// bank, so a fact behind the old cursor is read again — which is what makes
	// the team's content match the members' after a withdrawal.
	fromStart := member.RebuildRequested
	cursor := CursorOf(member.State)
	// The cursor's lower bound. A replay has none: comparing against the zero
	// timestamp with a zero id can still skip a row, because the row comparison
	// is not the same thing as "after the beginning", and a bank's oldest fact is
	// then never replayed. An incremental round resumes strictly after the last
	// fact it consumed.
	columns := `
		SELECT id::text, text, fact_type, COALESCE(tags, '{}'::text[])::text, updated_at,
		       COALESCE(context, ''), event_date, occurred_start, occurred_end, mentioned_at,
		       COALESCE(observation_scopes::text, ''), COALESCE(text_signals, ''),
		       created_at, updated_at, edited_at, embedding::text, COALESCE(source_memory_ids, '{}'::uuid[])::text
		FROM ` + qualified + `.memory_units
		WHERE bank_id = $1 AND fact_type = ANY($2)`
	var statement string
	var args []any
	if fromStart || cursor.Before() {
		statement = columns + `
		ORDER BY updated_at, id
		LIMIT $3`
		args = []any{memberBankID(member), inputTypes, limit}
	} else {
		statement = columns + `
		  AND (updated_at, id) > ($3::timestamptz, $4::uuid)
		ORDER BY updated_at, id
		LIMIT $5`
		args = []any{memberBankID(member), inputTypes, cursor.UpdatedAt, cursor.UnitID, limit}
	}
	rows, err := tx.QueryContext(memberCtx, statement, args...)
	if err != nil {
		return out, err
	}
	for rows.Next() {
		var fact MergeFact
		var tags, scopes, signals, sources string
		var embedding sql.NullString
		var eventDate, occurredStart, occurredEnd, mentionedAt, createdAt, updatedAt, editedAt sql.NullTime
		if err := rows.Scan(&fact.UnitID, &fact.Text, &fact.FactType, &tags, &fact.UpdatedAt,
			&fact.Context, &eventDate, &occurredStart, &occurredEnd, &mentionedAt,
			&scopes, &signals, &createdAt, &updatedAt, &editedAt, &embedding, &sources); err != nil {
			_ = rows.Close()
			return out, err
		}
		fact.Tags = decodePostgresTextArray([]byte(tags))
		fact.TenantID = member.TenantID
		fact.EventDate = nullTimePtr(eventDate)
		fact.OccurredStart = nullTimePtr(occurredStart)
		fact.OccurredEnd = nullTimePtr(occurredEnd)
		fact.MentionedAt = nullTimePtr(mentionedAt)
		if scopes != "" {
			fact.ObservationScopes = []byte(scopes)
		}
		if signals != "" {
			fact.TextSignals = &signals
		}
		fact.CreatedAt = nullTimePtr(createdAt)
		fact.EditedAt = nullTimePtr(editedAt)
		fact.SourceMemoryIDs = decodePostgresTextArray([]byte(sources))
		decoded, err := DecodeEmbedding(embedding.String)
		if err != nil {
			_ = rows.Close()
			return out, err
		}
		fact.Embedding = decoded
		fresh = append(fresh, fact)
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return out, err
	}
	out.Fresh = fresh
	// A read that filled its batch may have stopped short of the bank's end. The
	// round's cursor record advances only to what it consumed, so the rest waits
	// for the next round — but only a loop that keeps going gets it.
	if len(fresh) >= limit {
		out.More, err = s.memberHasMoreTx(memberCtx, tx, member, qualified, fresh, inputTypes)
		if err != nil {
			return out, err
		}
	}
	out.Links, err = s.loadMemberLinks(ctx, tx, member, qualified, fresh, inputTypes)
	if err != nil {
		return out, err
	}
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

// memberHasMoreTx reports whether a member's bank holds facts after the last one
// a full batch consumed.
func (s *PostgresMergeStore) memberHasMoreTx(
	memberCtx context.Context, tx Tx, member MergeProjectMember, qualified string,
	fresh []MergeFact, inputTypes []string,
) (bool, error) {
	if len(fresh) == 0 {
		return false, nil
	}
	last := fresh[len(fresh)-1]
	var more bool
	err := tx.QueryRowContext(memberCtx, `
		SELECT EXISTS (
		    SELECT 1 FROM `+qualified+`.memory_units
		    WHERE bank_id = $1 AND fact_type = ANY($2)
		      AND (updated_at, id) > ($3::timestamptz, $4::uuid)
		)`, memberBankID(member), inputTypes, last.UpdatedAt, last.UnitID).Scan(&more)
	return more, err
}

// loadMemberLinks reads the member's links that touch this round's facts.
//
// A link is read when either endpoint is in the batch, so a second endpoint
// that the batch has not reached yet is carried when it arrives: that later
// round reads the same link from its own side, and both team facts now exist.
// The link types a round carries are the fact kinds it carries: a link with an
// endpoint that is not an input type is dropped whole, because that endpoint has
// no row in the team bank to attach to. That is what keeps a link into a
// member's observation from arriving — observations are not copied, and a link
// whose other end does not exist is not content, it is a dangling edge.
//
// The endpoints are returned as merge keys rather than unit ids because a team
// fact is a new row; the write looks both keys up in the team bank.
func (s *PostgresMergeStore) loadMemberLinks(
	ctx context.Context, tx Tx, member MergeProjectMember, qualified string,
	fresh []MergeFact, inputTypes []string,
) ([]MergeLink, error) {
	if len(fresh) == 0 {
		return nil, nil
	}
	unitIDs := make([]string, len(fresh))
	for i := range fresh {
		unitIDs[i] = fresh[i].UnitID
	}
	rows, err := tx.QueryContext(ctx, `
		SELECT source.text, target.text, link.link_type, link.weight
		FROM `+qualified+`.memory_links link
		JOIN `+qualified+`.memory_units source ON source.id = link.from_unit_id
		JOIN `+qualified+`.memory_units target ON target.id = link.to_unit_id
		WHERE link.bank_id = $1
		  AND (link.from_unit_id = ANY($2::uuid[]) OR link.to_unit_id = ANY($2::uuid[]))
		  AND source.fact_type = ANY($3) AND target.fact_type = ANY($3)
		ORDER BY source.id, target.id, link.link_type`,
		memberBankID(member), unitIDs, inputTypes)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	links := make([]MergeLink, 0)
	for rows.Next() {
		var fromText, toText, linkType string
		var weight float64
		if err := rows.Scan(&fromText, &toText, &linkType, &weight); err != nil {
			return nil, err
		}
		links = append(links, MergeLink{
			FromMergeKey: tenant.MergeKey(fromText),
			ToMergeKey:   tenant.MergeKey(toText),
			LinkType:     MemoryLinkType(linkType),
			Weight:       weight,
		})
	}
	return links, rows.Err()
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
