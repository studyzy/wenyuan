package storage

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// ListWebhooks returns a bank's webhook registrations, newest first.
func (s *MemoryStore) ListWebhooks(ctx context.Context, bankID string) ([]Webhook, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Webhook, 0)
	for _, webhook := range s.webhooks {
		if webhook.BankID == bankID {
			out = append(out, cloneWebhook(webhook))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetWebhook returns one webhook by identifier.
func (s *MemoryStore) GetWebhook(ctx context.Context, bankID, webhookID string) (Webhook, error) {
	if err := ctx.Err(); err != nil {
		return Webhook{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	webhook, ok := s.webhooks[bankID+"\x00"+webhookID]
	if !ok {
		return Webhook{}, ErrWebhookNotFound
	}
	return cloneWebhook(webhook), nil
}

// PutWebhook inserts or replaces a webhook.
func (s *MemoryStore) PutWebhook(ctx context.Context, webhook Webhook) (Webhook, error) {
	if err := ctx.Err(); err != nil {
		return Webhook{}, err
	}
	if webhook.ID == "" || webhook.BankID == "" {
		return Webhook{}, ErrInvalidOperationID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Webhook{}, s.writeErr
	}
	if s.webhooks == nil {
		s.webhooks = map[string]Webhook{}
	}
	key := webhook.BankID + "\x00" + webhook.ID
	now := time.Now().UTC()
	if existing, ok := s.webhooks[key]; ok {
		webhook.CreatedAt = existing.CreatedAt
	}
	if webhook.CreatedAt.IsZero() {
		webhook.CreatedAt = now
	}
	webhook.UpdatedAt = now
	s.webhooks[key] = cloneWebhook(webhook)
	return cloneWebhook(webhook), nil
}

// UpdateWebhook applies a partial update.
func (s *MemoryStore) UpdateWebhook(ctx context.Context, bankID, webhookID string, patch WebhookPatch) (Webhook, error) {
	if err := ctx.Err(); err != nil {
		return Webhook{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Webhook{}, s.writeErr
	}
	key := bankID + "\x00" + webhookID
	webhook, ok := s.webhooks[key]
	if !ok {
		return Webhook{}, ErrWebhookNotFound
	}
	if patch.URL != nil {
		webhook.URL = *patch.URL
	}
	if patch.Secret != nil {
		webhook.Secret = *patch.Secret
	} else if patch.ClearSecret {
		webhook.Secret = ""
	}
	if patch.EventTypes != nil {
		webhook.EventTypes = append([]string(nil), (*patch.EventTypes)...)
	}
	if patch.Enabled != nil {
		webhook.Enabled = *patch.Enabled
	}
	if patch.HTTPConfig != nil {
		webhook.HTTPConfig = *patch.HTTPConfig
	}
	webhook.UpdatedAt = time.Now().UTC()
	s.webhooks[key] = cloneWebhook(webhook)
	return cloneWebhook(webhook), nil
}

// DeleteWebhook removes a webhook and its delivery records.
func (s *MemoryStore) DeleteWebhook(ctx context.Context, bankID, webhookID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	key := bankID + "\x00" + webhookID
	if _, ok := s.webhooks[key]; !ok {
		return ErrWebhookNotFound
	}
	delete(s.webhooks, key)
	// The delivery records are operations, and an operation is a historical
	// fact: a deleted webhook's past deliveries stay readable, exactly as the
	// database backend leaves them.
	return nil
}

// ListWebhookDeliveries returns one page of a webhook's delivery records.
//
// A delivery is an operation of type webhook_delivery, which is where the
// reference records one: the endpoint, the event and the outcome all live on
// that row rather than in a table of their own. Deriving the page from the
// operations keeps the two backends answering the same question the same way.
func (s *MemoryStore) ListWebhookDeliveries(ctx context.Context, bankID, webhookID string, limit int, cursor string) (WebhookDeliveryPage, error) {
	if err := ctx.Err(); err != nil {
		return WebhookDeliveryPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]WebhookDelivery, 0)
	for _, op := range s.operations {
		if op.BankID != bankID || op.OperationType != OperationTypeWebhookDelivery {
			continue
		}
		delivery := deliveryFromOperation(op)
		if webhookID != "" && delivery.WebhookID != webhookID {
			continue
		}
		matched = append(matched, delivery)
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].CreatedAt.Equal(matched[j].CreatedAt) {
			return matched[i].CreatedAt.After(matched[j].CreatedAt)
		}
		return matched[i].ID < matched[j].ID
	})
	if cursor != "" {
		after, err := parseDeliveryCursor(cursor)
		if err != nil {
			return WebhookDeliveryPage{}, errInvalidDeliveryCursor(cursor)
		}
		filtered := matched[:0]
		for _, delivery := range matched {
			if delivery.CreatedAt.Before(after) {
				filtered = append(filtered, delivery)
			}
		}
		matched = filtered
	}
	if limit <= 0 {
		limit = 50
	}
	hasMore := len(matched) > limit
	if hasMore {
		matched = matched[:limit]
	}
	return WebhookDeliveryPage{Deliveries: matched, HasMore: hasMore}, nil
}

// errInvalidDeliveryCursor is the error a malformed delivery cursor produces.
//
// The reference does not pre-validate the cursor: it hands the value to the
// driver, which rejects a non-datetime and surfaces a 500 whose detail is the
// driver's own message. Both backends raise this same text so the delivery
// route answers a malformed cursor identically whichever store is behind it.
func errInvalidDeliveryCursor(cursor string) error {
	return fmt.Errorf(
		"invalid input for query argument $3: '%s' (expected a datetime.date or datetime.datetime instance, got 'str')",
		cursor)
}

// parseDeliveryCursor reads a delivery pagination cursor.
//
// The reference accepts no cursor at all. Its handler declares the parameter as
// a bare `str` and passes it straight to `created_at < $3::timestamptz`
// (api/http.py:10006-10060, ops_postgresql.py:1410-1441), so asyncpg is asked
// to bind a `str` for a timestamptz column and refuses every value — including
// the `next_cursor` the reference's own response just minted
// (api/http.py:10041), which makes its cursor paging impossible past the first
// page. That is the observable contract, so this service answers the same way:
// any non-empty cursor is refused with the adapter's error.
func parseDeliveryCursor(cursor string) (time.Time, error) {
	_ = cursor
	return time.Time{}, errInvalidDeliveryCursor(cursor)
}

// parseDeliveryTimestamp reads a stored delivery timestamp, the cursor's
// *source* rather than the cursor itself. It stays tolerant of the reference's
// PostgreSQL text form and RFC 3339 for the rows already on disk.
func parseDeliveryTimestamp(cursor string) (time.Time, error) {
	layouts := []string{
		time.RFC3339Nano,
		time.RFC3339,
		"2006-01-02 15:04:05.999999-07",
		"2006-01-02 15:04:05-07",
	}
	var err error
	for _, layout := range layouts {
		var parsed time.Time
		if parsed, err = time.Parse(layout, cursor); err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, err
}

// cloneWebhook copies the mutable fields.
func cloneWebhook(webhook Webhook) Webhook {
	out := webhook
	out.EventTypes = append([]string(nil), webhook.EventTypes...)
	out.HTTPConfig = copyAnyMap(webhook.HTTPConfig)
	return out
}

// deliveryFromOperation renders one webhook-delivery operation as a delivery
// record, the same projection the Postgres backend reads from its column set.
func deliveryFromOperation(op Operation) WebhookDelivery {
	delivery := WebhookDelivery{
		ID:          op.OperationID,
		BankID:      op.BankID,
		Status:      string(op.Status),
		Attempts:    op.RetryCount + 1,
		LastError:   op.Error,
		NextRetryAt: op.NextRetryAt,
		CreatedAt:   op.CreatedAt,
		UpdatedAt:   op.UpdatedAt,
	}
	if webhookID, ok := op.Payload["webhook_id"].(string); ok {
		delivery.WebhookID = webhookID
	}
	if eventType, ok := op.Payload["event_type"].(string); ok {
		delivery.EventType = eventType
	}
	if url, ok := op.Payload["url"].(string); ok {
		delivery.URL = url
	}
	if op.ResultMetadata != nil {
		switch code := op.ResultMetadata["last_status_code"].(type) {
		case int:
			delivery.LastResponseStatus = &code
		case float64:
			intCode := int(code)
			delivery.LastResponseStatus = &intCode
		}
		// The response body is recorded only when the deployment exposed it, so
		// a stored value is one the API may return; an absent key stays nil.
		if body, ok := op.ResultMetadata["last_response_body"].(string); ok {
			delivery.LastResponseBody = &body
		}
		if attempt, ok := op.ResultMetadata["last_attempt_at"].(string); ok {
			if parsed, err := parseDeliveryTimestamp(attempt); err == nil {
				delivery.LastAttemptAt = &parsed
			}
		}
	}
	return delivery
}
