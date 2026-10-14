-- The schema inventory both sides of the comparison are rendered from.
--
-- Read as: one row per table, column, index or constraint. Both databases are
-- dumped with the same query so the outputs are directly comparable, which is
-- what makes the diff trustworthy — rendering each side differently would let a
-- formatting difference read as a schema difference.
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

-- Columns, with the parts that decide whether a query works: type, width,
-- nullability and default.
SELECT 'COLUMN|' || table_name || '|' || column_name || '|'
    || CASE
         WHEN data_type = 'character varying' THEN 'varchar'
         WHEN data_type = 'timestamp with time zone' THEN 'timestamptz'
         WHEN data_type = 'timestamp without time zone' THEN 'timestamp'
         WHEN data_type = 'double precision' THEN 'float8'
         WHEN data_type = 'integer' THEN 'int4'
         WHEN data_type = 'bigint' THEN 'int8'
         WHEN data_type = 'boolean' THEN 'bool'
         ELSE data_type
       END
    || '|' || COALESCE(character_maximum_length::text, '')
    || '|' || CASE WHEN is_nullable = 'NO' THEN 'notnull' ELSE 'null' END
    || '|' || COALESCE(regexp_replace(column_default, '\s+', ' ', 'g'), '')
FROM information_schema.columns
WHERE table_schema = :'schema'
  AND table_name <> 'alembic_version' AND table_name <> 'schema_migrations'
ORDER BY table_name, column_name;

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
