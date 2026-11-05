//go:build pgx

package storage

import "context"

// Writing the links a round carries between its team facts.

// applyLinks recreates the member links between the round's team facts.
//
// Both endpoints are addressed by merge key, so a link whose fact another round
// wrote resolves without the round having to carry that fact's id. A link whose
// endpoint is not in the bank — the batch has not reached it yet, or the fact
// was removed — is skipped: the later round that reads that endpoint picks the
// link up from its own side.
//
// entity_id is not carried: an entity is an identity in one bank, and the team
// bank has no row for the member's. Entity-mediated edges are derived from
// unit_entities, which this round does not carry.
func (s *PostgresMergeStore) applyLinks(
	ctx context.Context, tx Tx, qualified, bankID string,
	links []MergeLink, ids map[string]string,
) error {
	missing := make(map[string]bool, len(links))
	for _, link := range links {
		if _, ok := ids[link.FromMergeKey]; !ok {
			missing[link.FromMergeKey] = true
		}
		if _, ok := ids[link.ToMergeKey]; !ok {
			missing[link.ToMergeKey] = true
		}
	}
	if len(missing) > 0 {
		rows, err := tx.QueryContext(ctx, `
			SELECT metadata->>'merge_key', id::text
			FROM `+qualified+`.memory_units
			WHERE bank_id = $1 AND metadata->>'merge_key' = ANY($2::text[])`,
			bankID, keysOf(missing))
		if err != nil {
			return err
		}
		for rows.Next() {
			var key, id string
			if err := rows.Scan(&key, &id); err != nil {
				_ = rows.Close()
				return err
			}
			ids[key] = id
		}
		_ = rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
	}
	for _, link := range links {
		from, ok := ids[link.FromMergeKey]
		if !ok {
			continue
		}
		to, ok := ids[link.ToMergeKey]
		if !ok {
			continue
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO `+qualified+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type, weight)
			VALUES ($1, $2::uuid, $3::uuid, $4, $5)
			ON CONFLICT DO NOTHING`,
			bankID, from, to, string(link.LinkType), link.Weight); err != nil {
			return err
		}
	}
	return nil
}
