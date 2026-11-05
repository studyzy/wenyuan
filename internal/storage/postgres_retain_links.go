package storage

import (
	"context"
	"encoding/json"
	"sort"
)

// createSemanticLinks links each new unit to existing units whose embedding is
// within the similarity floor, using the pgvector distance operator.
func (s *PostgresStore) createSemanticLinks(ctx context.Context, bankID string, unitIDs []string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if len(unitIDs) == 0 {
		return nil
	}
	qualified := quoteIdent(schema)
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	if len(unitIDs) == 0 {
		return nil
	}
	qualified := quoteIdent(schema)
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

// nilEntityID is the folded value a null entity contributes to a link's unique
// key, so a null and an explicit nil sort to the same place.
const nilEntityID = "00000000-0000-0000-0000-000000000000"

// linkOrderKey is a link's direction-normalised unique key. Normalising the
// endpoints makes (A, B) and (B, A) sort adjacent, and including the type and
// entity keeps two edges sharing an endpoint pair from being locked in
// opposite orders by concurrent writers.
type linkOrderKey struct {
	low      string
	high     string
	linkType string
	entity   string
}

func orderKeyFor(link MemoryLink) linkOrderKey {
	a, b := link.FromUnitID, link.ToUnitID
	low, high := a, b
	if a > b {
		low, high = b, a
	}
	entity := link.EntityID
	if entity == "" {
		entity = nilEntityID
	}
	return linkOrderKey{low, high, string(link.LinkType), entity}
}

// sortLinksByLockOrder returns the links in one canonical order so every writer
// takes the index locks in the same sequence and concurrent inserts cannot
// deadlock against each other.
func sortLinksByLockOrder(links []MemoryLink) []MemoryLink {
	ordered := append([]MemoryLink(nil), links...)
	sort.SliceStable(ordered, func(i, j int) bool {
		a, b := orderKeyFor(ordered[i]), orderKeyFor(ordered[j])
		if a.low != b.low {
			return a.low < b.low
		}
		if a.high != b.high {
			return a.high < b.high
		}
		if a.linkType != b.linkType {
			return a.linkType < b.linkType
		}
		return a.entity < b.entity
	})
	return ordered
}

// causalDescriptor is one causal edge parked on the curation archive while an
// endpoint is invalidated. Invalidation moves a fact out of the live table and
// its incident links go with it; the descriptor is what lets a revert bring the
// edge back.
type causalDescriptor struct {
	FromUnitID string  `json:"from_unit_id"`
	ToUnitID   string  `json:"to_unit_id"`
	LinkType   string  `json:"link_type"`
	Weight     float64 `json:"weight"`
}

// defaultCausalLinkWeight is the strength a descriptor carries when its stored
// weight is missing or zero.
const defaultCausalLinkWeight = 1.0

// marshalCausalDescriptors renders an archive column value. An empty set is an
// empty JSON array rather than a null, so the column's shape stays stable.
func marshalCausalDescriptors(descriptors []causalDescriptor) []byte {
	if len(descriptors) == 0 {
		return []byte("[]")
	}
	data, err := json.Marshal(descriptors)
	if err != nil {
		return []byte("[]")
	}
	return data
}

// parseCausalDescriptors reads the descriptors a stored archive row holds. The
// column is schemaless JSON, so entries that are not objects, lack an endpoint,
// or name a link type a causal edge cannot take are skipped instead of aborting
// the whole revert. A weight that is absent or zero takes the default.
func parseCausalDescriptors(raw []byte) []causalDescriptor {
	var items []json.RawMessage
	if json.Unmarshal(raw, &items) != nil {
		return nil
	}
	parsed := make([]causalDescriptor, 0, len(items))
	for _, item := range items {
		var fields map[string]json.RawMessage
		if json.Unmarshal(item, &fields) != nil || fields == nil {
			continue
		}
		var descriptor causalDescriptor
		if json.Unmarshal(item, &descriptor) != nil {
			continue
		}
		if descriptor.FromUnitID == "" || descriptor.ToUnitID == "" {
			continue
		}
		if !IsCausalLinkType(MemoryLinkType(descriptor.LinkType)) {
			continue
		}
		if descriptor.Weight == 0 {
			descriptor.Weight = defaultCausalLinkWeight
		}
		parsed = append(parsed, descriptor)
	}
	return parsed
}

// snapshotCausalLinks collects the causal edges that must survive a unit's move
// to the archive. It merges the unit's currently materialized edges with
// descriptors already parked on archived peers naming this unit, so an edge
// whose other endpoint was archived first still has a copy. Keeping a copy on
// every archived endpoint makes the revert order irrelevant.
func (s *PostgresStore) snapshotCausalLinks(ctx context.Context, bankID, unitID string) ([]causalDescriptor, error) {
	schema, err := s.schemaFor(ctx)
	if err != nil {
		return nil, err
	}
	q := quoteIdent(schema)
	rows, err := s.db.QueryContext(ctx,
		`SELECT from_unit_id::text, to_unit_id::text, link_type, COALESCE(weight, 1.0)::float8
		   FROM `+q+`.memory_links
		  WHERE bank_id = $1 AND (from_unit_id = $2::uuid OR to_unit_id = $2::uuid)
		    AND link_type = ANY($3::text[])
		 UNION
		 SELECT d.from_unit_id::text, d.to_unit_id::text, d.link_type, COALESCE(d.weight, 1.0)::float8
		   FROM `+q+`.invalidated_memory_units a
		   CROSS JOIN LATERAL jsonb_to_recordset(a.causal_links)
		        AS d(from_unit_id uuid, to_unit_id uuid, link_type text, weight float8)
		  WHERE a.bank_id = $1 AND a.causal_links <> '[]'::jsonb
		    AND (d.from_unit_id = $2::uuid OR d.to_unit_id = $2::uuid)
		    AND d.from_unit_id IS NOT NULL AND d.to_unit_id IS NOT NULL
		    AND d.link_type = ANY($3::text[])`,
		bankID, unitID, causalLinkTypeNames())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	descriptors := make([]causalDescriptor, 0)
	for rows.Next() {
		var descriptor causalDescriptor
		if err := rows.Scan(&descriptor.FromUnitID, &descriptor.ToUnitID, &descriptor.LinkType, &descriptor.Weight); err != nil {
			return nil, err
		}
		descriptors = append(descriptors, descriptor)
	}
	return descriptors, rows.Err()
}

// rematerializeCausalLinks recreates archived causal edges whose endpoints are
// both live again. A peer still archived or permanently deleted keeps its own
// copy of the descriptor and materializes the edge when it reverts, so a
// missing endpoint is simply left out here. ON CONFLICT DO NOTHING keeps
// repeated invalidate/revert cycles from duplicating an edge.
func (s *PostgresStore) rematerializeCausalLinks(ctx context.Context, bankID string, descriptors []causalDescriptor) error {
	if len(descriptors) == 0 {
		return nil
	}
	schema, err := s.schemaFor(ctx)
	if err != nil {
		return err
	}
	q := quoteIdent(schema)
	links := make([]MemoryLink, 0, len(descriptors))
	for _, descriptor := range descriptors {
		links = append(links, MemoryLink{
			BankID:     bankID,
			FromUnitID: descriptor.FromUnitID,
			ToUnitID:   descriptor.ToUnitID,
			LinkType:   MemoryLinkType(descriptor.LinkType),
			Weight:     descriptor.Weight,
		})
	}
	for _, link := range sortLinksByLockOrder(links) {
		if _, err := s.db.ExecContext(ctx,
			`INSERT INTO `+q+`.memory_links (bank_id, from_unit_id, to_unit_id, link_type, weight)
			 SELECT $1, $2::uuid, $3::uuid, $4, $5
			 WHERE EXISTS (SELECT 1 FROM `+q+`.memory_units WHERE bank_id = $1 AND id = $2::uuid)
			   AND EXISTS (SELECT 1 FROM `+q+`.memory_units WHERE bank_id = $1 AND id = $3::uuid)
			 ON CONFLICT DO NOTHING`,
			bankID, link.FromUnitID, link.ToUnitID, string(link.LinkType), link.Weight); err != nil {
			return err
		}
	}
	return nil
}
