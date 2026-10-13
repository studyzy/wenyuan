-- 0013_core_indexes
-- Adds the lookup indexes the core tables need.
--
-- Each index backs a query the service actually runs: a bank-scoped listing
-- filters on the bank, a document lookup filters on the document, and a tag
-- filter needs containment rather than equality. Without them every one of
-- those queries scans its table.
--
-- The column names follow this schema rather than the reference's, because the
-- tables here are named and shaped independently. What is carried over is the
-- index *purpose*, not the reference's identifiers.

-- Memory units: the recall and listing paths filter by bank, type and date.
CREATE INDEX IF NOT EXISTS idx_memory_units_bank_id ON memory_units (bank_id);
CREATE INDEX IF NOT EXISTS idx_memory_units_bank_type ON memory_units (bank_id, type);
CREATE INDEX IF NOT EXISTS idx_memory_units_type ON memory_units (type);
CREATE INDEX IF NOT EXISTS idx_memory_units_document_id ON memory_units (document_id);
CREATE INDEX IF NOT EXISTS idx_memory_units_chunk_id ON memory_units (chunk_id);
CREATE INDEX IF NOT EXISTS idx_memory_units_mentioned_at ON memory_units (mentioned_at DESC);
CREATE INDEX IF NOT EXISTS idx_memory_units_bank_event_date ON memory_units (bank_id, event_date DESC);
-- A tag filter asks whether a set of tags is contained, which needs GIN.
CREATE INDEX IF NOT EXISTS idx_memory_units_tags ON memory_units USING GIN (tags);

-- Documents: listing and dedup both filter on these.
CREATE INDEX IF NOT EXISTS idx_documents_bank_id ON documents (bank_id);
CREATE INDEX IF NOT EXISTS idx_documents_content_hash ON documents (content_hash);
CREATE INDEX IF NOT EXISTS idx_documents_tags ON documents USING GIN (tags);

-- Chunks: the listing filters by bank and by document.
CREATE INDEX IF NOT EXISTS idx_chunks_bank_id ON chunks (bank_id);
CREATE INDEX IF NOT EXISTS idx_chunks_document_id ON chunks (document_id);

-- Entities: resolution filters on the bank and the name, case-insensitively.
CREATE INDEX IF NOT EXISTS idx_entities_bank_id ON entities (bank_id);
CREATE INDEX IF NOT EXISTS idx_entities_bank_name ON entities (bank_id, name);
CREATE INDEX IF NOT EXISTS idx_entities_lower_name ON entities (bank_id, LOWER(name));

-- Unit-to-entity links: traversed in both directions.
CREATE INDEX IF NOT EXISTS idx_unit_entities_unit ON unit_entities (unit_id);
CREATE INDEX IF NOT EXISTS idx_unit_entities_entity ON unit_entities (entity_id);

-- Memory links: the graph walk enters from either endpoint.
CREATE INDEX IF NOT EXISTS idx_memory_links_bank_id ON memory_links (bank_id);
CREATE INDEX IF NOT EXISTS idx_memory_links_from_id ON memory_links (from_id);
CREATE INDEX IF NOT EXISTS idx_memory_links_to_id ON memory_links (to_id);
CREATE INDEX IF NOT EXISTS idx_memory_links_link_type ON memory_links (link_type);

-- Co-occurrence: ranked by count, entered by either entity.
CREATE INDEX IF NOT EXISTS idx_entity_cooccurrences_bank ON entity_cooccurrences (bank_id);
CREATE INDEX IF NOT EXISTS idx_entity_cooccurrences_count ON entity_cooccurrences (count DESC);

-- Operations: the queue and the listing both filter on bank and status.
CREATE INDEX IF NOT EXISTS idx_async_operations_bank_id ON async_operations (bank_id);
CREATE INDEX IF NOT EXISTS idx_async_operations_bank_status ON async_operations (bank_id, status);
CREATE INDEX IF NOT EXISTS idx_async_operations_status ON async_operations (status);
CREATE INDEX IF NOT EXISTS idx_async_operations_result_metadata ON async_operations USING GIN (result_metadata);
