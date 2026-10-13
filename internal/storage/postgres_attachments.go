package storage

import (
	"context"
	"database/sql"
)

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
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.attachments
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
	query := `SELECT bank_id, document_id, attachment_hash, short_id, media_type, byte_size,
	                 storage_key, kind, filename
	          FROM ` + quoteIdent(s.schema) + `.attachments
	          WHERE bank_id = $1 AND document_id = $2
	          ORDER BY created_at, attachment_hash`
	rows, err := s.db.QueryContext(ctx, query, bankID, documentID)
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
