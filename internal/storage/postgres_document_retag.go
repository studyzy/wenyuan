package storage

import (
	"context"
	"database/sql"
)

// RetagDocumentMemories carries a document's renamed tags onto its units, then
// rebuilds the observations built over them.
//
// A document update that renames a tag must reach three places at once: the
// units' own tags (which consolidation scopes by), the frozen observation_scopes
// strings on those units (which an explicit list form writes verbatim at retain
// time), and the observations already consolidated from them. Without the first
// two, the next pass rebuilds the observations under the stale tag and recall by
// the new one can no longer reach them; without the third, the old observations
// keep answering under tags nobody asked for.
//
// The rewritten scopes and the unit tags land in one transaction, and the
// stale-observation sweep runs on the same transaction, so a reader never sees
// a unit whose tags and scopes disagree. Returns how many observations were
// deleted, which is what tells the caller a re-consolidation pass is warranted.
func (s *PostgresStore) RetagDocumentMemories(
	ctx context.Context, bankID, docID string, oldTags, newTags []string,
) (int, error) {
	qualified := quoteIdent(s.schema)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()

	rows, err := tx.QueryContext(ctx,
		`SELECT id::text, COALESCE(tags, '{}'::text[])::text, fact_type, observation_scopes::text
		   FROM `+qualified+`.memory_units
		  WHERE document_id = $1 AND bank_id = $2`,
		docID, bankID)
	if err != nil {
		return 0, err
	}
	type unitRow struct {
		id       string
		factType string
		scopes   []byte
	}
	units := make([]unitRow, 0)
	for rows.Next() {
		var id, tags, factType string
		var scopes sql.NullString
		if err := rows.Scan(&id, &tags, &factType, &scopes); err != nil {
			_ = rows.Close()
			return 0, err
		}
		var rawScopes []byte
		if scopes.Valid && scopes.String != "" {
			rawScopes = []byte(scopes.String)
		}
		units = append(units, unitRow{id: id, factType: factType, scopes: rawScopes})
	}
	_ = rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	// Rewrite only the units whose explicit scope list names the renamed tag,
	// grouped by the rewritten JSON so identical scopes travel one statement.
	byScopes := map[string][]string{}
	for _, unit := range units {
		rewritten := renamedScopes(unit.scopes, oldTags, newTags)
		if rewritten == nil {
			continue
		}
		key := string(rewritten)
		byScopes[key] = append(byScopes[key], unit.id)
	}
	for scopesJSON, ids := range byScopes {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET observation_scopes = $1::jsonb
			  WHERE bank_id = $2 AND id = ANY($3::uuid[])`,
			scopesJSON, bankID, ids); err != nil {
			return 0, err
		}
	}

	unitTags := normalizeTags(newTags)
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+qualified+`.memory_units SET tags = $1, updated_at = now()
		  WHERE document_id = $2 AND bank_id = $3`,
		unitTags, docID, bankID); err != nil {
		return 0, err
	}

	srcIDs := make([]string, 0, len(units))
	for _, unit := range units {
		if unit.factType == string(TypeWorld) || unit.factType == string(TypeExperience) {
			srcIDs = append(srcIDs, unit.id)
		}
	}
	if len(srcIDs) == 0 {
		return 0, tx.Commit()
	}
	deleted, err := s.deleteStaleObservationsTx(ctx, tx, bankID, srcIDs)
	if err != nil {
		return 0, err
	}
	if deleted > 0 {
		// The document's own facts changed scope, so they re-consolidate under
		// the new one — a plain re-queue marker, no column values move.
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET consolidated_at = NULL
			  WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
			bankID, srcIDs); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}
