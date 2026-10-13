-- 0017_attachments
-- Adds inline retain attachments.
--
-- A retain item's content may be an ordered list of text, image and file
-- blocks. The blocks are flattened at the API boundary into one canonical body
-- in which each attachment is an atomic placeholder, and the bytes are written
-- to file storage content-addressed by their sha256. The document text
-- therefore stays plain text, which is what keeps content_hash idempotency and
-- chunk-delta re-extraction working unchanged.
--
-- attachments records what each attachment is and where its bytes live. An
-- attachment belongs to the document that carries it, so the primary key is
-- the document plus the content hash: two documents carrying the same image
-- hold two rows and two blobs, which is what lets a delete free exactly its own
-- bytes without having to ask whether some other document still needs them.
-- Document text references an attachment by short_id, a prefix of the digest,
-- so that prefix must identify exactly one attachment within its document.
--
-- memory_units.attachment_ids records which attachments a fact was drawn from.
-- It is a column rather than a junction table because these ids behave exactly
-- like tags: a short array read with the unit and never queried on its own.

CREATE TABLE IF NOT EXISTS attachments (
    bank_id         TEXT NOT NULL,
    document_id     TEXT NOT NULL,
    attachment_hash VARCHAR(64) NOT NULL,
    short_id        VARCHAR(12) NOT NULL,
    media_type      TEXT NOT NULL,
    byte_size       BIGINT NOT NULL,
    storage_key     TEXT NOT NULL,
    kind            TEXT NOT NULL DEFAULT 'file',
    filename        TEXT,
    created_at      TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    CONSTRAINT pk_attachments PRIMARY KEY (bank_id, document_id, attachment_hash)
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_attachments_short_id
    ON attachments (bank_id, document_id, short_id);

ALTER TABLE memory_units
    ADD COLUMN IF NOT EXISTS attachment_ids TEXT[] NOT NULL DEFAULT '{}'::text[];