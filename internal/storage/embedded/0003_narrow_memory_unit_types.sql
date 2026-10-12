-- 0003_narrow_memory_unit_types (embedded copy)

ALTER TABLE memory_units DROP CONSTRAINT IF EXISTS memory_units_type_check;
ALTER TABLE memory_units
    ADD CONSTRAINT memory_units_type_check
    CHECK (type IN ('world', 'experience', 'observation'));