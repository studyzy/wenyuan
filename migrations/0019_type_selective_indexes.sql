-- 0019_type_selective_indexes
-- Adds the partial indexes the reference carries per fact type.
--
-- The reference keeps an index for each fact type whose access pattern differs
-- from the general one: opinions are ranked by confidence and by recency, and
-- observations by recency. A partial index over one type is a fraction of the
-- size of the full index and is chosen by the planner only when the query names
-- that type, which is exactly the read path this schema serves.
--
-- The destination column is named `type` where the reference names it
-- `fact_type`, so the predicates below use `type`.

-- Opinions, ranked by confidence.
CREATE INDEX IF NOT EXISTS idx_memory_units_opinion_confidence
    ON memory_units (bank_id, confidence_score DESC)
    WHERE type = 'opinion';

-- Opinions, ranked by recency.
CREATE INDEX IF NOT EXISTS idx_memory_units_opinion_date
    ON memory_units (bank_id, event_date DESC)
    WHERE type = 'opinion';

-- Observations, ranked by recency.
CREATE INDEX IF NOT EXISTS idx_memory_units_observation_date
    ON memory_units (bank_id, event_date DESC)
    WHERE type = 'observation';

-- Observations, grouped by the consolidation pass that produced them.
CREATE INDEX IF NOT EXISTS idx_memory_units_unconsolidated
    ON memory_units (bank_id, created_at DESC)
    WHERE consolidated_at IS NULL AND type <> 'observation';