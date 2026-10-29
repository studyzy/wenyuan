package app

import (
	"strings"
	"testing"
	"time"
)

// The event body is the bytes a receiver verifies a signature over, so the
// data object is rendered the way the reference's model is: members in
// declaration order, with every unset member absent rather than null
// (`model_dump_json(exclude_none=True)`).
func TestWebhookEventBodyExcludesUnsetDataMembers(t *testing.T) {
	at := time.Date(2026, 10, 17, 20, 27, 46, 609832000, time.UTC)
	body, err := marshalWebhookEvent("memory_defense.triggered", "b1", "", "", "redact", at, map[string]any{
		"action":         "redact",
		"detector":       "sensitive_data",
		"document_id":    nil,
		"matched_types":  []string{"github_token"},
		"message":        "Sensitive data pattern matched: github_token",
		"severity":       nil,
		"api_key_name":   nil,
		"hits":           []map[string]any{{"detector": "github_token", "preview": "ghp_...6789"}},
		"memory_unit_id": nil,
		"receipt_uri":    nil,
	})
	if err != nil {
		t.Fatal(err)
	}
	// The order and the omissions are the reference's own output for this
	// decision, read off a live delivery.
	want := `{"event":"memory_defense.triggered","bank_id":"b1","operation_id":"","status":"redact",` +
		`"timestamp":"2026-10-17T20:27:46.609832Z","data":{"action":"redact","detector":"sensitive_data",` +
		`"matched_types":["github_token"],"message":"Sensitive data pattern matched: github_token",` +
		`"hits":[{"detector":"github_token","preview":"ghp_...6789"}]}}`
	if string(body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", body, want)
	}
}

// The retain event's data is the three members the reference declares, in that
// order, and an item that contributed nothing leaves an empty object.
func TestWebhookEventBodyRetainData(t *testing.T) {
	at := time.Date(2026, 10, 17, 19, 40, 36, 583053000, time.UTC)
	body, err := marshalWebhookEvent("retain.completed", "b1", "f81acee434cb4c2fab83181b769dc4e3", "", "completed", at,
		map[string]any{"memory_unit_count": 1, "document_id": "doc-a", "tags": []string{"a", "b"}})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"event":"retain.completed","bank_id":"b1","operation_id":"f81acee434cb4c2fab83181b769dc4e3",` +
		`"status":"completed","timestamp":"2026-10-17T19:40:36.583053Z",` +
		`"data":{"document_id":"doc-a","tags":["a","b"],"memory_unit_count":1}}`
	if string(body) != want {
		t.Fatalf("body =\n%s\nwant\n%s", body, want)
	}

	empty, err := marshalWebhookEvent("retain.completed", "b1", "f81acee434cb4c2fab83181b769dc4e3", "", "completed", at,
		map[string]any{"document_id": nil, "tags": nil, "memory_unit_count": nil})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(string(empty), `"data":{}}`) {
		t.Fatalf("an event with nothing to report must carry an empty data object: %s", empty)
	}
}

// A member the declaration order does not name is still emitted, after the
// declared ones, so a payload a caller adds is never silently lost.
func TestWebhookEventBodyKeepsUndeclaredMembers(t *testing.T) {
	body, err := marshalWebhookEvent("consolidation.completed", "b1", "op", "", "completed", time.Now().UTC(),
		map[string]any{"extra": "kept", "observations_deleted": 1, "observations_created": 2})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"data":{"observations_created":2,"observations_deleted":1,"extra":"kept"}`) {
		t.Fatalf("body = %s", body)
	}
}

// The body is rendered with no HTML escaping, so a receiver that re-serializes
// the parsed body reproduces these bytes and the signature still verifies.
func TestWebhookEventBodyDoesNotEscapeHTML(t *testing.T) {
	body, err := marshalWebhookEvent("memory_defense.triggered", "b1", "", "", "redact", time.Now().UTC(),
		map[string]any{"action": "redact", "message": "<b>a & b</b>"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"message":"<b>a & b</b>"`) {
		t.Fatalf("body = %s", body)
	}
}
