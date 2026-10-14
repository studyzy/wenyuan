-- The schema inventory both sides of the comparison are rendered from.
--
-- Read as: one row per object that a client can see and depend on — table,
-- column, index, constraint, function, view, sequence, trigger, extension.
-- Both databases are dumped with the same query so the outputs are directly
-- comparable, which is what makes the diff trustworthy — rendering each side
-- differently would let a formatting difference read as a schema difference.
--
-- Every row is compared in full by scripts/schema_inventory.py, not just by
-- name, because an object with the right name and the wrong definition is
-- exactly the kind of difference that a name-only comparison hides:
--
--   * columns carry the declared type as `format_type` renders it, so an array
--     whose element width differs (varchar[] vs varchar(255)[]) shows up, plus
--     the identity and generated flags, which decide whether an insert may
--     omit or must supply the column
--   * indexes and constraints carry their full definition
--   * functions carry their body, because the maintenance discovery routines
--     are pure SQL and a divergent body is a divergent behaviour
--
-- Usage: psql -v schema=public -f scripts/schema_diff.sql

\pset tuples_only on
\pset format unaligned

-- Tables.
SELECT 'TABLE|' || table_name
FROM information_schema.tables
WHERE table_schema = :'schema' AND table_type = 'BASE TABLE'
  AND table_name <> 'alembic_version' AND table_name <> 'schema_migrations'
ORDER BY table_name;

-- Columns, with the parts that decide whether a query works: the declared type
-- (which for an array includes the element width), nullability, default, and
-- whether the value is supplied by an identity or a generation expression.
SELECT 'COLUMN|' || c.relname || '|' || a.attname || '|'
    || format_type(a.atttypid, a.atttypmod)
    || '|' || CASE WHEN a.attnotnull THEN 'notnull' ELSE 'null' END
    || '|' || COALESCE(regexp_replace(pg_get_expr(d.adbin, d.adrelid), '\s+', ' ', 'g'), '')
    || '|' || CASE a.attidentity
                  WHEN 'a' THEN 'identity-always'
                  WHEN 'd' THEN 'identity-default'
                  ELSE ''
               END
    || '|' || CASE a.attgenerated
                  WHEN 's' THEN 'generated-stored'
                  ELSE ''
               END
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_attrdef d ON d.adrelid = c.oid AND d.adnum = a.attnum
WHERE n.nspname = :'schema' AND c.relkind = 'r'
  AND a.attnum > 0 AND NOT a.attisdropped
  AND c.relname <> 'alembic_version' AND c.relname <> 'schema_migrations'
ORDER BY c.relname, a.attname;

-- Indexes, including the partial predicate and the uniqueness flag: a missing
-- index is a performance regression, and a missing unique index is a
-- correctness one.
SELECT 'INDEX|' || t.relname || '|' || i.relname || '|'
    || CASE WHEN ix.indisunique THEN 'unique' ELSE 'plain' END || '|'
    || pg_get_indexdef(ix.indexrelid)
FROM pg_index ix
JOIN pg_class i ON i.oid = ix.indexrelid
JOIN pg_class t ON t.oid = ix.indrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = :'schema'
  AND t.relname <> 'alembic_version' AND t.relname <> 'schema_migrations'
  -- A primary-key or unique-constraint backing index is also reported as a
  -- constraint below, so reporting it twice would double-count every table.
  AND NOT ix.indisprimary
  AND NOT EXISTS (
      SELECT 1 FROM pg_constraint c WHERE c.conindid = ix.indexrelid
  )
ORDER BY t.relname, i.relname;

-- Constraints that are not indexes: primary keys, uniques, foreign keys and
-- checks. A missing foreign key means orphan rows become possible.
SELECT 'CONSTRAINT|' || t.relname || '|' || c.conname || '|' || c.contype::text || '|'
    || pg_get_constraintdef(c.oid)
FROM pg_constraint c
JOIN pg_class t ON t.oid = c.conrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = :'schema'
  AND t.relname <> 'alembic_version' AND t.relname <> 'schema_migrations'
ORDER BY t.relname, c.conname;

-- Functions and procedures in the schema. The maintenance discovery routines
-- live here; their bodies are the behaviour, so the body is part of the row.
SELECT 'FUNCTION|' || p.proname || '|' || pg_get_function_identity_arguments(p.oid) || '|'
    || p.prokind::text || '|'
    || regexp_replace(pg_get_functiondef(p.oid), '\s+', ' ', 'g')
FROM pg_proc p
JOIN pg_namespace n ON n.oid = p.pronamespace
WHERE n.nspname = :'schema' AND p.prokind IN ('f', 'p')
ORDER BY p.proname, pg_get_function_identity_arguments(p.oid);

-- Views.
SELECT 'VIEW|' || table_name || '|' || regexp_replace(view_definition, '\s+', ' ', 'g')
FROM information_schema.views
WHERE table_schema = :'schema'
ORDER BY table_name;

-- Sequences, which a serial-style key would create and an identity key would
-- not — the two are not interchangeable for a client that supplies its own key.
SELECT 'SEQUENCE|' || sequence_name || '|' || data_type || '|' || start_value || '|' || increment
FROM information_schema.sequences
WHERE sequence_schema = :'schema'
ORDER BY sequence_name;

-- Triggers defined by the schema itself (not the ones a foreign key creates).
SELECT 'TRIGGER|' || t.relname || '|' || tr.tgname || '|'
    || regexp_replace(pg_get_triggerdef(tr.oid), '\s+', ' ', 'g')
FROM pg_trigger tr
JOIN pg_class t ON t.oid = tr.tgrelid
JOIN pg_namespace n ON n.oid = t.relnamespace
WHERE n.nspname = :'schema' AND NOT tr.tgisinternal
ORDER BY t.relname, tr.tgname;

-- Extensions. Not schema-scoped, but a schema built by a different toolchain
-- must install the ones its objects depend on, so both sides are compared.
SELECT 'EXTENSION|' || extname || '|' || extversion
FROM pg_extension
ORDER BY extname;