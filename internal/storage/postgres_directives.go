package storage

import (
	"context"
	"database/sql"
	"errors"
)

// directiveSelect is the shared projection so every read returns the same
// column order the scanner expects.
const directiveSelect = `id, bank_id, name, content, COALESCE(priority,0), COALESCE(is_active,true),
       COALESCE(tags, '{}')::text, created_at, updated_at`

// ListDirectives returns the directives of one bank, highest priority first so
// the order a reflection would follow them is the order they are returned in.
func (s *PostgresStore) ListDirectives(ctx context.Context, bankID string) ([]Directive, error) {
	query := `SELECT ` + directiveSelect + ` FROM ` + quoteIdent(s.schema) + `.directives
	          WHERE bank_id = $1
	          ORDER BY priority DESC, created_at DESC, id ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Directive, 0)
	for rows.Next() {
		directive, err := scanDirective(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, directive)
	}
	return out, rows.Err()
}

// GetDirective returns one directive by identifier within a bank.
func (s *PostgresStore) GetDirective(ctx context.Context, bankID, id string) (Directive, error) {
	query := `SELECT ` + directiveSelect + ` FROM ` + quoteIdent(s.schema) + `.directives
	          WHERE bank_id = $1 AND id = $2`
	directive, err := scanDirective(s.db.QueryRowContext(ctx, query, bankID, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Directive{}, ErrDirectiveNotFound
		}
		return Directive{}, err
	}
	return directive, nil
}

// PutDirective inserts or replaces a directive.
func (s *PostgresStore) PutDirective(ctx context.Context, directive Directive) (Directive, error) {
	if directive.ID == "" || directive.BankID == "" {
		return Directive{}, ErrInvalidOperationID
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.directives
	          (id, bank_id, name, content, priority, is_active, tags, created_at, updated_at)
	          VALUES ($1,$2,$3,$4,$5,$6,$7, now(), now())
	          ON CONFLICT (id) DO UPDATE SET
	              name = EXCLUDED.name, content = EXCLUDED.content, priority = EXCLUDED.priority,
	              is_active = EXCLUDED.is_active, tags = EXCLUDED.tags, updated_at = now()`
	if _, err := s.db.ExecContext(ctx, query, directive.ID, directive.BankID, directive.Name,
		directive.Content, directive.Priority, directive.IsActive, normalizeTags(directive.Tags)); err != nil {
		return Directive{}, err
	}
	return s.GetDirective(ctx, directive.BankID, directive.ID)
}

// UpdateDirective applies a partial update and returns the stored value.
func (s *PostgresStore) UpdateDirective(ctx context.Context, bankID, id string, patch DirectivePatch) (Directive, error) {
	current, err := s.GetDirective(ctx, bankID, id)
	if err != nil {
		return Directive{}, err
	}
	if patch.Name != nil {
		current.Name = *patch.Name
	}
	if patch.Content != nil {
		current.Content = *patch.Content
	}
	if patch.Priority != nil {
		current.Priority = *patch.Priority
	}
	if patch.IsActive != nil {
		current.IsActive = *patch.IsActive
	}
	if patch.Tags != nil {
		current.Tags = append([]string(nil), (*patch.Tags)...)
	}
	return s.PutDirective(ctx, current)
}

// DeleteDirective removes a directive.
func (s *PostgresStore) DeleteDirective(ctx context.Context, bankID, id string) error {
	query := `DELETE FROM ` + quoteIdent(s.schema) + `.directives WHERE bank_id = $1 AND id = $2`
	result, err := s.db.ExecContext(ctx, query, bankID, id)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrDirectiveNotFound
	}
	return nil
}

// scanDirective reads the shared projection.
func scanDirective(row rowScanner) (Directive, error) {
	var directive Directive
	var tags string
	if err := row.Scan(&directive.ID, &directive.BankID, &directive.Name, &directive.Content,
		&directive.Priority, &directive.IsActive, &tags, &directive.CreatedAt, &directive.UpdatedAt); err != nil {
		return Directive{}, err
	}
	directive.Tags = decodeTextArray(tags)
	return directive, nil
}
