-- 0004_bank_aliases: alias table, bank watermarks and named indexes.
-- Safe to re-run: every statement is guarded with IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS bank_aliases (
    alias       TEXT PRIMARY KEY,
    bank_id     TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    is_primary  BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX IF NOT EXISTS idx_bank_aliases_bank ON bank_aliases (bank_id);

CREATE UNIQUE INDEX IF NOT EXISTS idx_bank_aliases_one_primary
    ON bank_aliases (bank_id) WHERE is_primary;

ALTER TABLE banks ADD COLUMN IF NOT EXISTS last_write_at    TIMESTAMPTZ;
ALTER TABLE banks ADD COLUMN IF NOT EXISTS last_document_at TIMESTAMPTZ;