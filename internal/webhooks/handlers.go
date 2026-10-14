package webhooks

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// Webhook is the wire shape. Field names are public surface.
type Webhook struct {
	ID         string   `json:"id"`
	BankID     string   `json:"bank_id"`
	URL        string   `json:"url"`
	EventTypes []string `json:"event_types"`
	Enabled    bool     `json:"enabled"`
	// Secret is never returned, because a receiver's signing secret must not be
	// readable through the API once it is set.
	HasSecret bool   `json:"has_secret"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// CreateRequest is the create body.
type CreateRequest struct {
	URL        string   `json:"url"`
	Secret     string   `json:"secret"`
	EventTypes []string `json:"event_types"`
	Enabled    *bool    `json:"enabled"`
}

// UpdateRequest is the partial-update body.
type UpdateRequest struct {
	URL        *string   `json:"url"`
	Secret     *string   `json:"secret"`
	EventTypes *[]string `json:"event_types"`
	Enabled    *bool     `json:"enabled"`
}

// ListResponse is the collection envelope.
type ListResponse struct {
	BankID   string    `json:"bank_id"`
	Webhooks []Webhook `json:"webhooks"`
}

// Delivery is one recorded delivery attempt.
type Delivery struct {
	ID          string `json:"id"`
	WebhookID   string `json:"webhook_id"`
	EventType   string `json:"event_type"`
	Status      string `json:"status"`
	StatusCode  *int   `json:"status_code"`
	Error       string `json:"error,omitempty"`
	Attempt     int    `json:"attempt"`
	DeliveredAt string `json:"delivered_at"`
}

// DeliveryListResponse is the delivery listing envelope.
type DeliveryListResponse struct {
	BankID     string     `json:"bank_id"`
	WebhookID  string     `json:"webhook_id"`
	Deliveries []Delivery `json:"deliveries"`
	Total      int        `json:"total"`
	Limit      int        `json:"limit"`
	Offset     int        `json:"offset"`
}

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
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixWebhooks), h.List)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixWebhooks), h.Create)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixWebhookMember), h.Update)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixWebhookMember), h.Delete)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixWebhookDeliveries), h.ListDeliveries)
}

// List returns the bank's webhooks.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	records, err := h.Store.ListWebhooks(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]Webhook, 0, len(records))
	for _, record := range records {
		out = append(out, toWebhook(record))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ListResponse{BankID: bankID, Webhooks: out})
}

// Create registers a webhook after validating its target URL.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	var body CreateRequest
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
	enabled := true
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	record, err := h.Store.PutWebhook(r.Context(), storage.Webhook{
		ID: newID(), BankID: bankID, URL: strings.TrimSpace(body.URL),
		Secret: body.Secret, EventTypes: events, Enabled: enabled,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusCreated, toWebhook(record))
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
	record, err := h.Store.UpdateWebhook(r.Context(), bankID, webhookID, storage.WebhookPatch{
		URL: body.URL, Secret: body.Secret, EventTypes: events, Enabled: body.Enabled,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toWebhook(record))
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
	w.WriteHeader(http.StatusNoContent)
}

// ListDeliveries returns one page of a webhook's delivery records.
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
	page, err := h.Store.ListWebhookDeliveries(r.Context(), bankID, webhookID, pageLimit(r), pageOffset(r))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]Delivery, 0, len(page.Deliveries))
	for _, record := range page.Deliveries {
		out = append(out, Delivery{
			ID: record.ID, WebhookID: record.WebhookID, EventType: record.EventType,
			Status: record.Status, StatusCode: record.StatusCode, Error: record.Error,
			Attempt: record.Attempt, DeliveredAt: formatTime(record.DeliveredAt),
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DeliveryListResponse{
		BankID: bankID, WebhookID: webhookID, Deliveries: out,
		Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r),
	})
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
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	return bankID, true
}

// toWebhook renders a stored record, withholding the secret.
func toWebhook(record storage.Webhook) Webhook {
	events := record.EventTypes
	if events == nil {
		events = []string{}
	}
	return Webhook{
		ID: record.ID, BankID: record.BankID, URL: record.URL,
		EventTypes: events, Enabled: record.Enabled,
		HasSecret: record.Secret != "",
		CreatedAt: formatTime(record.CreatedAt), UpdatedAt: formatTime(record.UpdatedAt),
	}
}

// decodeBody decodes a JSON body with a size bound.
func decodeBody(r *http.Request, target any) error {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	return nil
}

// pageLimit reads the page size.
func pageLimit(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
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

// pageOffset reads the page offset.
func pageOffset(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("offset"))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// formatTime renders a timestamp as the API reports it.
func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "bank not found")
	case errors.Is(err, storage.ErrWebhookNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "webhook not found")
	case errors.Is(err, storage.ErrWebhookURLNotAllowed):
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	default:
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	}
}

// newID builds an opaque identifier.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("wh-%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(raw[:])
}
