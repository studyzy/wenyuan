package storage

import (
	"context"
	"strings"
	"testing"
)

// A malformed delivery cursor is rejected by both backends with the same error
// text, which is the driver message the reference surfaces as a 500.
func TestMemoryWebhookDeliveriesRejectMalformedCursor(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.PutWebhook(ctx, Webhook{
		ID: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", BankID: "b1",
		URL: "https://example.test/hook", EventTypes: []string{"x"}, Enabled: true,
	}); err != nil {
		t.Fatalf("put webhook: %v", err)
	}
	_, err := store.ListWebhookDeliveries(ctx, "b1", "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", 50, "not-a-timestamp")
	if err == nil {
		t.Fatal("a malformed cursor must be rejected")
	}
	want := "invalid input for query argument $3: 'not-a-timestamp' (expected a datetime.date or datetime.datetime instance, got 'str')"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("cursor error = %q, want it to contain %q", err.Error(), want)
	}
}
