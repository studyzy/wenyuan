package storage

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
)

// attachmentPlaceholderIDRe matches a well-formed inline-attachment placeholder
// and captures its short id. It mirrors the token the retain ingress writes into
// a document's canonical text, so a document's referenced attachments can be
// recovered from that text alone.
var attachmentPlaceholderIDRe = regexp.MustCompile(`\x{27e6}hs-att:([0-9a-fA-F]+)\x{27e7}`)

// referencedAttachmentIDs returns the distinct short ids a document's text
// references, in first-appearance order. The slice is always non-nil so an
// empty set reaches SQL as an empty array rather than a NULL.
func referencedAttachmentIDs(text string) []string {
	matches := attachmentPlaceholderIDRe.FindAllStringSubmatch(text, -1)
	out := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		id := match[1]
		if seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}

// SyncDocumentAttachments drops a document's attachment rows whose short id the
// document's current text no longer references.
//
// Only the attachments an inline placeholder names are managed here: a
// converted upload's own row records the document's source file, has no
// placeholder in the text, and must survive a re-retain (the attachment route
// resolves it when a caller downloads the original).
func (s *PostgresStore) SyncDocumentAttachments(ctx context.Context, bankID, documentID, text string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	referenced := referencedAttachmentIDs(text)
	query := `DELETE FROM ` + quoteIdent(schema) + `.attachments
	          WHERE bank_id = $1 AND document_id = $2 AND kind <> $3
	            AND NOT (short_id = ANY($4::text[]))`
	_, err := s.db.ExecContext(ctx, query, bankID, documentID, KindUploadedFile, referenced)
	return err
}

// DeleteDocumentAttachments removes one document's attachment rows. Deleting a
// document deletes exactly its own attachments; a short id another document
// still carries survives through that document's own row.
func (s *PostgresStore) DeleteDocumentAttachments(ctx context.Context, bankID, documentID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `DELETE FROM ` + quoteIdent(schema) + `.attachments WHERE bank_id = $1 AND document_id = $2`
	_, err := s.db.ExecContext(ctx, query, bankID, documentID)
	return err
}

// DeleteBankAttachments removes every attachment row a bank holds. A cleared
// bank goes on existing, so nothing cascades from the bank row and the images
// must be retired explicitly.
func (s *PostgresStore) DeleteBankAttachments(ctx context.Context, bankID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `DELETE FROM ` + quoteIdent(schema) + `.attachments WHERE bank_id = $1`
	_, err := s.db.ExecContext(ctx, query, bankID)
	return err
}

// AttachmentsByShortIDs resolves short attachment ids to a bank's attachment
// rows, keyed by short id. An id with no row is simply absent: a document whose
// bytes were reclaimed still recalls its facts without the attachment. Several
// documents can share a short id; they are content-addressed, so any row serves
// identical bytes and the first written wins.
func (s *PostgresStore) AttachmentsByShortIDs(ctx context.Context, bankID string, ids []string) (map[string]Attachment, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 map[string]Attachment
		return zero0, schemaErr
	}
	resolved := map[string]Attachment{}
	if len(ids) == 0 {
		return resolved, nil
	}
	query := `SELECT DISTINCT ON (short_id) bank_id, document_id, attachment_hash, short_id, media_type,
	                 byte_size, storage_key, kind, filename
	          FROM ` + quoteIdent(schema) + `.attachments
	          WHERE bank_id = $1 AND short_id = ANY($2::text[])
	          ORDER BY short_id, created_at, attachment_hash`
	rows, err := s.reader().QueryContext(ctx, query, bankID, ids)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var attachment Attachment
		var filename sql.NullString
		if err := rows.Scan(&attachment.BankID, &attachment.DocumentID, &attachment.AttachmentHash,
			&attachment.ShortID, &attachment.MediaType, &attachment.ByteSize, &attachment.StorageKey,
			&attachment.Kind, &filename); err != nil {
			return nil, err
		}
		attachment.Filename = filename.String
		resolved[attachment.ShortID] = attachment
	}
	return resolved, rows.Err()
}

// enrichRecallAttachments fills each match's attachment side values from the
// memory units it names. A unit's own attachment ids win; an observation that
// carries none inherits the union of its source facts', because an observation
// is read off no image but stands for the facts that were. Ids with no surviving
// row are omitted rather than fabricated, so a reclaimed image leaves the fact
// recallable without it.
func (s *PostgresStore) enrichRecallAttachments(ctx context.Context, bankID string, matches []RecallMatch) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return
	}
	if len(matches) == 0 {
		return
	}
	ids := make([]string, 0, len(matches))
	for _, match := range matches {
		ids = append(ids, match.ID)
	}
	query := `WITH target AS (
	              SELECT id, attachment_ids, source_memory_ids
	              FROM ` + quoteIdent(schema) + `.memory_units
	              WHERE bank_id = $1 AND id::text = ANY($2::text[])
	          ),
	          effective AS (
	              SELECT t.id AS unit_id, s.short_id
	              FROM target t
	              CROSS JOIN LATERAL unnest(
	                  CASE
	                      WHEN cardinality(t.attachment_ids) > 0 THEN t.attachment_ids
	                      ELSE COALESCE((
	                          SELECT array_agg(DISTINCT src.attachment_id)
	                          FROM ` + quoteIdent(schema) + `.memory_units m2,
	                               unnest(m2.attachment_ids) AS src(attachment_id)
	                          WHERE m2.bank_id = $1 AND m2.id = ANY(t.source_memory_ids)
	                      ), '{}'::text[])
	                  END
	              ) AS s(short_id)
	          )
	          SELECT DISTINCT e.unit_id::text, e.short_id, COALESCE(a.kind, ''),
	                 COALESCE(a.attachment_hash, ''), COALESCE(a.media_type, ''),
	                 COALESCE(a.byte_size, 0), COALESCE(a.filename, '')
	          FROM effective e
	          LEFT JOIN ` + quoteIdent(schema) + `.attachments a
	            ON a.bank_id = $1 AND a.short_id = e.short_id`
	rows, err := s.reader().QueryContext(ctx, query, bankID, ids)
	if err != nil {
		return
	}
	defer func() { _ = rows.Close() }()
	byUnit := map[string][]RecallAttachment{}
	for rows.Next() {
		var unitID, shortID, kind, hash, mediaType, filename string
		var byteSize int64
		if err := rows.Scan(&unitID, &shortID, &kind, &hash, &mediaType, &byteSize, &filename); err != nil {
			return
		}
		byUnit[unitID] = append(byUnit[unitID], recallAttachment(bankID, shortID, kind, hash, mediaType, byteSize, filename))
	}
	for index := range matches {
		if attachments := byUnit[matches[index].ID]; len(attachments) > 0 {
			matches[index].Attachments = attachments
		}
	}
}

// recallAttachment renders one attachment side value for the recall envelope.
func recallAttachment(bankID, shortID, kind, hash, mediaType string, byteSize int64, filename string) RecallAttachment {
	return RecallAttachment{
		ID:        shortID,
		Kind:      kind,
		URI:       bankAttachmentPath(bankID, shortID),
		Hash:      hash,
		MediaType: mediaType,
		ByteSize:  byteSize,
		Filename:  filename,
	}
}

// bankAttachmentPath is the public URL of one retained attachment, matching the
// route that serves its bytes.
func bankAttachmentPath(bankID, attachmentID string) string {
	return "/v1/default/banks/" + bankID + "/attachments/" + attachmentID
}

// PutAttachment records one document's copy of an attachment. The primary key
// is the document plus the content hash, so a conflict means this document
// already holds identical bytes and there is nothing to update; two concurrent
// retains of the same document therefore make the loser a no-op rather than a
// deadlock-prone write.
//
// The conflict target is deliberately the primary key and NOT
// (bank_id, document_id, short_id): a short-id clash between two *different*
// attachments in one document must not be swallowed, or the second attachment
// would go unrecorded and its placeholder would resolve to the first. The
// unique index over the short id therefore raises, and the retain fails where
// an operator can see it.
func (s *PostgresStore) PutAttachment(ctx context.Context, attachment Attachment) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	query := `INSERT INTO ` + quoteIdent(schema) + `.attachments
	          (bank_id, document_id, attachment_hash, short_id, media_type, byte_size, storage_key, kind, filename)
	          VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
	          ON CONFLICT (bank_id, document_id, attachment_hash) DO NOTHING`
	_, err := s.db.ExecContext(ctx, query,
		attachment.BankID, attachment.DocumentID, attachment.AttachmentHash, attachment.ShortID,
		attachment.MediaType, attachment.ByteSize, attachment.StorageKey, attachment.Kind,
		nullString(attachment.Filename))
	return err
}

// ListAttachments returns the attachments a document carries, ordered by
// creation so provenance resolves deterministically.
func (s *PostgresStore) ListAttachments(ctx context.Context, bankID, documentID string) ([]Attachment, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []Attachment
		return zero0, schemaErr
	}
	query := `SELECT bank_id, document_id, attachment_hash, short_id, media_type, byte_size,
	                 storage_key, kind, filename
	          FROM ` + quoteIdent(schema) + `.attachments
	          WHERE bank_id = $1 AND document_id = $2
	          ORDER BY created_at, attachment_hash`
	rows, err := s.reader().QueryContext(ctx, query, bankID, documentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var attachments []Attachment
	for rows.Next() {
		var attachment Attachment
		var filename sql.NullString
		if err := rows.Scan(&attachment.BankID, &attachment.DocumentID, &attachment.AttachmentHash,
			&attachment.ShortID, &attachment.MediaType, &attachment.ByteSize, &attachment.StorageKey,
			&attachment.Kind, &filename); err != nil {
			return nil, err
		}
		attachment.Filename = filename.String
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

// ChunkAttachments returns each chunk's attachments, keyed by chunk id: the
// attachments the chunk's own text references, in first-appearance order.
//
// Provenance is the chunk text, not the units drawn from it: a chunk's text is
// where the inline placeholder stands, so an attachment is listed on the chunk
// that carries it whether or not any fact was extracted from that part of the
// text (and never on a sibling chunk that does not).
func (s *PostgresStore) ChunkAttachments(ctx context.Context, bankID, documentID string) (map[string][]Attachment, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 map[string][]Attachment
		return zero0, schemaErr
	}
	rows, err := s.reader().QueryContext(ctx,
		`SELECT chunk_id, COALESCE(chunk_text,'') FROM `+quoteIdent(schema)+`.chunks
		 WHERE bank_id = $1 AND document_id = $2 ORDER BY chunk_index ASC`, bankID, documentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	type chunkText struct {
		id   string
		text string
	}
	chunks := make([]chunkText, 0)
	for rows.Next() {
		var chunk chunkText
		if err := rows.Scan(&chunk.id, &chunk.text); err != nil {
			return nil, err
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	byShortID, err := s.attachmentsByShortIDsInDocument(ctx, bankID, documentID)
	if err != nil {
		return nil, err
	}
	byChunk := map[string][]Attachment{}
	for _, chunk := range chunks {
		seen := map[string]bool{}
		for _, shortID := range referencedAttachmentIDs(chunk.text) {
			if seen[shortID] {
				continue
			}
			seen[shortID] = true
			if attachment, ok := byShortID[shortID]; ok {
				byChunk[chunk.id] = append(byChunk[chunk.id], attachment)
			}
		}
	}
	return byChunk, nil
}

// attachmentsByShortIDsInDocument resolves a document's attachment rows, keyed
// by short id, so a chunk's placeholders map to that document's own rows.
func (s *PostgresStore) attachmentsByShortIDsInDocument(ctx context.Context, bankID, documentID string) (map[string]Attachment, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 map[string]Attachment
		return zero0, schemaErr
	}
	query := `SELECT bank_id, document_id, attachment_hash, short_id, media_type, byte_size,
	                 storage_key, kind, filename
	          FROM ` + quoteIdent(schema) + `.attachments
	          WHERE bank_id = $1 AND document_id = $2`
	rows, err := s.reader().QueryContext(ctx, query, bankID, documentID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	resolved := map[string]Attachment{}
	for rows.Next() {
		var attachment Attachment
		var filename sql.NullString
		if err := rows.Scan(&attachment.BankID, &attachment.DocumentID, &attachment.AttachmentHash,
			&attachment.ShortID, &attachment.MediaType, &attachment.ByteSize, &attachment.StorageKey,
			&attachment.Kind, &filename); err != nil {
			return nil, err
		}
		attachment.Filename = filename.String
		resolved[attachment.ShortID] = attachment
	}
	return resolved, rows.Err()
}

// GetAttachment returns one attachment by its short id.
//
// The short id is what a placeholder token and an attachment URL carry, so it is
// the only handle a reader has; the row is where the storage key lives, which is
// what the bytes are actually read by.
func (s *PostgresStore) GetAttachment(ctx context.Context, bankID, attachmentID string) (Attachment, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Attachment
		return zero0, schemaErr
	}
	query := `SELECT bank_id, document_id, attachment_hash, short_id, media_type, byte_size,
	                 storage_key, kind, filename
	          FROM ` + quoteIdent(schema) + `.attachments
	          WHERE bank_id = $1 AND short_id = $2`
	var attachment Attachment
	var filename sql.NullString
	err := s.reader().QueryRowContext(ctx, query, bankID, attachmentID).Scan(&attachment.BankID, &attachment.DocumentID,
		&attachment.AttachmentHash, &attachment.ShortID, &attachment.MediaType, &attachment.ByteSize,
		&attachment.StorageKey, &attachment.Kind, &filename)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, ErrAttachmentNotFound
	}
	if err != nil {
		return Attachment{}, err
	}
	attachment.Filename = filename.String
	return attachment, nil
}
