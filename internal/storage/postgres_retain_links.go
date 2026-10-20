package storage

import (
	"context"
)

// createSemanticLinks links each new unit to existing units whose embedding is
// within the similarity floor, using the pgvector distance operator.
func (s *PostgresStore) createSemanticLinks(ctx context.Context, bankID string, unitIDs []string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema)
	// The reference's ANN phase probes each new unit's nearest bank units and
	// takes at most `top_k` of them above the similarity floor, with the
	// similarity clamped to [0,1] because a cosine distance can exceed it on
	// denormalised vectors (`memories/pg/links.py:594-660`). The probe is split
	// by fact type to pick the per-bank HNSW index, NOT to restrict the
	// candidate set: an observation and the world facts behind it are linked
	// when they are similar, which is why the per-fact-type fan-out here only
	// affects which index a real deployment would use.
	//
	// A unit is its own nearest neighbour, so the reference records a
	// self-link (weight 1) for every unit it probes; `ON CONFLICT DO NOTHING`
	// keeps that a single row per unit rather than one per probe. Those rows are
	// visible in the statistics and in the graph, so leaving them out would
	// undercount both.
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO `+qualified+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type, weight)
		 SELECT neighbours.bank_id, neighbours.from_id, neighbours.to_id, 'semantic',
		        LEAST(1.0, GREATEST(0.0, neighbours.similarity))
		 FROM (
		     SELECT u1.bank_id, u1.id AS from_id, u2.id AS to_id,
		            1 - (u1.embedding <=> u2.embedding) AS similarity,
		            ROW_NUMBER() OVER (
		                PARTITION BY u1.id
		                ORDER BY (u1.embedding <=> u2.embedding), u2.id
		            ) AS rn
		     FROM `+qualified+`.memory_units u1
		     JOIN `+qualified+`.memory_units u2
		       ON u2.bank_id = u1.bank_id
		      AND u2.embedding IS NOT NULL
		     WHERE u1.bank_id = $1 AND u1.id = ANY($2::uuid[]) AND u1.embedding IS NOT NULL
		 ) neighbours
		 WHERE neighbours.rn <= $4
		   AND neighbours.similarity >= $3
		 ON CONFLICT DO NOTHING`,
		bankID, unitIDs, s.tuning.SemanticLinkMinSimilarity, maxSemanticLinksPerUnit)
	return err
}

// createTemporalLinks links the dated units a retain just wrote to each other
// and to their nearest dated neighbours, the way the reference's retain does
// before its semantic phase (`retain/orchestrator.py:820` →
// `memories/pg/links.py:457-566`).
//
// Two shapes, both bounded by the same per-unit cap and 24h window:
//
//   - existing units: each new unit takes its nearest same-fact-type neighbours
//     inside the window, nearest first;
//   - the new batch itself: within a fact-type group ordered by date, every unit
//     links BIDIRECTIONALLY to its next `max_per_unit` successors in the window
//     (`_within_batch_temporal_links`), so a batch of n same-day facts produces
//     n*(n-1) edges rather than only the forward ones.
//
// The weight falls off with the gap and floors at the reference's 0.3. Only
// experience and world units carry an event date, so only they are linked, and
// a temporal pass never writes a self-link.
func (s *PostgresStore) createTemporalLinks(ctx context.Context, bankID string, unitIDs []string) error {
	if len(unitIDs) == 0 {
		return nil
	}
	qualified := quoteIdent(s.schema)
	_, err := s.db.ExecContext(ctx,
		`WITH new_units AS (
		     SELECT id, event_date, fact_type
		     FROM `+qualified+`.memory_units
		     WHERE bank_id = $1 AND id = ANY($2::uuid[])
		       AND event_date IS NOT NULL
		       AND fact_type IN ('experience', 'world')
		 ),

		 -- New units against the units already in the bank.
		 existing AS (
		     SELECT near_rows.from_id, near_rows.to_id, near_rows.diff_hours
		     FROM (
		         SELECT u1.id AS from_id, near.id AS to_id,
		                ABS(EXTRACT(EPOCH FROM near.event_date - u1.event_date)) / 3600.0 AS diff_hours,
		                ROW_NUMBER() OVER (
		                    PARTITION BY u1.id
		                    ORDER BY ABS(EXTRACT(EPOCH FROM near.event_date - u1.event_date)), near.id
		                ) AS rn
		         FROM new_units u1
		         JOIN `+qualified+`.memory_units near
		           ON near.bank_id = $1 AND near.id <> u1.id
		          AND near.fact_type = u1.fact_type AND near.event_date IS NOT NULL
		     ) near_rows
		     WHERE near_rows.rn <= $4
		 ),

		 -- The new batch against itself: each unit to its next K successors in
		 -- date order, both directions, which is what the reference's
		 -- within-batch pass emits.
		 within AS (
		     SELECT a.id AS from_id, b.id AS to_id,
		            ABS(EXTRACT(EPOCH FROM b.event_date - a.event_date)) / 3600.0 AS diff_hours
		     FROM new_units a
		     JOIN new_units b
		       ON b.fact_type = a.fact_type AND b.id <> a.id
		      AND b.event_date >= a.event_date
		     WHERE (SELECT count(*) FROM new_units c
		            WHERE c.fact_type = a.fact_type
		              AND (c.event_date, c.id) > (a.event_date, a.id)
		              AND (c.event_date, c.id) <= (b.event_date, b.id)) <= $4
		     UNION ALL
		     SELECT b.id AS from_id, a.id AS to_id,
		            ABS(EXTRACT(EPOCH FROM b.event_date - a.event_date)) / 3600.0 AS diff_hours
		     FROM new_units a
		     JOIN new_units b
		       ON b.fact_type = a.fact_type AND b.id <> a.id
		      AND b.event_date >= a.event_date
		     WHERE (SELECT count(*) FROM new_units c
		            WHERE c.fact_type = a.fact_type
		              AND (c.event_date, c.id) > (a.event_date, a.id)
		              AND (c.event_date, c.id) <= (b.event_date, b.id)) <= $4
		 )
		 INSERT INTO `+qualified+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type, weight)
		 SELECT $1, candidates.from_id, candidates.to_id, 'temporal',
		        GREATEST(0.3, 1.0 - candidates.diff_hours / $3)
		 FROM (SELECT * FROM existing UNION ALL SELECT * FROM within) candidates
		 WHERE candidates.diff_hours <= $3
		 ON CONFLICT DO NOTHING`,
		bankID, unitIDs, temporalLinkWindowHours, maxTemporalLinksPerUnit)
	return err
}
