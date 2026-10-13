-- 0007_worker_columns_and_file_storage (embedded copy of migrations/0007_async_operation_claim_columns.sql)
-- Aligns the queue and file-storage shape with the reference implementation.
--
-- The background queue consumer needs claim ownership so two consumers cannot
-- process the same operation: worker_id records which consumer holds the claim
-- and claimed_at records when it was taken, so a claim older than the lease is
-- treated as abandoned and becomes reclaimable. task_payload carries the
-- serialized operation payload, which the queue previously had nowhere to put.
--
-- file_storage provides zero-configuration file storage keyed by an opaque
-- storage key, so uploaded payloads live in the database rather than on disk.

ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS worker_id    TEXT;
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS claimed_at   TIMESTAMPTZ;
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS task_payload JSONB;

-- The claim scan reads the oldest claimable operation, so the index is partial
-- and ordered the way the poll query is.
CREATE INDEX IF NOT EXISTS idx_async_operations_pending_claim
    ON async_operations (status, created_at)
    WHERE status = 'pending' AND task_payload IS NOT NULL;

-- Finding the work one consumer still holds, for a graceful shutdown or a
-- decommission, scans by the claiming consumer.
CREATE INDEX IF NOT EXISTS idx_async_operations_worker_id
    ON async_operations (worker_id)
    WHERE worker_id IS NOT NULL;

-- Uploaded file payloads, addressed by an opaque storage key.
CREATE TABLE IF NOT EXISTS file_storage (
    storage_key TEXT PRIMARY KEY,
    data        BYTEA NOT NULL
);
