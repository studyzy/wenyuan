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

// Every delivery cursor is refused, not just a malformed one.
//
// The reference declares the parameter as a bare `str` and binds it straight to
// `created_at < $3::timestamptz` (api/http.py:10006-10060,
// engine/memories/pg/ops_postgresql.py:1410-1441), so asyncpg rejects every
// value — including the `next_cursor` its own response minted
// (api/http.py:10041). A client therefore cannot page past the first page on
// either service; accepting a well-formed cursor here would have been a
// divergence that silently returned rows the reference never delivers.
func TestDeliveryCursorIsRefusedInEveryAcceptedForm(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	const webhookID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := store.PutWebhook(ctx, Webhook{
		ID: webhookID, BankID: "b1",
		URL: "https://example.test/hook", EventTypes: []string{"x"}, Enabled: true,
	}); err != nil {
		t.Fatalf("put webhook: %v", err)
	}
	for _, cursor := range []string{
		"2026-10-26T00:00:00Z",
		"2026-10-26T00:00:00+00:00",
		"2026-10-26 06:00:00.123456+00", // the reference's own next_cursor form
		"2026-10-26T00:00:00.123Z",
		"2026-10-26T00:00:00",
		"2026-10-26",
		"notadate",
	} {
		t.Run(cursor, func(t *testing.T) {
			_, err := store.ListWebhookDeliveries(ctx, "b1", webhookID, 50, cursor)
			if err == nil {
				t.Fatalf("cursor %q was accepted; the reference refuses every cursor", cursor)
			}
			if !strings.Contains(err.Error(), "invalid input for query argument $3") {
				t.Fatalf("cursor %q error = %q, want the reference's adapter refusal", cursor, err.Error())
			}
		})
	}
}

// An empty cursor is no cursor: the first page is served.
func TestDeliveryWithoutCursorServesTheFirstPage(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore("public")
	const webhookID = "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	if _, err := store.PutWebhook(ctx, Webhook{
		ID: webhookID, BankID: "b1",
		URL: "https://example.test/hook", EventTypes: []string{"x"}, Enabled: true,
	}); err != nil {
		t.Fatalf("put webhook: %v", err)
	}
	if _, err := store.ListWebhookDeliveries(ctx, "b1", webhookID, 50, ""); err != nil {
		t.Fatalf("the first page must be served without a cursor: %v", err)
	}
}
