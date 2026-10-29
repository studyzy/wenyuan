package storage

import "context"

// dropNonCausalLinks deletes every link incident to one unit except the causal
// edges. It is PostgreSQL's half of the reference's apply_edit link sweep
// (pg/writes.py apply_edit -> delete_unit_links with keep_link_types): the
// DERIVED temporal and semantic edges go, because graph maintenance recomputes
// the ones that still hold, while the causal edges stay — they are retain-time
// extraction output that nothing recreates (#2864). An edit therefore cannot
// leave a stale temporal or semantic edge behind.
//
// The victims of that sweep are queued first, before the rows go: the survivors
// whose outgoing edges pointed here, plus the edited unit itself, which stays
// live with its own derived adjacency stripped (the reference's
// enqueue_relink_victims with include_affected_units set).
func (s *PostgresStore) dropNonCausalLinks(ctx context.Context, bankID, unitID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	qualified := quoteIdent(schema)
	if err := s.enqueueRelinkVictims(ctx, s.db, bankID, []string{unitID}, true); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.memory_links
		 WHERE bank_id = $1 AND (from_unit_id = $2::uuid OR to_unit_id = $2::uuid)
		   AND NOT (link_type = ANY($3::text[]))`,
		bankID, unitID, causalLinkTypeNames())
	return err
}
