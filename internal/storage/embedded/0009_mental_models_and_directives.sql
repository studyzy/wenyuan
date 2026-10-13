-- 0009_mental_models_and_directives (embedded copy of migrations/0009_mental_models_and_directives.sql)
-- Adds the mental-model table and the directive table.
--
-- A mental model is a named, reusable note a reflection can be built from. Its
-- subtype selects which kind it is, so a pinned note and a directive are the
-- same storage with a different subtype rather than separate mechanisms.

CREATE TABLE IF NOT EXISTS mental_models (
    id           VARCHAR(64)  NOT NULL,
    bank_id      VARCHAR(64)  NOT NULL,
    subtype      VARCHAR(32)  NOT NULL,
    name         VARCHAR(256) NOT NULL,
    description  TEXT         NOT NULL DEFAULT '',
    source_query TEXT,
    content      TEXT,
    embedding    vector(384),
    entity_id    UUID,
    observations JSONB        DEFAULT '{"observations": []}'::jsonb,
    links        VARCHAR[],
    tags         VARCHAR[]    DEFAULT '{}',
    max_tokens   INTEGER      NOT NULL DEFAULT 2048,
    trigger      JSONB        DEFAULT '{"refresh_after_consolidation": false}'::jsonb,
    structured_content  JSONB,
    reflect_response    JSONB,
    history      JSONB        DEFAULT '[]'::jsonb,
    last_refreshed_source_query TEXT,
    last_refreshed_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    last_updated TIMESTAMP WITH TIME ZONE,
    created_at   TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    PRIMARY KEY (id, bank_id),
    FOREIGN KEY (bank_id) REFERENCES banks (id) ON DELETE CASCADE,
    CONSTRAINT chk_mental_models_subtype
        CHECK (subtype IN ('structural', 'emergent', 'pinned', 'learned'))
);

CREATE INDEX IF NOT EXISTS idx_mental_models_bank_id ON mental_models (bank_id);
CREATE INDEX IF NOT EXISTS idx_mental_models_subtype ON mental_models (bank_id, subtype);

-- A directive is a standing instruction the reflection loop follows. It is a
-- first-class row rather than a mental-model subtype, so its priority and
-- active flag are addressable without decoding another record.
CREATE TABLE IF NOT EXISTS directives (
    id         TEXT         NOT NULL,
    bank_id    VARCHAR(64)  NOT NULL,
    name       VARCHAR(256) NOT NULL,
    content    TEXT         NOT NULL,
    priority   INTEGER      NOT NULL DEFAULT 0,
    is_active  BOOLEAN      NOT NULL DEFAULT true,
    tags       VARCHAR[]    DEFAULT '{}',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    PRIMARY KEY (id),
    FOREIGN KEY (bank_id) REFERENCES banks (id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_directives_bank_id ON directives (bank_id);
