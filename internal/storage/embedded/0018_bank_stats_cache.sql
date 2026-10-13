-- 0018_bank_stats_cache
-- Table-backed cross-process cache for the bank statistics summary.
--
-- The summary aggregates over memory_units, memory_links and async_operations,
-- which is a multi-second scan on a bank with millions of rows, and it powers a
-- UI widget and a freshness hint inside reflect. A per-process cache would make
-- each worker recompute what another already has, so this table lets one
-- worker's computation be read by every other and no caller recomputes while a
-- fresh row exists. The result is intentionally approximate, so a few tens of
-- seconds of staleness is safe.

CREATE TABLE IF NOT EXISTS bank_stats_cache (
    bank_id     TEXT PRIMARY KEY,
    payload     JSONB NOT NULL,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);