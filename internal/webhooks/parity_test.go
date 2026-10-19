package webhooks

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// createBody returns the webhook the route created for a body, failing on a
// non-201.
func createBody(t *testing.T, mux *http.ServeMux, body string) Webhook {
	t.Helper()
	rec := send(mux, http.MethodPost, "/v1/default/banks/b1/webhooks", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create %s: %d (body %s)", body, rec.Code, rec.Body.String())
	}
	var webhook Webhook
	if err := json.Unmarshal(rec.Body.Bytes(), &webhook); err != nil {
		t.Fatal(err)
	}
	return webhook
}

// The create route answers the request model's 422 items for a rejected body.
func TestWebhookCreateValidationEnvelope(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"
	cases := []struct {
		body string
		kind string
		loc  []any
	}{
		{`{}`, "missing", []any{"body", "url"}},
		{`{"url":null}`, "string_type", []any{"body", "url"}},
		{`{"url":123}`, "string_type", []any{"body", "url"}},
		{`{"url":"https://example.test/h","event_types":null}`, "list_type", []any{"body", "event_types"}},
		{`{"url":"https://example.test/h","enabled":null}`, "bool_type", []any{"body", "enabled"}},
		{`{"url":"https://example.test/h","enabled":"maybe"}`, "bool_parsing", []any{"body", "enabled"}},
		{`{"url":"https://example.test/h","http_config":null}`, "model_attributes_type", []any{"body", "http_config"}},
	}
	for _, tc := range cases {
		rec := send(mux, http.MethodPost, base, tc.body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: status = %d, want 422 (body %s)", tc.body, rec.Code, rec.Body.String())
		}
		var envelope struct {
			Detail []struct {
				Type string `json:"type"`
				Loc  []any  `json:"loc"`
			} `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if len(envelope.Detail) != 1 || envelope.Detail[0].Type != tc.kind {
			t.Fatalf("%s: detail = %+v, want type %s", tc.body, envelope.Detail, tc.kind)
		}
	}
}

// An explicit empty event list is stored as an empty list, not the default; an
// unknown event name is accepted verbatim.
func TestWebhookEventTypesVerbatim(t *testing.T) {
	mux, _ := newServer(t)
	empty := createBody(t, mux, `{"url":"https://example.test/a","event_types":[]}`)
	if empty.EventTypes == nil || len(empty.EventTypes) != 0 {
		t.Fatalf("event_types = %v, want empty list", empty.EventTypes)
	}
	unknown := createBody(t, mux, `{"url":"https://example.test/b","event_types":["custom.event"]}`)
	if len(unknown.EventTypes) != 1 || unknown.EventTypes[0] != "custom.event" {
		t.Fatalf("event_types = %v", unknown.EventTypes)
	}
}

// The URL guard refuses the reserved and carrier-grade-NAT ranges the reference
// blocks, plus the legacy numeric loopback spellings, and accepts credentials.
func TestWebhookGuardBlockedRanges(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/webhooks"
	for _, url := range []string{
		"https://100.64.0.1/h", "https://240.0.0.1/h", "http://2130706433/h", "http://127.1/h",
	} {
		rec := send(mux, http.MethodPost, base, `{"url":"`+url+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400 (body %s)", url, rec.Code, rec.Body.String())
		}
	}
	if rec := send(mux, http.MethodPost, base, `{"url":"https://user:pass@example.test/h"}`); rec.Code != http.StatusCreated {
		t.Fatalf("credentials must be accepted: %d (body %s)", rec.Code, rec.Body.String())
	}
}

// The list route defaults to a 100-item page, honours limit=0 as an empty page,
// and orders oldest first.
func TestWebhookListPagingAndOrder(t *testing.T) {
	mux, _ := newServer(t)
	first := createBody(t, mux, `{"url":"https://example.test/first"}`)
	second := createBody(t, mux, `{"url":"https://example.test/second"}`)
	third := createBody(t, mux, `{"url":"https://example.test/third"}`)

	var page struct {
		Items  []Webhook `json:"items"`
		Total  int       `json:"total"`
		Limit  int       `json:"limit"`
		Offset int       `json:"offset"`
	}
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/webhooks", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	if page.Limit != 100 || page.Total != 3 {
		t.Fatalf("default page = %+v", page)
	}
	if len(page.Items) != 3 || page.Items[0].ID != first.ID || page.Items[1].ID != second.ID || page.Items[2].ID != third.ID {
		t.Fatalf("oldest-first order expected, got %+v", page.Items)
	}

	var empty struct {
		Items []Webhook `json:"items"`
		Limit int       `json:"limit"`
	}
	rec = send(mux, http.MethodGet, "/v1/default/banks/b1/webhooks?limit=0", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &empty); err != nil {
		t.Fatal(err)
	}
	if len(empty.Items) != 0 || empty.Limit != 0 {
		t.Fatalf("limit=0 page = %+v", empty)
	}

	if rec := send(mux, http.MethodGet, "/v1/default/banks/b1/webhooks?limit=1001", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=1001: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodGet, "/v1/default/banks/b1/webhooks?offset=-1", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("offset=-1: %d, want 422", rec.Code)
	}
}

// The delete acknowledgement keeps nulls, matching the reference.
func TestWebhookDeleteResponse(t *testing.T) {
	mux, _ := newServer(t)
	webhook := createBody(t, mux, `{"url":"https://example.test/del"}`)
	rec := send(mux, http.MethodDelete, "/v1/default/banks/b1/webhooks/"+webhook.ID, "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["success"] != true {
		t.Fatalf("body = %v", body)
	}
	if value, present := body["message"]; !present || value != nil {
		t.Fatalf("message must be present and null: %v", body)
	}
	if value, present := body["deleted_count"]; !present || value != nil {
		t.Fatalf("deleted_count must be present and null: %v", body)
	}
}

// A patch that names no recognised field is refused; an explicit null secret
// clears the stored secret.
func TestWebhookUpdateSemantics(t *testing.T) {
	mux, store := newServer(t)
	webhook := createBody(t, mux, `{"url":"https://example.test/patch","secret":"s3cret"}`)

	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/webhooks/"+webhook.ID, `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("empty patch: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/webhooks/"+webhook.ID, `{"bogus":1}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown-only patch: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/webhooks/not-a-uuid", `{"enabled":false}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed id: %d, want 422", rec.Code)
	}

	if rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/webhooks/"+webhook.ID, `{"secret":null}`); rec.Code != http.StatusOK {
		t.Fatalf("clear secret: %d (body %s)", rec.Code, rec.Body.String())
	}
	stored, err := store.GetWebhook(context.Background(), "b1", webhook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Secret != "" {
		t.Fatalf("secret must be cleared, got %q", stored.Secret)
	}
}

// The identifier is parsed before the body is inspected: a malformed one is
// refused even when the body named nothing to update.
func TestWebhookUpdateMalformedIDPrecedesNoFields(t *testing.T) {
	mux, _ := newServer(t)
	rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/webhooks/not-a-uuid", `{}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if detail := errorDetail(t, rec); detail != "Invalid webhook ID" {
		t.Fatalf("detail = %q, want %q", detail, "Invalid webhook ID")
	}
	// A well-formed identifier with nothing to update keeps the reference's own
	// no-fields refusal.
	rec = send(mux, http.MethodPatch, "/v1/default/banks/b1/webhooks/00000000-0000-0000-0000-000000000000", `{}`)
	if rec.Code != http.StatusUnprocessableEntity || errorDetail(t, rec) != "No fields provided to update" {
		t.Fatalf("empty patch: %d %s", rec.Code, rec.Body.String())
	}
}

// A null on a nullable member is accepted by the model and reaches the
// reference's write unprepared: http_config is dereferenced while the statement
// is built, and url/event_types/enabled are written into a NOT NULL column, so
// an existing row is a 500 either way — while a null on the three columns is
// only a failure once a row matched, which makes an unknown identifier the
// reference's 404.
func TestWebhookUpdateNullMembers(t *testing.T) {
	mux, store := newServer(t)
	webhook := createBody(t, mux, `{"url":"https://example.test/null"}`)
	const missing = "00000000-0000-0000-0000-000000000000"
	cases := []struct {
		body   string
		id     string
		status int
		detail string
	}{
		{`{"http_config":null}`, webhook.ID, http.StatusInternalServerError, httpx.DetailInternal},
		{`{"http_config":null}`, missing, http.StatusInternalServerError, httpx.DetailInternal},
		{`{"url":null}`, webhook.ID, http.StatusInternalServerError, httpx.DetailInternal},
		{`{"event_types":null}`, webhook.ID, http.StatusInternalServerError, httpx.DetailInternal},
		{`{"enabled":null}`, webhook.ID, http.StatusInternalServerError, httpx.DetailInternal},
		{`{"url":null}`, missing, http.StatusNotFound, "Webhook not found"},
		{`{"event_types":null}`, missing, http.StatusNotFound, "Webhook not found"},
		{`{"enabled":null}`, missing, http.StatusNotFound, "Webhook not found"},
	}
	for _, tc := range cases {
		rec := send(mux, http.MethodPatch, "/v1/default/banks/b1/webhooks/"+tc.id, tc.body)
		if rec.Code != tc.status {
			t.Fatalf("%s on %s: status = %d, want %d (body %s)", tc.body, tc.id, rec.Code, tc.status, rec.Body.String())
		}
		if detail := errorDetail(t, rec); detail != tc.detail {
			t.Fatalf("%s on %s: detail = %q, want %q", tc.body, tc.id, detail, tc.detail)
		}
	}
	// The row is untouched by the failed updates.
	stored, err := store.GetWebhook(context.Background(), "b1", webhook.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.URL != "https://example.test/null" || stored.EventTypes == nil || !stored.Enabled {
		t.Fatalf("webhook changed: %+v", stored)
	}
}

// errorDetail reads the detail field of an error envelope.
func errorDetail(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var envelope struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("error body %s: %v", rec.Body.String(), err)
	}
	return envelope.Detail
}

// The deliveries route rejects an out-of-range limit and an unparseable cursor
// the way the reference does.
func TestWebhookDeliveriesValidation(t *testing.T) {
	mux, _ := newServer(t)
	webhook := createBody(t, mux, `{"url":"https://example.test/d"}`)
	base := "/v1/default/banks/b1/webhooks/" + webhook.ID + "/deliveries"
	if rec := send(mux, http.MethodGet, base+"?limit=300", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=300: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"?limit=abc", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=abc: %d, want 422", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"?limit=0", ""); rec.Code != http.StatusOK {
		t.Fatalf("limit=0: %d, want 200", rec.Code)
	}
	if rec := send(mux, http.MethodGet, "/v1/default/banks/b1/webhooks/not-a-uuid/deliveries", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed id: %d, want 422", rec.Code)
	}
	unknown := "11111111-1111-1111-1111-111111111111"
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/webhooks/"+unknown+"/deliveries", "")
	var body struct {
		Detail string `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusNotFound || body.Detail != "Webhook not found" {
		t.Fatalf("unknown webhook = %d %q", rec.Code, body.Detail)
	}
}

// The http_config timeout is an integer the reference's lax mode reads from a
// numeric string or a boolean, and an explicit zero is a timeout the caller
// asked for rather than an unset field that falls back to the default.
func TestWebhookHTTPConfigTimeoutLax(t *testing.T) {
	mux, _ := newServer(t)
	fromString := createBody(t, mux, `{"url":"https://example.test/a","http_config":{"timeout_seconds":"30"}}`)
	if fromString.HTTPConfig.TimeoutSeconds != 30 {
		t.Fatalf("timeout_seconds = %d, want 30", fromString.HTTPConfig.TimeoutSeconds)
	}
	fromBool := createBody(t, mux, `{"url":"https://example.test/b","http_config":{"timeout_seconds":true}}`)
	if fromBool.HTTPConfig.TimeoutSeconds != 1 {
		t.Fatalf("timeout_seconds = %d, want 1", fromBool.HTTPConfig.TimeoutSeconds)
	}
	zero := createBody(t, mux, `{"url":"https://example.test/c","http_config":{"timeout_seconds":0}}`)
	if zero.HTTPConfig.TimeoutSeconds != 0 {
		t.Fatalf("an explicit zero timeout = %d, want 0", zero.HTTPConfig.TimeoutSeconds)
	}
}

// A whole number other than 0 or 1 sent for a boolean is a parsing failure
// whose wording says the value could not be interpreted.
func TestWebhookBooleanNumberIsParsingFailure(t *testing.T) {
	mux, _ := newServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/b1/webhooks",
		`{"url":"https://example.test/h","enabled":2}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"bool_parsing"`) {
		t.Fatalf("detail = %s", rec.Body.String())
	}
}
