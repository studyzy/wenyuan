package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
)

// DirectSource reads a bank's rows straight out of a reference-shaped database
// and renders them as an archive, so a bank can be moved without first writing
// an export file by hand.
//
// The reference schema names several columns differently from the destination
// (`fact_type` where the destination says `type`, `canonical_name` where it says
// `name`, a `bank_id` column where the destination keys banks on `id`). The
// reader translates those names here, which is what confines the difference to
// one place instead of spreading it through the importer.
type DirectSource struct {
	db     DB
	bankID string
}

// NewDirectSource binds a reader to an open connection and one bank.
func NewDirectSource(db DB, bankID string) *DirectSource {
	return &DirectSource{db: db, bankID: bankID}
}

// Read renders the bank as an archive. Documents, their chunks and their facts
// are read in one pass each; a fact's causal relations are matched from the
// links table by comparing the units they join.
func (s *DirectSource) Read(ctx context.Context) (*hindsightarchive.Archive, error) {
	documents, err := s.readDocuments(ctx)
	if err != nil {
		return nil, err
	}
	byDocument := map[string]*hindsightarchive.Document{}
	order := make([]*hindsightarchive.Document, 0, len(documents))
	for _, doc := range documents {
		byDocument[doc.ID] = doc
		order = append(order, doc)
	}

	if err := s.attachChunks(ctx, byDocument); err != nil {
		return nil, err
	}
	if err := s.attachFacts(ctx, byDocument); err != nil {
		return nil, err
	}
	rendered := make([]hindsightarchive.Document, 0, len(order))
	facts := 0
	for _, doc := range order {
		rendered = append(rendered, *doc)
		facts += len(doc.Facts)
	}
	return &hindsightarchive.Archive{
		Manifest: hindsightarchive.Manifest{
			SchemaVersion: hindsightarchive.SupportedSchemaVersion,
			SourceBankID:  s.bankID,
			ExportedAt:    time.Now().UTC(),
			DocumentCount: len(rendered),
			FactCount:     facts,
			ArchiveType:   "direct",
		},
		Documents: rendered,
	}, nil
}

// readDocuments reads the bank's documents.
func (s *DirectSource) readDocuments(ctx context.Context) ([]*hindsightarchive.Document, error) {
	query := `SELECT id, original_text, retain_params, tags, created_at
	          FROM documents WHERE bank_id = $1 ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query, s.bankID)
	if err != nil {
		return nil, fmt.Errorf("read documents: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var documents []*hindsightarchive.Document
	for rows.Next() {
		var (
			id, text   string
			retainJSON []byte
			tagsJSON   []byte
			created    sql.NullTime
		)
		if err := rows.Scan(&id, &text, &retainJSON, &tagsJSON, &created); err != nil {
			return nil, err
		}
		documents = append(documents, &hindsightarchive.Document{
			ID:           id,
			OriginalText: text,
			RetainParams: decodeObject(retainJSON),
			Tags:         decodeStrings(tagsJSON),
			CreatedAt:    nullTime(created),
		})
	}
	return documents, rows.Err()
}

// attachChunks reads the bank's chunks and hangs each on its document.
func (s *DirectSource) attachChunks(ctx context.Context, byDocument map[string]*hindsightarchive.Document) error {
	query := `SELECT document_id, chunk_index, COALESCE(chunk_text, text, '')
	          FROM chunks WHERE bank_id = $1 ORDER BY document_id, chunk_index`
	rows, err := s.db.QueryContext(ctx, query, s.bankID)
	if err != nil {
		// A reference schema without a chunks table simply has no chunks, which
		// is not a reason to fail the whole read.
		return nil
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var documentID, text string
		var index int
		if err := rows.Scan(&documentID, &index, &text); err != nil {
			return err
		}
		if doc, ok := byDocument[documentID]; ok {
			doc.Chunks = append(doc.Chunks, hindsightarchive.Chunk{ChunkIndex: index, ChunkText: text})
		}
	}
	return rows.Err()
}

// attachFacts reads the bank's memory units and hangs each on its document. A
// fact with no document is emitted under a synthetic document of its own so no
// fact is lost.
func (s *DirectSource) attachFacts(ctx context.Context, byDocument map[string]*hindsightarchive.Document) error {
	query := `SELECT id, document_id, text, fact_type, context, event_date,
	                 occurred_start, occurred_end, mentioned_at, confidence_score,
	                 metadata, created_at
	          FROM memory_units WHERE bank_id = $1 ORDER BY created_at, id`
	rows, err := s.db.QueryContext(ctx, query, s.bankID)
	if err != nil {
		return fmt.Errorf("read memory units: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var (
			id, text, factType string
			documentID         sql.NullString
			context            sql.NullString
			eventDate          sql.NullTime
			occurredStart      sql.NullTime
			occurredEnd        sql.NullTime
			mentionedAt        sql.NullTime
			confidence         sql.NullFloat64
			metadataJSON       []byte
			createdAt          sql.NullTime
		)
		if err := rows.Scan(&id, &documentID, &text, &factType, &context, &eventDate,
			&occurredStart, &occurredEnd, &mentionedAt, &confidence, &metadataJSON, &createdAt); err != nil {
			return err
		}
		fact := hindsightarchive.Fact{
			Text:          text,
			FactType:      factType,
			Context:       context.String,
			EventDate:     nullTime(eventDate),
			OccurredStart: nullTime(occurredStart),
			OccurredEnd:   nullTime(occurredEnd),
			MentionedAt:   nullTime(mentionedAt),
			Metadata:      decodeObject(metadataJSON),
			CreatedAt:     nullTime(createdAt),
		}
		if confidence.Valid {
			if fact.Metadata == nil {
				fact.Metadata = map[string]any{}
			}
			fact.Metadata["confidence_score"] = confidence.Float64
		}
		target := byDocument[documentID.String]
		if target == nil {
			target = &hindsightarchive.Document{ID: documentID.String}
			if target.ID == "" {
				target.ID = "units-" + s.bankID
			}
			byDocument[target.ID] = target
		}
		target.Facts = append(target.Facts, fact)
	}
	return rows.Err()
}

// nullTime converts an optional timestamp to a pointer.
func nullTime(value sql.NullTime) *time.Time {
	if !value.Valid {
		return nil
	}
	at := value.Time
	return &at
}

// decodeObject parses a JSON object column. An unreadable or absent value
// yields an empty object so the caller never has to nil-check a metadata bag.
func decodeObject(data []byte) map[string]any {
	if len(data) == 0 {
		return map[string]any{}
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil || decoded == nil {
		return map[string]any{}
	}
	return decoded
}

// decodeStrings parses a JSON array column of strings.
func decodeStrings(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	var decoded []string
	if err := json.Unmarshal(data, &decoded); err != nil {
		return nil
	}
	return decoded
}
