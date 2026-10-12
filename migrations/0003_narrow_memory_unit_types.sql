-- 0003_narrow_memory_unit_types: narrow the memory-unit type constraint to the
-- final accepted set. This supersedes the wider set declared in migration 0001.

ALTER TABLE memory_units DROP CONSTRAINT IF EXISTS memory_units_type_check;
ALTER TABLE memory_units
    ADD CONSTRAINT memory_units_type_check
    CHECK (type IN ('world', 'experience', 'observation'));