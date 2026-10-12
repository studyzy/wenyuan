-- 0001_init: initial memory-domain schema for the destination.
-- Safe to re-run: every statement is guarded with IF NOT EXISTS.

CREATE TABLE IF NOT EXISTS banks (
    id                      TEXT PRIMARY KEY,
    name                    TEXT NOT NULL,
    created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at              TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS documents (
    id          TEXT PRIMARY KEY,
    bank_id     TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    source      TEXT,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS chunks (
    id          TEXT PRIMARY KEY,
    bank_id     TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    document_id TEXT REFERENCES documents (id) ON DELETE CASCADE,
    text        TEXT NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS entities (
    id       TEXT PRIMARY KEY,
    bank_id  TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    name     TEXT NOT NULL,
    kind     TEXT
);

CREATE TABLE IF NOT EXISTS memory_units (
    id              TEXT PRIMARY KEY,
    bank_id         TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    text            TEXT NOT NULL,
    type            TEXT NOT NULL,
    metadata        JSONB NOT NULL DEFAULT '{}'::jsonb,
    document_id     TEXT,
    chunk_id        TEXT,
    occurred_start  TIMESTAMPTZ,
    occurred_end    TIMESTAMPTZ,
    mentioned_at    TIMESTAMPTZ,
    tags            TEXT[] NOT NULL DEFAULT '{}',
    source_fact_ids TEXT[] NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS unit_entities (
    unit_id   TEXT NOT NULL REFERENCES memory_units (id) ON DELETE CASCADE,
    entity_id TEXT NOT NULL REFERENCES entities (id) ON DELETE CASCADE,
    PRIMARY KEY (unit_id, entity_id)
);

CREATE TABLE IF NOT EXISTS memory_links (
    id       TEXT PRIMARY KEY,
    bank_id  TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    from_id  TEXT NOT NULL REFERENCES memory_units (id) ON DELETE CASCADE,
    to_id    TEXT NOT NULL REFERENCES memory_units (id) ON DELETE CASCADE,
    link_type TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS entity_cooccurrences (
    bank_id   TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    entity_a  TEXT NOT NULL,
    entity_b  TEXT NOT NULL,
    count     INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (bank_id, entity_a, entity_b)
);

CREATE TABLE IF NOT EXISTS async_operations (
    id          TEXT PRIMARY KEY,
    bank_id     TEXT NOT NULL REFERENCES banks (id) ON DELETE CASCADE,
    status      TEXT NOT NULL,
    error       TEXT,
    run         INTEGER NOT NULL DEFAULT 0,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- The initial type constraint admits a wider set than the final contract.
ALTER TABLE memory_units
    DROP CONSTRAINT IF EXISTS memory_units_type_check;
ALTER TABLE memory_units
    ADD CONSTRAINT memory_units_type_check
    CHECK (type IN ('world', 'experience', 'observation', 'opinion'));