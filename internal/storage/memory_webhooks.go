package storage

import (
	"context"
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
			return out[i].CreatedAt.After(out[j].CreatedAt)
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
		if after, err := time.Parse(time.RFC3339Nano, cursor); err == nil {
			filtered := matched[:0]
			for _, delivery := range matched {
				if delivery.CreatedAt.Before(after) {
					filtered = append(filtered, delivery)
				}
			}
			matched = filtered
		}
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
	}
	return delivery
}
