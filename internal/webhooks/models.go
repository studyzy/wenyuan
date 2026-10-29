package webhooks

import (
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// HTTPConfig is the typed HTTP delivery configuration. Field names and defaults
// are public surface: every field is always emitted.
type HTTPConfig struct {
	Method         string            `json:"method"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Headers        map[string]string `json:"headers"`
	Params         map[string]string `json:"params"`
}

// defaultHTTPConfig is the reference's unset http_config: POST, thirty seconds,
// no custom headers or query parameters.
func defaultHTTPConfig() HTTPConfig {
	return HTTPConfig{Method: "POST", TimeoutSeconds: 30, Headers: map[string]string{}, Params: map[string]string{}}
}

// Webhook is the wire shape. Field names are public surface. The secret is
// always present and null: a receiver's signing secret must not be readable
// through the API once it is set.
type Webhook struct {
	ID         string     `json:"id"`
	BankID     string     `json:"bank_id"`
	URL        string     `json:"url"`
	Secret     *string    `json:"secret"`
	EventTypes []string   `json:"event_types"`
	Enabled    bool       `json:"enabled"`
	HTTPConfig HTTPConfig `json:"http_config"`
	CreatedAt  string     `json:"created_at"`
	UpdatedAt  string     `json:"updated_at"`
}

// CreateRequest is the create body.
type CreateRequest struct {
	URL        string     `json:"url"`
	Secret     *string    `json:"secret"`
	EventTypes []string   `json:"event_types"`
	Enabled    *bool      `json:"enabled"`
	HTTPConfig HTTPConfig `json:"http_config"`
}

// UpdateRequest is the partial-update body.
type UpdateRequest struct {
	URL        *string     `json:"url"`
	Secret     *string     `json:"secret"`
	EventTypes *[]string   `json:"event_types"`
	Enabled    *bool       `json:"enabled"`
	HTTPConfig *HTTPConfig `json:"http_config"`
}

// ListResponse is the collection envelope.
type ListResponse struct {
	Items  []Webhook `json:"items"`
	Total  int       `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

// DeleteResponse is the delete acknowledgement. The reference keeps nulls on
// this response, so the unused members are emitted as explicit nulls.
type DeleteResponse struct {
	Success      bool    `json:"success"`
	Message      *string `json:"message"`
	DeletedCount *int    `json:"deleted_count"`
}

// Delivery is one recorded delivery attempt. Nullable fields stay present as
// null, matching the reference's delivery model; event_id is null only for a
// delivery queued before event ids existed.
type Delivery struct {
	ID                 string  `json:"id"`
	EventID            *string `json:"event_id"`
	WebhookID          string  `json:"webhook_id"`
	URL                string  `json:"url"`
	EventType          string  `json:"event_type"`
	Status             string  `json:"status"`
	Attempts           int     `json:"attempts"`
	NextRetryAt        *string `json:"next_retry_at"`
	LastError          *string `json:"last_error"`
	LastResponseStatus *int    `json:"last_response_status"`
	LastResponseBody   *string `json:"last_response_body"`
	LastAttemptAt      *string `json:"last_attempt_at"`
	CreatedAt          *string `json:"created_at"`
	UpdatedAt          *string `json:"updated_at"`
}

// DeliveryListResponse is the delivery listing envelope.
type DeliveryListResponse struct {
	Items      []Delivery `json:"items"`
	NextCursor *string    `json:"next_cursor"`
}

// toWebhook renders a stored record, withholding the secret (always present and
// null) and defaulting the HTTP configuration.
func toWebhook(record storage.Webhook) Webhook {
	events := record.EventTypes
	if events == nil {
		events = []string{}
	}
	return Webhook{
		ID: record.ID, BankID: record.BankID, URL: record.URL,
		EventTypes: events, Enabled: record.Enabled,
		HTTPConfig: toHTTPConfig(record.HTTPConfig),
		CreatedAt:  httpx.FormatPostgresTimestamp(record.CreatedAt),
		UpdatedAt:  httpx.FormatPostgresTimestamp(record.UpdatedAt),
	}
}

// toDelivery renders one stored delivery record for the wire. exposeBody gates
// the receiver's response body: the row stores it, but the reference withholds
// it from API callers unless the deployment opts in.
func toDelivery(record storage.WebhookDelivery, exposeBody bool) Delivery {
	delivery := Delivery{
		ID:        record.ID,
		WebhookID: record.WebhookID,
		URL:       record.URL,
		EventType: record.EventType,
		Status:    record.Status,
		Attempts:  record.Attempts,
	}
	if record.EventID != "" {
		value := record.EventID
		delivery.EventID = &value
	}
	if record.NextRetryAt != nil {
		value := httpx.FormatPostgresTimestamp(*record.NextRetryAt)
		delivery.NextRetryAt = &value
	}
	if record.LastError != "" {
		value := record.LastError
		delivery.LastError = &value
	}
	delivery.LastResponseStatus = record.LastResponseStatus
	if exposeBody {
		delivery.LastResponseBody = record.LastResponseBody
	}
	if record.LastAttemptAt != nil {
		value := httpx.FormatTimestamp(*record.LastAttemptAt)
		delivery.LastAttemptAt = &value
	}
	if !record.CreatedAt.IsZero() {
		value := httpx.FormatPostgresTimestamp(record.CreatedAt)
		delivery.CreatedAt = &value
	}
	if !record.UpdatedAt.IsZero() {
		value := httpx.FormatPostgresTimestamp(record.UpdatedAt)
		delivery.UpdatedAt = &value
	}
	return delivery
}

// toHTTPConfig renders a stored configuration, filling defaults for any field
// the stored value leaves unset.
func toHTTPConfig(raw map[string]any) HTTPConfig {
	cfg := defaultHTTPConfig()
	if raw == nil {
		return cfg
	}
	if method, ok := raw["method"].(string); ok && method != "" {
		cfg.Method = method
	}
	if seconds, ok := numberValue(raw["timeout_seconds"]); ok {
		// An explicit value is honoured as stored: zero is a timeout the caller
		// asked for, not an unset field, so it must not fall back to the
		// default the way a missing key does.
		cfg.TimeoutSeconds = seconds
	}
	if headers, ok := raw["headers"]; ok {
		cfg.Headers = stringMap(headers)
	}
	if params, ok := raw["params"]; ok {
		cfg.Params = stringMap(params)
	}
	return cfg
}

// toStorageConfig renders the wire configuration as the stored map, always with
// every field present so the stored shape is the reference's full config.
func toStorageConfig(cfg HTTPConfig) map[string]any {
	if cfg.Method == "" {
		cfg.Method = "POST"
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	if cfg.Params == nil {
		cfg.Params = map[string]string{}
	}
	return map[string]any{
		"method":          cfg.Method,
		"timeout_seconds": cfg.TimeoutSeconds,
		"headers":         cfg.Headers,
		"params":          cfg.Params,
	}
}

// stringMap converts a decoded JSON object to a string map, dropping
// non-string values rather than failing the response.
func stringMap(raw any) map[string]string {
	source, ok := raw.(map[string]any)
	if !ok {
		if direct, ok := raw.(map[string]string); ok {
			return direct
		}
		return map[string]string{}
	}
	out := make(map[string]string, len(source))
	for key, value := range source {
		if text, ok := value.(string); ok {
			out[key] = text
		}
	}
	return out
}

// numberValue reads an integer from a decoded JSON number, which is a float64,
// or from an integer stored in process.
func numberValue(raw any) (int, bool) {
	switch value := raw.(type) {
	case float64:
		return int(value), true
	case int:
		return value, true
	case int64:
		return int(value), true
	case json.Number:
		parsed, err := value.Int64()
		if err != nil {
			return 0, false
		}
		return int(parsed), true
	default:
		return 0, false
	}
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailMissingBank)
	case errors.Is(err, storage.ErrWebhookNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "Webhook not found")
	case errors.Is(err, storage.ErrWebhookURLNotAllowed):
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	case errors.Is(err, storage.ErrInvalidBankID):
		return httpx.NewHTTPError(httpx.KindBadRequest, "bank identifier is invalid")
	case errors.Is(err, storage.ErrAliasConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "name conflicts with an existing bank or alias")
	case errors.Is(err, storage.ErrWritesUnavailable):
		return httpx.NewHTTPError(httpx.KindUnavailable, httpx.DetailWritesDown)
	default:
		// Anything the storage layer did not recognise is a server fault, not a
		// malformed request: the reference's catch-all answers 500 and reports the
		// error it caught.
		return httpx.NewHTTPError(httpx.KindInternal, err.Error())
	}
}

// NewEventID mints the identifier of one logical webhook event. Every delivery
// an event fans out to carries the same value, and every retry of one delivery
// repeats it, so a receiver dedupes on it. It is deliberately not an operation
// id: a batch retain fires one event per document under one operation, so the
// operation cannot tell two of its events apart.
func NewEventID() string { return newID() }

// newID builds the identifier a webhook is addressed by. The column is a uuid
// on both backends, so the value is rendered in its canonical dashed form: a
// bare 32-character hex string stores as the same uuid but does not compare
// equal to the column's text form, which made every read of a webhook just
// written report it missing.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		// A system entropy failure is not something a webhook registration can
		// recover from, but the value still has to be a canonical uuid or the
		// row is not addressable; derive one from the clock instead of
		// returning a shape the store cannot look up.
		binary.BigEndian.PutUint64(raw[:], uint64(time.Now().UnixNano()))
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(raw[:])
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// canonicalWebhookID folds the uuid spellings the reference's parser accepts —
// the dashed 8-4-4-4-12 form, the 32-hex form, and the brace- and urn-wrapped
// spellings — onto the canonical dashed form the store compares against. It
// reports false for a value that is not a uuid at all, which the route answers
// with the reference's 422 "Invalid webhook ID".
func canonicalWebhookID(raw string) (string, bool) {
	value := raw
	if len(value) >= len("urn:uuid:") && strings.EqualFold(value[:len("urn:uuid:")], "urn:uuid:") {
		value = value[len("urn:uuid:"):]
	}
	if len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' {
		value = value[1 : len(value)-1]
	}
	switch len(value) {
	case 32:
		if !isHex(value) {
			return "", false
		}
	case 36:
		if !dashedUUIDShape(value) {
			return "", false
		}
		value = strings.ReplaceAll(value, "-", "")
	default:
		return "", false
	}
	hexed := strings.ToLower(value)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32], true
}

// isHex reports whether every byte of a value is a hexadecimal digit.
func isHex(value string) bool {
	if value == "" {
		return false
	}
	for i := range value {
		if !isHexDigit(value[i]) {
			return false
		}
	}
	return true
}

// dashedUUIDShape reports whether a value has the 8-4-4-4-12 hex shape.
func dashedUUIDShape(value string) bool {
	for i := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if value[i] != '-' {
				return false
			}
			continue
		}
		if !isHexDigit(value[i]) {
			return false
		}
	}
	return true
}

func isHexDigit(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}
