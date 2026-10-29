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
const webhookSelect = `id::text, bank_id, url, COALESCE(secret,''), COALESCE(event_types, '{}')::text,
       COALESCE(enabled, true), COALESCE(http_config, '{}'::jsonb), created_at, updated_at`

// ListWebhooks returns a bank's webhook registrations, oldest first.
func (s *PostgresStore) ListWebhooks(ctx context.Context, bankID string) ([]Webhook, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 []Webhook
		return zero0, schemaErr
	}
	query := `SELECT ` + webhookSelect + ` FROM ` + quoteIdent(schema) + `.webhooks
	          WHERE bank_id = $1 ORDER BY created_at ASC, id ASC`
	rows, err := s.reader().QueryContext(ctx, query, bankID)
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Webhook
		return zero0, schemaErr
	}
	query := `SELECT ` + webhookSelect + ` FROM ` + quoteIdent(schema) + `.webhooks
	          WHERE bank_id = $1 AND id::text = $2`
	webhook, err := scanWebhook(s.reader().QueryRowContext(ctx, query, bankID, webhookID))
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
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 Webhook
		return zero0, schemaErr
	}
	if webhook.ID == "" || webhook.BankID == "" {
		return Webhook{}, ErrInvalidOperationID
	}
	config, err := jsonColumn(webhook.HTTPConfig)
	if err != nil {
		return Webhook{}, err
	}
	query := `INSERT INTO ` + quoteIdent(schema) + `.webhooks
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
	} else if patch.ClearSecret {
		current.Secret = ""
	}
	if patch.EventTypes != nil {
		current.EventTypes = append([]string(nil), (*patch.EventTypes)...)
	}
	if patch.Enabled != nil {
		current.Enabled = *patch.Enabled
	}
	if patch.HTTPConfig != nil {
		current.HTTPConfig = *patch.HTTPConfig
	}
	return s.PutWebhook(ctx, current)
}

// DeleteWebhook removes a webhook registration. The delivery records are
// operations — historical facts — and are left in place, exactly as the
// reference and the memory backend leave them.
func (s *PostgresStore) DeleteWebhook(ctx context.Context, bankID, webhookID string) error {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		return schemaErr
	}
	result, err := s.db.ExecContext(ctx,
		`DELETE FROM `+quoteIdent(schema)+`.webhooks WHERE bank_id = $1 AND id::text = $2`, bankID, webhookID)
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

// ListWebhookDeliveries returns one page of a webhook's delivery records. A
// delivery is an async_operations row of type webhook_delivery, which is where
// the reference records one: the endpoint, the event and the outcome all live
// on that row rather than in a table of their own.
func (s *PostgresStore) ListWebhookDeliveries(ctx context.Context, bankID, webhookID string, limit int, cursor string) (WebhookDeliveryPage, error) {
	schema, schemaErr := s.schemaFor(ctx)
	if schemaErr != nil {
		var zero0 WebhookDeliveryPage
		return zero0, schemaErr
	}
	qualified := quoteIdent(schema)
	filter := `bank_id = $1 AND operation_type = 'webhook_delivery'
	           AND ($2 = '' OR task_payload->>'webhook_id' = $2)`
	args := []any{bankID, webhookID}
	if cursor != "" {
		// The cursor is validated here rather than left to the driver so the
		// rejection matches the reference's own adapter error and the memory
		// backend's, instead of surfacing the driver's schema-specific text.
		if _, err := parseDeliveryCursor(cursor); err != nil {
			return WebhookDeliveryPage{}, errInvalidDeliveryCursor(cursor)
		}
		filter += ` AND created_at < $3::timestamptz`
		args = append(args, cursor)
	}
	limit = clampLimit(limit, 50)
	args = append(args, limit+1) // one extra row tells the caller a page follows
	limitClause := "$" + itoa(len(args))

	rows, err := s.reader().QueryContext(ctx,
		`SELECT operation_id::text,
		        COALESCE(task_payload->>'webhook_id', ''),
		        bank_id,
		        COALESCE(task_payload->>'url', ''),
		        COALESCE(task_payload->>'event_type', ''),
		        status,
		        COALESCE(retry_count, 0) + 1,
		        next_retry_at,
		        COALESCE(error_message, ''),
		        (result_metadata->>'last_status_code')::int,
		        result_metadata->>'last_response_body',
		        result_metadata->>'last_attempt_at',
		        created_at,
		        updated_at
		 FROM `+qualified+`.async_operations WHERE `+filter+`
		 ORDER BY created_at DESC, operation_id ASC LIMIT `+limitClause,
		args...)
	if err != nil {
		return WebhookDeliveryPage{}, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]WebhookDelivery, 0, limit)
	for rows.Next() {
		var delivery WebhookDelivery
		var statusCode sql.NullInt64
		var responseBody, lastAttempt sql.NullString
		var nextRetryAt sql.NullTime
		if err := rows.Scan(&delivery.ID, &delivery.WebhookID, &delivery.BankID, &delivery.URL,
			&delivery.EventType, &delivery.Status, &delivery.Attempts, &nextRetryAt,
			&delivery.LastError, &statusCode, &responseBody, &lastAttempt, &delivery.CreatedAt, &delivery.UpdatedAt); err != nil {
			return WebhookDeliveryPage{}, err
		}
		delivery.LastResponseStatus = nullIntPtr(statusCode)
		// The body is stored only when the deployment exposes it, so a
		// non-null value is one the API is allowed to return.
		if responseBody.Valid {
			value := responseBody.String
			delivery.LastResponseBody = &value
		}
		if lastAttempt.Valid {
			if parsed, err := parseDeliveryTimestamp(lastAttempt.String); err == nil {
				delivery.LastAttemptAt = &parsed
			}
		}
		if nextRetryAt.Valid {
			value := nextRetryAt.Time
			delivery.NextRetryAt = &value
		}
		out = append(out, delivery)
	}
	if err := rows.Err(); err != nil {
		return WebhookDeliveryPage{}, err
	}
	hasMore := len(out) > limit
	if hasMore {
		out = out[:limit]
	}
	return WebhookDeliveryPage{Deliveries: out, HasMore: hasMore}, nil
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
