-- 0005_retain_pipeline (embedded copy of migrations/0005_retain_pipeline.sql)

ALTER TABLE documents ADD COLUMN IF NOT EXISTS retain_params JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS original_text TEXT;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS content_hash TEXT;
ALTER TABLE documents ADD COLUMN IF NOT EXISTS metadata    JSONB NOT NULL DEFAULT '{}'::jsonb;

CREATE INDEX IF NOT EXISTS idx_documents_retain_params ON documents USING GIN (retain_params);

ALTER TABLE chunks ADD COLUMN IF NOT EXISTS index        INTEGER NOT NULL DEFAULT 0;
ALTER TABLE chunks ADD COLUMN IF NOT EXISTS content_hash TEXT;

ALTER TABLE entities ADD COLUMN IF NOT EXISTS kind          TEXT NOT NULL DEFAULT 'regular';
ALTER TABLE entities ADD COLUMN IF NOT EXISTS first_seen    TIMESTAMPTZ;
ALTER TABLE entities ADD COLUMN IF NOT EXISTS last_seen     TIMESTAMPTZ;
ALTER TABLE entities ADD COLUMN IF NOT EXISTS mention_count INTEGER NOT NULL DEFAULT 0;

ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS operation_type  TEXT NOT NULL DEFAULT 'retain';
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS error_message   TEXT;
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS result_metadata JSONB NOT NULL DEFAULT '{}'::jsonb;
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS retry_count     INTEGER NOT NULL DEFAULT 0;
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS next_retry_at   TIMESTAMPTZ;
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS progress        JSONB;
ALTER TABLE async_operations ADD COLUMN IF NOT EXISTS completed_at    TIMESTAMPTZ;