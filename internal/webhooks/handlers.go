package webhooks

import (
	"net/http"
	"strconv"

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

// List returns the bank's webhooks, oldest first.
func (h *Handlers) List(w http.ResponseWriter, r *http.Request) {
	// Webhooks are bank-wide, so a tag-scoped caller may not read them.
	if httpx.RefuseBankWideIfScoped(w, r, "list the bank's webhooks") {
		return
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	limit, offset, failure := parseListQuery(r)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
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
	total := len(out)
	page := pageSlice(out, offset, limit)
	// The response model keeps null fields (the redacted secret), so it is
	// written through the open-row writer.
	httpx.WriteJSON(w, http.StatusOK, ListResponse{Items: page, Total: total, Limit: limit, Offset: offset})
}

// Create registers a webhook after validating its target URL.
//
// The bank is created on demand: a webhook registration is a bank-scoped write
// and the bank row is its foreign-key target, so a caller that has not declared
// the bank yet gets it created here rather than a not-found refusal. Listing and
// the other reads still refuse an unknown bank.
func (h *Handlers) Create(w http.ResponseWriter, r *http.Request) {
	// Webhooks are bank-wide, so a tag-scoped caller may not change them.
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's webhooks") {
		return
	}
	bankID, ok := h.bankID(w, r)
	if !ok {
		return
	}
	input, failure := validateCreateBody(r)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	// The URL is validated before anything is stored, so an unreachable or
	// internal target can never be persisted.
	if err := h.Guard.Validate(input.url); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	record, err := h.Store.PutWebhook(r.Context(), storage.Webhook{
		ID: newID(), BankID: bankID, URL: input.url,
		Secret: input.secret, EventTypes: input.eventTypes, Enabled: input.enabled,
		HTTPConfig: toStorageConfig(input.httpConfig),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, toWebhook(record))
}

// Update applies a partial update, re-validating any new URL.
func (h *Handlers) Update(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's webhooks") {
		return
	}
	// The framework validates the body before the handler runs, so a rejected
	// body is a 422 that precedes the webhook identifier's own 422/404.
	input, failure, noFields := validateUpdateBody(r)
	if noFields {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindUnprocessable, "No fields provided to update"))
		return
	}
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	bankID, webhookID, ok := h.member(w, r)
	if !ok {
		return
	}
	if input.url != nil {
		if err := h.Guard.Validate(*input.url); err != nil {
			httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
			return
		}
	}
	record, err := h.Store.UpdateWebhook(r.Context(), bankID, webhookID, storage.WebhookPatch{
		URL: input.url, Secret: input.secret, ClearSecret: input.clearSecret,
		EventTypes: input.eventTypes, Enabled: input.enabled, HTTPConfig: input.httpConfig,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toWebhook(record))
}

// Delete removes a webhook.
func (h *Handlers) Delete(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's webhooks") {
		return
	}
	bankID, webhookID, ok := h.member(w, r)
	if !ok {
		return
	}
	if err := h.Store.DeleteWebhook(r.Context(), bankID, webhookID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, DeleteResponse{Success: true})
}

// ListDeliveries returns one page of a webhook's delivery records, newest
// first, addressed by the created_at cursor of the last item of the previous
// page.
//
// The webhook must exist: an empty list for an unknown identifier would read as
// "this webhook has delivered nothing" rather than "there is no such webhook".
func (h *Handlers) ListDeliveries(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "read the bank's webhook deliveries") {
		return
	}
	bankID, webhookID, ok := h.member(w, r)
	if !ok {
		return
	}
	limit, failure := parseDeliveryLimit(r)
	if failure != nil {
		httpx.WriteValidationFailure(w, failure)
		return
	}
	if _, err := h.Store.GetWebhook(r.Context(), bankID, webhookID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	cursor := httpx.Query(r, "cursor")
	if limit <= 0 {
		// The reference slices the fetched rows to `[:limit]`, so a non-positive
		// limit yields an empty page while `total` is unaffected.
		httpx.WriteJSON(w, http.StatusOK, DeliveryListResponse{Items: []Delivery{}, NextCursor: nil})
		return
	}
	page, err := h.Store.ListWebhookDeliveries(r.Context(), bankID, webhookID, limit, cursor)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	out := make([]Delivery, 0, len(page.Deliveries))
	exposeBody := h.Guard.Global().ExposesResponseBody()
	for _, record := range page.Deliveries {
		out = append(out, toDelivery(record, exposeBody))
	}
	var next *string
	if page.HasMore && len(page.Deliveries) > 0 {
		value := httpx.FormatPostgresTimestamp(page.Deliveries[len(page.Deliveries)-1].CreatedAt)
		next = &value
	}
	httpx.WriteJSON(w, http.StatusOK, DeliveryListResponse{Items: out, NextCursor: next})
}

// member resolves the path bank and the webhook identifier. The identifier is
// folded to the canonical uuid the store keys on, and a value that is not a uuid
// is the reference's 422 "Invalid webhook ID".
func (h *Handlers) member(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	webhookID, ok := h.webhookIDParam(w, r)
	if !ok {
		return "", "", false
	}
	return r.PathValue("bank_id"), webhookID, true
}

// webhookIDParam reads and canonicalizes the path webhook identifier.
func (h *Handlers) webhookIDParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	canonical, ok := canonicalWebhookID(r.PathValue("webhook_id"))
	if !ok {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindUnprocessable, "Invalid webhook ID"))
		return "", false
	}
	return canonical, true
}

// requireBank reports not-found for an unknown bank and returns the bank id.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID, ok := h.bankID(w, r)
	if !ok {
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// bankID reads the path bank identifier, refusing an empty one.
func (h *Handlers) bankID(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := r.PathValue("bank_id")
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank))
		return "", false
	}
	return bankID, true
}

// parseListQuery reads the collection's limit and offset with the reference's
// bounds: limit 0..1000 (default 100) and a non-negative offset.
func parseListQuery(r *http.Request) (int, int, *httpx.ValidationFailure) {
	items := make([]httpx.ValidationError, 0, 2)
	limit := 100
	if value, present, item := queryIntParam(r, "limit", 0, 1000); item != nil {
		items = append(items, *item)
	} else if present {
		limit = value
	}
	offset := 0
	if value, present, item := queryIntParam(r, "offset", 0, 0); item != nil {
		items = append(items, *item)
	} else if present {
		offset = value
	}
	if len(items) > 0 {
		return 0, 0, &httpx.ValidationFailure{Errors: items}
	}
	return limit, offset, nil
}

// parseDeliveryLimit reads the delivery page size: default 50, upper bound 200,
// and no lower bound (the reference declares only le=200).
func parseDeliveryLimit(r *http.Request) (int, *httpx.ValidationFailure) {
	values, present := r.URL.Query()["limit"]
	if !present || len(values) == 0 {
		return 50, nil
	}
	parsed, err := strconv.Atoi(values[0])
	if err != nil {
		item := httpx.TypeMismatch("int_parsing", []string{"query", "limit"}, values[0])
		return 0, &httpx.ValidationFailure{Errors: []httpx.ValidationError{item}}
	}
	if parsed > 200 {
		item := httpx.LessThanEqual([]string{"query", "limit"}, values[0], 200)
		return 0, &httpx.ValidationFailure{Errors: []httpx.ValidationError{item}}
	}
	return parsed, nil
}

// queryIntParam parses a bounded integer query parameter. max of 0 means no
// upper bound. The structured item names the raw value the client sent.
func queryIntParam(r *http.Request, key string, min, max int) (int, bool, *httpx.ValidationError) {
	values, present := r.URL.Query()[key]
	if !present || len(values) == 0 {
		return 0, false, nil
	}
	raw := values[0]
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		item := httpx.TypeMismatch("int_parsing", []string{"query", key}, raw)
		return 0, false, &item
	}
	if parsed < min {
		item := httpx.GreaterThanEqual([]string{"query", key}, raw, min)
		return 0, false, &item
	}
	if max != 0 && parsed > max {
		item := httpx.LessThanEqual([]string{"query", key}, raw, max)
		return 0, false, &item
	}
	return parsed, true, nil
}

// pageSlice returns one page of a slice.
func pageSlice[T any](items []T, offset, limit int) []T {
	if offset >= len(items) || limit <= 0 {
		return items[len(items):]
	}
	end := offset + limit
	if end > len(items) {
		end = len(items)
	}
	return items[offset:end]
}
