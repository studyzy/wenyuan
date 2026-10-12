-- 0006_memory_unit_source_columns
-- Adds the memory-unit and document columns a bank imported from a Hindsight
-- archive needs, so a migrated bank keeps its source context and metadata
-- rather than dropping values that have no column yet.

ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS context          TEXT;
ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS event_date       TIMESTAMPTZ;
ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS confidence_score DOUBLE PRECISION;
ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS access_count     INTEGER NOT NULL DEFAULT 0;

ALTER TABLE documents ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';