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
	// The delivery records describe a webhook that no longer exists.
	kept := s.webhookDeliveries[:0]
	for _, delivery := range s.webhookDeliveries {
		if delivery.WebhookID == webhookID {
			continue
		}
		kept = append(kept, delivery)
	}
	s.webhookDeliveries = kept
	return nil
}

// ListWebhookDeliveries returns one page of a webhook's delivery records.
func (s *MemoryStore) ListWebhookDeliveries(ctx context.Context, bankID, webhookID string, limit, offset int) (WebhookDeliveryPage, error) {
	if err := ctx.Err(); err != nil {
		return WebhookDeliveryPage{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	matched := make([]WebhookDelivery, 0)
	for _, delivery := range s.webhookDeliveries {
		if delivery.BankID == bankID && (webhookID == "" || delivery.WebhookID == webhookID) {
			matched = append(matched, delivery)
		}
	}
	sort.SliceStable(matched, func(i, j int) bool {
		if !matched[i].DeliveredAt.Equal(matched[j].DeliveredAt) {
			return matched[i].DeliveredAt.After(matched[j].DeliveredAt)
		}
		return matched[i].ID < matched[j].ID
	})
	total := len(matched)
	start, end := pageBounds(total, offset, limit)
	return WebhookDeliveryPage{Deliveries: matched[start:end], Total: total,
		Limit: limit, Offset: offset}, nil
}

// cloneWebhook copies the mutable fields.
func cloneWebhook(webhook Webhook) Webhook {
	out := webhook
	out.EventTypes = append([]string(nil), webhook.EventTypes...)
	out.HTTPConfig = copyAnyMap(webhook.HTTPConfig)
	return out
}
