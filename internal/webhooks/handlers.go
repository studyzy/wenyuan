package webhooks

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// Handlers serves the webhook route family.
type Handlers struct {
	Store storage.Store
	Guard *Guard
}

// NewHandlers builds the handlers over the store and the URL guard.
func NewHandlers(store storage.Store, guard *Guard) *Handlers {
	if guard == nil {
		guard = NewGuard(GuardConfig{})
	}
	return &Handlers{Store: store, Guard: guard}
}

// BindRoutes registers the family's route table.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixWebhooks), httpx.QueryParams(h.List, "limit", "offset"))
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixWebhooks), h.Create)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixWebhookMember), h.Update)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixWebhookMember), h.Delete)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixWebhookDeliveries), httpx.QueryParams(h.ListDeliveries, "limit", "cursor"))
}

// List returns the bank's webhooks.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requireBank(w, r); !ok {
		return
	}
	bankID := r.PathValue("bank_id")
	records, err := h.Store.ListWebhooks(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]Webhook, 0, len(records))
	for _, record := range records {
		out = append(out, toWebhook(record))
	}
	total := len(out)
	limit, offset := pageParams(r)
	out = pageSlice(out, offset, limit)
	// The response model keeps null fields (the redacted secret), so it is
	// written through the open-row writer.
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, ListResponse{Items: out, Total: total, Limit: limit, Offset: offset})
}

// Create registers a webhook after validating its target URL.
//
// The bank is created on demand: a webhook registration is a bank-scoped write
// and the bank row is its foreign-key target, so a caller that has not declared
// the bank yet gets it created here rather than a not-found refusal. Listing and
// the other reads still refuse an unknown bank.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return
	}
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	body := CreateRequest{HTTPConfig: defaultHTTPConfig()}
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	// The URL is validated before anything is stored, so an unreachable or
	// internal target can never be persisted.
	if err := h.Guard.Validate(body.URL); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	events, err := h.validateEvents(body.EventTypes)
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	// A webhook with no explicit subscription follows consolidation, matching
	// the reference's default.
	if len(events) == 0 {
		events = []string{"consolidation.completed"}
	}
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	secret := ""
	if body.Secret != nil {
		secret = *body.Secret
	}
	record, err := h.Store.PutWebhook(r.Context(), storage.Webhook{
		ID: newID(), BankID: bankID, URL: strings.TrimSpace(body.URL),
		Secret: secret, EventTypes: events, Enabled: enabled, HTTPConfig: toStorageConfig(body.HTTPConfig),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusCreated, toWebhook(record))
}

// Update applies a partial update, re-validating any new URL.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	bankID, webhookID, ok := h.member(w, r)
	if !ok {
		return
	}
	var body UpdateRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if body.URL != nil {
		if err := h.Guard.Validate(*body.URL); err != nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
			return
		}
	}
	var events *[]string
	if body.EventTypes != nil {
		validated, err := h.validateEvents(*body.EventTypes)
		if err != nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
			return
		}
		events = &validated
	}
	var config *map[string]any
	if body.HTTPConfig != nil {
		config = toStorageConfigPtr(*body.HTTPConfig)
	}
	record, err := h.Store.UpdateWebhook(r.Context(), bankID, webhookID, storage.WebhookPatch{
		URL: body.URL, Secret: body.Secret, EventTypes: events, Enabled: body.Enabled,
		HTTPConfig: config,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, toWebhook(record))
}

// Delete removes a webhook.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	bankID, webhookID, ok := h.member(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteWebhook(r.Context(), bankID, webhookID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DeleteResponse{Success: true, Message: "Deleted successfully"})
}

// ListDeliveries returns one page of a webhook's delivery records, newest
// first, addressed by the created_at cursor of the last item of the previous
// page.
//
// The webhook must exist: an empty list for an unknown identifier would read as
// "this webhook has delivered nothing" rather than "there is no such webhook".
func (h *Handlers) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	bankID, webhookID, ok := h.member(w, r)
	if !ok {
		return
	}
	if _, err := h.Store.GetWebhook(r.Context(), bankID, webhookID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	cursor := strings.TrimSpace(httpx.Query(r, "cursor"))
	page, err := h.Store.ListWebhookDeliveries(r.Context(), bankID, webhookID, deliveryLimit(r), cursor)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]Delivery, 0, len(page.Deliveries))
	for _, record := range page.Deliveries {
		out = append(out, toDelivery(record))
	}
	var next *string
	if page.HasMore && len(page.Deliveries) > 0 {
		value := formatDeliveryTime(page.Deliveries[len(page.Deliveries)-1].CreatedAt)
		next = &value
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, DeliveryListResponse{Items: out, NextCursor: next})
}

// validateEvents rejects an unknown event name, so a webhook cannot subscribe
// to something that will never fire.
func (h *Handlers) validateEvents(events []string) ([]string, error) {
	if len(events) == 0 {
		return []string{}, nil
	}
	known := map[string]bool{}
	for _, name := range storage.WebhookEventTypes() {
		known[name] = true
	}
	out := make([]string, 0, len(events))
	for _, event := range events {
		trimmed := strings.TrimSpace(event)
		if trimmed == "" {
			continue
		}
		if !known[trimmed] {
			return nil, fmt.Errorf("unknown event type %q", trimmed)
		}
		out = append(out, trimmed)
	}
	return out, nil
}

// member resolves the bank and the webhook identifier.
func (h *Handlers) member(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return "", "", false
	}
	webhookID := strings.TrimSpace(r.PathValue("webhook_id"))
	if webhookID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "webhook not found"))
		return "", "", false
	}
	return bankID, webhookID, true
}

// requireBank reports not-found for an unknown bank.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// decodeBody decodes a JSON body with a size bound.
func decodeBody(r *http.Request, target any) error {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	httpx.RecordUnknownBodyFields(r, body, target)
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	return nil
}

// pageLimit reads the page size for the webhook collection.
func pageLimit(r *http.Request) int {
	raw := strings.TrimSpace(httpx.Query(r, "limit"))
	if raw == "" {
		return 50
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 50
	}
	if value > 500 {
		return 500
	}
	return value
}

// pageOffset reads the page offset for the webhook collection.
func pageOffset(r *http.Request) int {
	raw := strings.TrimSpace(httpx.Query(r, "offset"))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// pageParams reads the collection's page size and offset.
func pageParams(r *http.Request) (int, int) { return pageLimit(r), pageOffset(r) }

// pageSlice returns one page of a slice.
func pageSlice[T any](items []T, offset, limit int) []T {
	if offset >= len(items) {
		return items[len(items):]
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}

// deliveryLimit reads the delivery page size: default 50, capped at 200.
func deliveryLimit(r *http.Request) int {
	raw := strings.TrimSpace(httpx.Query(r, "limit"))
	if raw == "" {
		return 50
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 50
	}
	if value > 200 {
		return 200
	}
	return value
}
