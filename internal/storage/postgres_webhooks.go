package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// webhookSelect is the shared projection for webhook registrations. The event
// type array is rendered to text so it can be scanned without a driver-specific
// array codec, and decoded by scanWebhook.
const webhookSelect = `id, bank_id, url, COALESCE(secret,''), COALESCE(event_types, '{}')::text,
       COALESCE(enabled, true), COALESCE(http_config, '{}'::jsonb), created_at, updated_at`

// ListWebhooks returns a bank's webhook registrations, newest first.
func (s *PostgresStore) ListWebhooks(ctx context.Context, bankID string) ([]Webhook, error) {
	query := `SELECT ` + webhookSelect + ` FROM ` + quoteIdent(s.schema) + `.webhooks
	          WHERE bank_id = $1 ORDER BY created_at DESC, id ASC`
	rows, err := s.db.QueryContext(ctx, query, bankID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Webhook, 0)
	for rows.Next() {
		webhook, err := scanWebhook(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, webhook)
	}
	return out, rows.Err()
}

// GetWebhook returns one webhook by identifier.
func (s *PostgresStore) GetWebhook(ctx context.Context, bankID, webhookID string) (Webhook, error) {
	query := `SELECT ` + webhookSelect + ` FROM ` + quoteIdent(s.schema) + `.webhooks
	          WHERE bank_id = $1 AND id = $2`
	webhook, err := scanWebhook(s.db.QueryRowContext(ctx, query, bankID, webhookID))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Webhook{}, ErrWebhookNotFound
		}
		return Webhook{}, err
	}
	return webhook, nil
}

// PutWebhook inserts or replaces a webhook.
func (s *PostgresStore) PutWebhook(ctx context.Context, webhook Webhook) (Webhook, error) {
	if webhook.ID == "" || webhook.BankID == "" {
		return Webhook{}, ErrInvalidOperationID
	}
	config, err := jsonColumn(webhook.HTTPConfig)
	if err != nil {
		return Webhook{}, err
	}
	query := `INSERT INTO ` + quoteIdent(s.schema) + `.webhooks
	          (id, bank_id, url, secret, event_types, enabled, http_config, created_at, updated_at)
	          VALUES ($1,$2,$3,NULLIF($4,''),$5,$6,$7::jsonb, now(), now())
	          ON CONFLICT (id) DO UPDATE SET
	              url = EXCLUDED.url, secret = EXCLUDED.secret,
	              event_types = EXCLUDED.event_types, enabled = EXCLUDED.enabled,
	              http_config = EXCLUDED.http_config, updated_at = now()`
	if _, err := s.db.ExecContext(ctx, query, webhook.ID, webhook.BankID, webhook.URL,
		webhook.Secret, normalizeTags(webhook.EventTypes), webhook.Enabled, config); err != nil {
		return Webhook{}, err
	}
	return s.GetWebhook(ctx, webhook.BankID, webhook.ID)
}

// UpdateWebhook applies a partial update.
func (s *PostgresStore) UpdateWebhook(ctx context.Context, bankID, webhookID string, patch WebhookPatch) (Webhook, error) {
	current, err := s.GetWebhook(ctx, bankID, webhookID)
	if err != nil {
		return Webhook{}, err
	}
	if patch.URL != nil {
		current.URL = *patch.URL
	}
	if patch.Secret != nil {
		current.Secret = *patch.Secret
	}
	if patch.EventTypes != nil {
		current.EventTypes = append([]string(nil), (*patch.EventTypes)...)
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	return s.PutWebhook(ctx, current)
}

// DeleteWebhook removes a webhook and its delivery records.
func (s *PostgresStore) DeleteWebhook(ctx context.Context, bankID, webhookID string) error {
	qualified := quoteIdent(s.schema)
	// The delivery records reference the webhook, so they go first.
	if _, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.webhook_deliveries WHERE bank_id = $1 AND webhook_id = $2`,
		bankID, webhookID); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM `+qualified+`.webhooks WHERE bank_id = $1 AND id = $2`, bankID, webhookID)
	if err != nil {
		return err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrWebhookNotFound
	}
	return nil
}

// ListWebhookDeliveries returns one page of a webhook's delivery records.
func (s *PostgresStore) ListWebhookDeliveries(ctx context.Context, bankID, webhookID string, limit, offset int) (WebhookDeliveryPage, error) {
	filter := `bank_id = $1 AND ($2 = '' OR webhook_id = $2)`
	qualified := quoteIdent(s.schema)
	var total int
	if err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM `+qualified+`.webhook_deliveries WHERE `+filter,
		bankID, webhookID).Scan(&total); err != nil {
		return WebhookDeliveryPage{}, err
	}
	limit = clampLimit(limit, 50)
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, webhook_id, bank_id, event_type, status, status_code, COALESCE(error,''), attempt, delivered_at
		 FROM `+qualified+`.webhook_deliveries WHERE `+filter+`
		 ORDER BY delivered_at DESC, id ASC LIMIT $3 OFFSET $4`,
		bankID, webhookID, limit, offset)
	if err != nil {
		return WebhookDeliveryPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]WebhookDelivery, 0)
	for rows.Next() {
		var delivery WebhookDelivery
		var statusCode sql.NullInt64
		if err := rows.Scan(&delivery.ID, &delivery.WebhookID, &delivery.BankID,
			&delivery.EventType, &delivery.Status, &statusCode, &delivery.Error,
			&delivery.Attempt, &delivery.DeliveredAt); err != nil {
			return WebhookDeliveryPage{}, err
		}
		delivery.StatusCode = nullIntPtr(statusCode)
		out = append(out, delivery)
	}
	return WebhookDeliveryPage{Deliveries: out, Total: total, Limit: limit, Offset: offset}, rows.Err()
}

// scanWebhook reads the shared projection.
func scanWebhook(row rowScanner) (Webhook, error) {
	var webhook Webhook
	var eventTypes string
	var config []byte
	if err := row.Scan(&webhook.ID, &webhook.BankID, &webhook.URL, &webhook.Secret,
		&eventTypes, &webhook.Enabled, &config, &webhook.CreatedAt, &webhook.UpdatedAt); err != nil {
		return Webhook{}, err
	}
	webhook.EventTypes = decodeTextArray(eventTypes)
	webhook.HTTPConfig = decodeJSONMap(config)
	return webhook, nil
}

// ensure the json import is used by the config encoding above.
var _ = json.Marshal
