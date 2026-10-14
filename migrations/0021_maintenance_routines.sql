-- Maintenance discovery routines, and the mental-model staleness watermark.
--
-- The background maintenance loop decides *what* to work on by asking four
-- database-global routines. Each enumerates pg_class across every schema and
-- dispatches per schema, so exactly one copy of each should exist in a
-- database, installed into the schema this deployment is configured to use and
-- called from there. They are created here rather than in Go because a
-- single round-trip that scans every schema is the whole point: a per-schema
-- query storm does not scale with tenant count the way the work it finds does.
--
-- These are STABLE plpgsql functions over pg_class and are safe to run
-- concurrently: each is a read. The delete work they lead to is what has to be
-- collision-proof, and that is handled at the delete (SKIP LOCKED), not here.

-- Retention discovery: the schemas holding rows older than the window.
-- A non-positive window means "keep forever", reported as an empty set so the
-- caller skips the sweep entirely rather than treating 0 as "delete all".
CREATE OR REPLACE FUNCTION schemas_with_expired_rows(
    p_table text, p_ts_col text, p_days int
)
RETURNS SETOF text
LANGUAGE plpgsql STABLE
AS $fn$
DECLARE
    sch text;
    has_expired boolean;
BEGIN
    IF p_days IS NULL OR p_days <= 0 THEN
        RETURN;
    END IF;
    FOR sch IN
        SELECT n.nspname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = p_table AND c.relkind = 'r'
    LOOP
        BEGIN
            EXECUTE format(
                'SELECT EXISTS (SELECT 1 FROM %I.%I WHERE %I < NOW() - make_interval(days => $1))',
                sch, p_table, p_ts_col
            ) INTO has_expired USING p_days;
        EXCEPTION
            -- Schema or its table vanished mid-scan; skip it.
            WHEN undefined_table OR invalid_schema_name OR undefined_column THEN
                CONTINUE;
        END;
        IF has_expired THEN
            RETURN NEXT sch;
        END IF;
    END LOOP;
END;
$fn$;

-- Consolidation reconcile: banks holding facts that are eligible but have no
-- consolidation in flight.
--
-- A fact is stranded when a consolidation operation failed terminally after it
-- was already written: consolidated_at and consolidation_failed_at are both
-- null, so nothing will ever pick it up again. This is how those facts are
-- found and requeued.
--
-- The bank-level enable switch is deliberately not consulted here. This
-- routine answers "is there work", not "should it run" — the caller resolves
-- the per-bank configuration before enqueuing, which is also where a bank that
-- has observation-building switched off is filtered.
CREATE OR REPLACE FUNCTION banks_needing_consolidation()
RETURNS TABLE(schema_name text, bank_id text)
LANGUAGE plpgsql STABLE
AS $fn$
DECLARE
    sch text;
BEGIN
    FOR sch IN
        SELECT n.nspname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = 'memory_units' AND c.relkind = 'r'
    LOOP
        BEGIN
            RETURN QUERY EXECUTE format($q$
                SELECT %1$L::text, m.bank_id
                FROM %1$I.memory_units m
                JOIN %1$I.banks b ON b.id = m.bank_id
                WHERE m.consolidated_at IS NULL
                  AND m.consolidation_failed_at IS NULL
                  AND m.type IN ('experience', 'world')
                  AND NOT EXISTS (
                      SELECT 1 FROM %1$I.async_operations o
                      WHERE o.bank_id = m.bank_id
                        AND o.operation_type = 'consolidate'
                        AND o.status IN ('pending', 'processing')
                  )
                GROUP BY m.bank_id
            $q$, sch);
        EXCEPTION
            -- Schema or its tables vanished between the pg_class snapshot and
            -- this query (tenant dropped or migrating).
            WHEN undefined_table OR invalid_schema_name OR undefined_column THEN
                CONTINUE;
        END;
    END LOOP;
END;
$fn$;

-- Operation-cleanup discovery: schemas holding terminal operations old enough
-- to prune.
--
-- Zero (or negative) retention means "keep forever": report nothing so the
-- caller skips the sweep. Only terminal rows are eligible, so a schema holding
-- nothing but pending or processing work is correctly reported as having
-- nothing to prune.
CREATE OR REPLACE FUNCTION schemas_with_expired_operations(p_days int)
RETURNS SETOF text
LANGUAGE plpgsql STABLE
AS $fn$
DECLARE
    sch text;
    has_expired boolean;
    prev_lock_timeout text;
BEGIN
    IF p_days IS NULL OR p_days <= 0 THEN
        RETURN;
    END IF;
    -- A schema mid-DDL holds an AccessExclusiveLock, and waiting on it is what
    -- closes a deadlock cycle between two sweeps. A short timeout turns that
    -- wait into a skip.
    prev_lock_timeout := current_setting('lock_timeout');
    PERFORM set_config('lock_timeout', '250ms', true);
    FOR sch IN
        SELECT n.nspname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = 'async_operations' AND c.relkind = 'r'
    LOOP
        BEGIN
            EXECUTE format(
                'SELECT EXISTS ('
                '  SELECT 1 FROM %I.async_operations'
                '  WHERE status IN (''completed'', ''failed'', ''cancelled'')'
                '    AND updated_at < NOW() - make_interval(days => $1)'
                ')',
                sch
            ) INTO has_expired USING p_days;
        EXCEPTION
            -- Schema or its tables vanished between the pg_class snapshot and
            -- this query (tenant dropped or migrating).
            WHEN undefined_table OR invalid_schema_name OR undefined_column THEN
                CONTINUE;
            -- Both outcomes of the same race, kept as separate arms so each
            -- reason is legible where it is handled. deadlock_detected is
            -- belt-and-braces for a cycle formed before lock_timeout.
            WHEN lock_not_available OR deadlock_detected THEN
                CONTINUE;
        END;
        IF has_expired THEN
            RETURN NEXT sch;
        END IF;
    END LOOP;
    PERFORM set_config('lock_timeout', prev_lock_timeout, true);
END;
$fn$;

-- Scheduled-refresh discovery: mental models carrying a cron schedule with no
-- refresh in flight.
--
-- The routine reports the schedule and the last refresh, not whether the model
-- is currently due: cron arithmetic is not expressible in plain SQL, so
-- due-ness is decided by the caller. A model with a refresh already queued or
-- running is excluded, so a fleet whose every process runs this loop does not
-- queue one wave per process.
CREATE OR REPLACE FUNCTION mental_models_with_cron()
RETURNS TABLE(schema_name text, bank_id text, mental_model_id text,
              refresh_cron text, last_refreshed_at timestamptz)
LANGUAGE plpgsql STABLE
AS $fn$
DECLARE
    sch text;
BEGIN
    FOR sch IN
        SELECT n.nspname
        FROM pg_class c
        JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relname = 'mental_models' AND c.relkind = 'r'
    LOOP
        BEGIN
            RETURN QUERY EXECUTE format($q$
                SELECT %1$L::text, mm.bank_id::text, mm.id::text,
                       mm.trigger->>'refresh_cron', mm.last_refreshed_at
                FROM %1$I.mental_models mm
                WHERE COALESCE(mm.trigger->>'refresh_cron', '') <> ''
                  AND NOT EXISTS (
                      SELECT 1 FROM %1$I.async_operations o
                      WHERE o.bank_id = mm.bank_id
                        AND o.operation_type = 'refresh_mental_model'
                        AND o.status IN ('pending', 'processing')
                        AND o.task_payload->>'mental_model_id' = mm.id::text
                  )
            $q$, sch);
        EXCEPTION
            -- Schema or its tables vanished between the pg_class snapshot and
            -- this query (tenant dropped or migrating).
            WHEN undefined_table OR invalid_schema_name OR undefined_column THEN
                CONTINUE;
        END;
    END LOOP;
END;
$fn$;

-- The watermark a scheduled refresh compares against: the write time of the
-- newest memory this model actually saw. Nullable, because a model that has
-- never been refreshed has seen nothing.
ALTER TABLE mental_models ADD COLUMN IF NOT EXISTS last_memory_seen_at TIMESTAMPTZ;

-- Backfill from the existing refresh time so a model refreshed before this
-- column existed is not treated as having seen nothing, which would make it
-- stale on the next tick and refresh every model in the deployment at once.
UPDATE mental_models
SET last_memory_seen_at = last_refreshed_at
WHERE last_memory_seen_at IS NULL AND last_refreshed_at IS NOT NULL;

-- A failed automatic refresh parks the model until an explicit refresh
-- releases it, so a schedule that fails every time does not burn a model call
-- per tick forever.
ALTER TABLE mental_models ADD COLUMN IF NOT EXISTS last_refresh_failed_at TIMESTAMPTZ;

-- The discovery routine's exclusion probe, and the cron index the per-bank
-- staleness check walks.
CREATE INDEX IF NOT EXISTS idx_mental_models_refresh_cron
    ON mental_models ((trigger ->> 'refresh_cron'))
    WHERE COALESCE(trigger ->> 'refresh_cron', '') <> '';