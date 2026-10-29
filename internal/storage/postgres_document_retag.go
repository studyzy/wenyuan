package storage

import (
	"context"
	"strings"
)

// RetagDocumentMemories carries a document's new tags and observation scopes
// onto its units, then rebuilds the observations built over the units whose
// resolved scopes that moves.
//
// A document update that renames a tag must reach three places at once: the
// units' own tags (which consolidation scopes by), the scope strings written on
// those units (which an explicit list form freezes at retain time), and the
// observations already consolidated from them. Without the first two, the next
// pass rebuilds the observations under the stale tag and recall by the new one
// can no longer reach them; without the third, the old observations keep
// answering under tags nobody asked for.
//
// Only the units whose RESOLVED scopes move lose their observations and are
// requeued: under `shared` or an explicit scope list a tag change leaves every
// observation where it was (#5593). The rewritten scopes and the unit tags land
// in one transaction, and the stale-observation sweep runs on the same
// transaction, so a reader never sees a unit whose tags and scopes disagree.
func (s *PostgresStore) RetagDocumentMemories(
	ctx context.Context, bankID, docID string, change DocumentScopeChange,
) (RetagOutcome, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return RetagOutcome{}, schemaErr
	}
	qualified := quoteIdent(schema)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return RetagOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()

	units, err := readDocumentUnitScopes(ctx, tx, qualified, bankID, docID)
	if err != nil {
		return RetagOutcome{}, err
	}
	plans := planDocumentRetag(units, change)
	// The scopes a unit ends with are grouped by their JSON so identical values
	// travel one statement.
	byScopes := map[string][]string{}
	for _, plan := range plans {
		if plan.newScopes == nil {
			continue
		}
		byScopes[string(plan.newScopes)] = append(byScopes[string(plan.newScopes)], plan.unit.ID)
	}
	for scopesJSON, ids := range byScopes {
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units SET observation_scopes = $1::jsonb
			  WHERE bank_id = $2 AND id = ANY($3::uuid[])`,
			scopesJSON, bankID, ids); err != nil {
			return RetagOutcome{}, err
		}
	}
	if change.NewTags != nil {
		if err := writeRetaggedUnitTags(ctx, tx, qualified, bankID, docID, change.NewTags, plans); err != nil {
			return RetagOutcome{}, err
		}
	} else {
		// A scope change alone writes no tags, so nothing above stamped the units
		// that moved. `updated_at` is the "this fact was edited" signal an
		// in-flight consolidation discards a stale result on (#4831), so the moved
		// ones are stamped — and only those: stamping a unit whose scopes did not
		// move would discard a still-valid result.
		if moved := movedSourceUnits(plans); len(moved) > 0 {
			if _, err := tx.ExecContext(ctx,
				`UPDATE `+qualified+`.memory_units SET updated_at = now()
				  WHERE bank_id = $1 AND id = ANY($2::uuid[])`,
				bankID, moved); err != nil {
				return RetagOutcome{}, err
			}
		}
	}

	srcIDs := movedSourceUnits(plans)
	if len(srcIDs) == 0 {
		return RetagOutcome{}, tx.Commit()
	}
	// Every unit that moved is requeued, whether or not it had an observation: it
	// was consolidated under scopes it no longer writes to. The sweep clears that
	// marker on the same rows, as bookkeeping only, so `updated_at` stays put —
	// the write above is what stamped them.
	removed, err := s.deleteObservationsOnTx(ctx, tx, bankID, srcIDs)
	if err != nil {
		return RetagOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return RetagOutcome{}, err
	}
	return RetagOutcome{
		InvalidatedObservations: len(removed), RequeuedUnits: len(srcIDs), Removed: removed,
	}, nil
}

// readDocumentUnitScopes reads the units of one document with the tag and scope
// values a retag plans against, before anything is written.
func readDocumentUnitScopes(ctx context.Context, tx Tx, qualified, bankID, docID string) ([]DocumentUnitScope, error) {
	rows, err := tx.QueryContext(ctx,
		`SELECT id::text, COALESCE(tags, '{}'::text[])::text, fact_type,
		        COALESCE(observation_scopes::text, '')
		   FROM `+qualified+`.memory_units
		  WHERE document_id = $1 AND bank_id = $2`,
		docID, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	units := make([]DocumentUnitScope, 0)
	for rows.Next() {
		var unit DocumentUnitScope
		var tags, factType, scopes string
		if err := rows.Scan(&unit.ID, &tags, &factType, &scopes); err != nil {
			return nil, err
		}
		unit.Tags = decodePostgresTextArray([]byte(tags))
		unit.FactType = MemoryUnitType(factType)
		unit.Scopes = rawScopesJSON(scopes)
		units = append(units, unit)
	}
	return units, rows.Err()
}

// rawScopesJSON returns an observation_scopes value as the JSON text that was
// stored, or nil when the unit carries none.
func rawScopesJSON(raw string) []byte {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	return []byte(trimmed)
}

// writeRetaggedUnitTags writes the document's new tags onto every unit, then
// restores each unit's own label projection over that blanket write.
//
// The blanket write is one statement so a unit created concurrently still lands
// on the document tags exactly as before. The follow-up then gives each unit its
// own label projection back over that write — a bank with no `tag: true` group
// restores nothing and every unit ends on the document tags.
func writeRetaggedUnitTags(
	ctx context.Context, tx Tx, qualified, bankID, docID string, newTags []string, plans []documentRetagPlan,
) error {
	if _, err := tx.ExecContext(ctx,
		`UPDATE `+qualified+`.memory_units SET tags = $1, updated_at = now()
		  WHERE document_id = $2 AND bank_id = $3`,
		normalizeTags(newTags), docID, bankID); err != nil {
		return err
	}
	// Each unit's row is written back in one statement with the FINAL tag array
	// the plan computed — so a unit whose label tag is also a document tag does
	// not come back carrying it twice — and only a unit whose list actually
	// changes takes the `updated_at` stamp that marks it edited. Stamping one
	// whose tags end as they started would discard a still-valid consolidation
	// result for a write that changed nothing visible.
	type tagGroup struct {
		tags    []string
		ids     []string
		stamped bool
	}
	grouped := map[string]*tagGroup{}
	for _, plan := range plans {
		stamped := !sameTagSet(plan.unit.Tags, plan.finalTags)
		key := strings.Join(plan.finalTags, "\x00")
		if !stamped {
			key = "=" + key
		}
		group, ok := grouped[key]
		if !ok {
			group = &tagGroup{tags: plan.finalTags, stamped: stamped}
			grouped[key] = group
		}
		group.ids = append(group.ids, plan.unit.ID)
	}
	for _, group := range grouped {
		setClause := "SET tags = $1"
		if group.stamped {
			setClause = "SET tags = $1, updated_at = now()"
		}
		if _, err := tx.ExecContext(ctx,
			`UPDATE `+qualified+`.memory_units `+setClause+`
			  WHERE document_id = $2 AND bank_id = $3 AND id = ANY($4::uuid[])`,
			group.tags, docID, bankID, group.ids); err != nil {
			return err
		}
	}
	return nil
}
