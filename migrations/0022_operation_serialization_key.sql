-- The serialization key an operation may carry.
--
-- At most one operation holding a given key runs at a time. It exists for the
-- per-record background refresh: two refreshes of the same record would each
-- read its inputs, and whichever finished last would win regardless of which
-- read more, so the second must not start while the first is running. A claim
-- with a NULL key is unaffected, which is every operation that is not a
-- per-record refresh.
--
-- Deduplication bounds the queue; this bounds concurrency. They are separate
-- concerns: without the key a refresh queued behind a running one would be
-- claimed immediately and write beside it.
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS serialization_key TEXT;

-- The claim skips a key another live claim holds, so it looks the key up on
-- every poll.
CREATE INDEX IF NOT EXISTS idx_async_operations_serialization_key
    ON async_operations (serialization_key)
    WHERE serialization_key IS NOT NULL;

-- The discovery routines exclude an operation already in flight for a subject,
-- and the retention probes order by the terminal-cleanup window. Both read the
-- status alongside the key.
CREATE INDEX IF NOT EXISTS idx_async_operations_serialization_live
    ON async_operations (serialization_key, status)
    WHERE serialization_key IS NOT NULL;
