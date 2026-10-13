-- 0016_migration_roundtrip_columns
-- Adds the columns the archive importer used to drop on the floor.
--
-- The hindsight export format carries fact identity, observation scope,
-- consolidation state and per-document retain parameters. Before this
-- migration the importer had nowhere to put them, so `unmappedFields()` in
-- internal/migration/hindsight/map.go reported them as lost. Adding the
-- columns makes an export/import round trip lossless and keeps the target a
-- superset of the reference schema.

ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS source_id text;
ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS observation_scopes jsonb;
ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS consolidated_at timestamptz;
ALTER TABLE memory_units ADD COLUMN IF NOT EXISTS consolidation_failed_at timestamptz;