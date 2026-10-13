-- 0011_knowledge_nodes (embedded copy of migrations/0011_knowledge_nodes.sql)
-- Adds the knowledge tree: folders group pages, and a page carries text.
--
-- The identifier is unique per bank, and a node points at its parent so the
-- tree can be rebuilt without a separate ordering column.

CREATE TABLE IF NOT EXISTS knowledge_nodes (
    id         VARCHAR(64)  NOT NULL,
    bank_id    VARCHAR(64)  NOT NULL,
    parent_id  VARCHAR(64),
    kind       VARCHAR(16)  NOT NULL,
    title      VARCHAR(512) NOT NULL,
    text       TEXT,
    tags       VARCHAR[]    DEFAULT '{}',
    created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
    PRIMARY KEY (id, bank_id),
    FOREIGN KEY (bank_id) REFERENCES banks (id) ON DELETE CASCADE,
    CONSTRAINT chk_knowledge_kind CHECK (kind IN ('folder', 'page'))
);

CREATE INDEX IF NOT EXISTS idx_knowledge_nodes_bank ON knowledge_nodes (bank_id, parent_id);
