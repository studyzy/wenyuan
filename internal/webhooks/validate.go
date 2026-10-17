package webhooks

import (
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// defaultEventTypes is the subscription the reference applies when a create body
// omits event_types. An explicit empty list is stored as an empty list.
var defaultEventTypes = []string{"consolidation.completed"}

// createInput is the accepted create body: the values the route stores.
type createInput struct {
	url        string
	secret     string
	eventTypes []string
	enabled    bool
	httpConfig HTTPConfig
}

// updateInput is a validated partial update. Each pointer is non-nil only when
// the caller sent that field; clearSecret reports an explicit null secret, which
// the reference treats as a clear rather than as "unchanged".
type updateInput struct {
	url         *string
	secret      *string
	clearSecret bool
	eventTypes  *[]string
	enabled     *bool
	httpConfig  *map[string]any
}

// validateCreateBody decodes and validates a webhook create body the way the
// reference's request model does, returning the structured items for any
// rejected field.
func validateCreateBody(r *http.Request) (createInput, *httpx.ValidationFailure) {
	raw, body, failure := decodeWebhookBody(r, createBodySample{})
	if failure != nil {
		return createInput{}, failure
	}
	fields := raw
	var items []httpx.ValidationError

	url, urlItem := requireString("url", fields, body)
	if urlItem != nil {
		items = append(items, *urlItem)
	}

	secret := ""
	if value, present := fields["secret"]; present && !isNull(value) {
		text, item := asString(value, []string{"body", "secret"})
		if item != nil {
			items = append(items, *item)
		} else {
			secret = text
		}
	}

	eventTypes := defaultEventTypes
	if value, present := fields["event_types"]; present {
		parsed, listItems := asStringArray(value, []string{"body", "event_types"})
		if len(listItems) > 0 {
			items = append(items, listItems...)
		} else {
			eventTypes = parsed
		}
	}

	enabled := true
	if value, present := fields["enabled"]; present {
		parsed, item := asBool(value, []string{"body", "enabled"})
		if item != nil {
			items = append(items, *item)
		} else {
			enabled = parsed
		}
	}

	httpConfig := defaultHTTPConfig()
	if value, present := fields["http_config"]; present {
		parsed, configItems := asHTTPConfig(value)
		if len(configItems) > 0 {
			items = append(items, configItems...)
		} else {
			httpConfig = parsed
		}
	}

	if len(items) > 0 {
		return createInput{}, &httpx.ValidationFailure{Errors: items}
	}
	return createInput{url: url, secret: secret, eventTypes: eventTypes, enabled: enabled, httpConfig: httpConfig}, nil
}

// validateUpdateBody decodes and validates a webhook partial-update body. The
// second result reports a body that names no recognised field, which the
// reference refuses with its own "No fields provided to update" detail rather
// than the framework's item list.
func validateUpdateBody(r *http.Request) (updateInput, *httpx.ValidationFailure, bool) {
	raw, _, failure := decodeWebhookBody(r, updateBodySample{})
	if failure != nil {
		return updateInput{}, failure, false
	}
	fields := raw
	if !namesKnownField(fields) {
		return updateInput{}, nil, true
	}
	var items []httpx.ValidationError
	var input updateInput

	if value, present := fields["url"]; present && !isNull(value) {
		text, item := asString(value, []string{"body", "url"})
		if item != nil {
			items = append(items, *item)
		} else {
			input.url = &text
		}
	}
	if value, present := fields["secret"]; present {
		if isNull(value) {
			input.clearSecret = true
		} else if text, item := asString(value, []string{"body", "secret"}); item != nil {
			items = append(items, *item)
		} else {
			input.secret = &text
		}
	}
	if value, present := fields["event_types"]; present {
		parsed, listItems := asStringArray(value, []string{"body", "event_types"})
		if len(listItems) > 0 {
			items = append(items, listItems...)
		} else {
			input.eventTypes = &parsed
		}
	}
	if value, present := fields["enabled"]; present {
		parsed, item := asBool(value, []string{"body", "enabled"})
		if item != nil {
			items = append(items, *item)
		} else {
			input.enabled = &parsed
		}
	}
	if value, present := fields["http_config"]; present {
		parsed, configItems := asHTTPConfig(value)
		if len(configItems) > 0 {
			items = append(items, configItems...)
		} else {
			stored := toStorageConfig(parsed)
			input.httpConfig = &stored
		}
	}

	if len(items) > 0 {
		return updateInput{}, &httpx.ValidationFailure{Errors: items}, false
	}
	return input, nil, false
}

// namesKnownField reports whether the body carried at least one field the update
// model declares. Unknown keys are ignored, as the reference's model ignores
// them.
func namesKnownField(fields map[string]json.RawMessage) bool {
	for name := range fields {
		if name == "url" || name == "secret" || name == "event_types" || name == "enabled" || name == "http_config" {
			return true
		}
	}
	return false
}

// decodeWebhookBody reads the request body and decodes it into both the raw
// per-field form and a generic object (the input the framework reports a missing
// field against). A body the reference's model refuses is answered from the
// shared walk's items, which carry the vocabulary, location and input the
// reference serves; sample names the route's own model, so the walk also
// resolves when the request was dispatched without a route pattern.
func decodeWebhookBody(r *http.Request, sample any) (map[string]json.RawMessage, map[string]any, *httpx.ValidationFailure) {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, httpx.BodyDecodeFailure(err, body)
	}
	httpx.RecordUnknownBodyFields(r, body, sample)
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, nil, httpx.BodyDecodeFailure(httpx.ErrEmptyBody, body)
	}
	// The walk reports every value the model refuses — a body that is not an
	// object, a `null` on a field that does not admit one, a missing required
	// field — so anything it reports is the answer, and the field readers below
	// are only reached for a body the reference accepts.
	if items := httpx.ValidationItemsAgainstRequest(r, sample, body); len(items) > 0 {
		return nil, nil, &httpx.ValidationFailure{Errors: items}
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, nil, httpx.BodyDecodeFailure(err, body)
	}
	var generic map[string]any
	_ = json.Unmarshal(body, &generic)
	return raw, generic, nil
}

// createBodySample declares the fields the create body carries, so the shared
// decoder can report an unknown key without a per-route list and can describe
// the body when no route pattern was matched.
type createBodySample struct {
	URL        string         `json:"url"`
	Secret     *string        `json:"secret"`
	EventTypes []string       `json:"event_types"`
	Enabled    *bool          `json:"enabled"`
	HTTPConfig map[string]any `json:"http_config"`
}

// updateBodySample declares the fields the partial-update body carries. Every
// member is nullable in the reference's model, which is what the sample's
// pointer members describe.
type updateBodySample struct {
	URL        *string        `json:"url"`
	Secret     *string        `json:"secret"`
	EventTypes []string       `json:"event_types"`
	Enabled    *bool          `json:"enabled"`
	HTTPConfig map[string]any `json:"http_config"`
}

// typeItem builds a wrong-type item with the wording the reference uses. It
// differs from the shared helper only for the container and model kinds, whose
// reference wording the shared type vocabulary does not carry.
func typeItem(kind string, loc []string, input any) httpx.ValidationError {
	msg := httpx.TypeMessage(kind)
	switch kind {
	case "list_type":
		msg = "Input should be a valid list"
	case "dict_type":
		msg = "Input should be a valid dictionary"
	case "model_attributes_type":
		msg = "Input should be a valid dictionary or object to extract fields from"
	}
	return httpx.ValidationItem(kind, loc, msg, input, nil)
}

// isNull reports whether a raw JSON value is the literal null.
func isNull(raw json.RawMessage) bool {
	return strings.TrimSpace(string(raw)) == "null"
}

// decodeAny reads a raw value into the generic shape the framework reports as a
// rejected field's input.
func decodeAny(raw json.RawMessage) any {
	var value any
	_ = json.Unmarshal(raw, &value)
	return value
}

// requireString validates a required string field, returning the field's item
// for an absent ("missing") or wrongly-typed value.
func requireString(name string, fields map[string]json.RawMessage, body any) (string, *httpx.ValidationError) {
	value, present := fields[name]
	if !present {
		item := httpx.Missing([]string{"body", name}, body)
		return "", &item
	}
	return asString(value, []string{"body", name})
}

// asString validates a string-valued field, distinguishing a null (which the
// framework reports as a string_type failure) from a non-string value.
func asString(raw json.RawMessage, loc []string) (string, *httpx.ValidationError) {
	if isNull(raw) {
		item := typeItem("string_type", loc, nil)
		return "", &item
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		item := typeItem("string_type", loc, decodeAny(raw))
		return "", &item
	}
	return value, nil
}

// asStringArray validates a list-of-strings field, reporting the item for each
// rejected element.
func asStringArray(raw json.RawMessage, loc []string) ([]string, []httpx.ValidationError) {
	if isNull(raw) {
		return nil, []httpx.ValidationError{typeItem("list_type", loc, nil)}
	}
	var elements []json.RawMessage
	if err := json.Unmarshal(raw, &elements); err != nil {
		return nil, []httpx.ValidationError{typeItem("list_type", loc, decodeAny(raw))}
	}
	out := make([]string, 0, len(elements))
	var items []httpx.ValidationError
	for index, element := range elements {
		elementLoc := append(append([]string{}, loc...), strconv.Itoa(index))
		if text, item := asString(element, elementLoc); item != nil {
			items = append(items, *item)
		} else {
			out = append(out, text)
		}
	}
	if len(items) > 0 {
		return nil, items
	}
	return out, nil
}

// asBool validates a boolean field with the framework's lax coercion: a JSON
// bool is taken as-is, and the lax spelling set is accepted for a string.
func asBool(raw json.RawMessage, loc []string) (bool, *httpx.ValidationError) {
	if isNull(raw) {
		item := typeItem("bool_type", loc, nil)
		return false, &item
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err == nil {
		return value, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		switch strings.ToLower(text) {
		case "true", "1", "yes", "on", "t", "y":
			return true, nil
		case "false", "0", "no", "off", "f", "n":
			return false, nil
		default:
			item := typeItem("bool_parsing", loc, text)
			return false, &item
		}
	}
	if value, ok := decodeAny(raw).(float64); ok && value == math.Trunc(value) {
		// A number other than 0 or 1 is one the framework cannot interpret as a
		// boolean, which it reports as a parsing failure worded that way.
		item := typeItem("bool_parsing", loc, decodeAny(raw))
		return false, &item
	}
	item := typeItem("bool_type", loc, decodeAny(raw))
	return false, &item
}

// asHTTPConfig validates the nested http_config object against the reference's
// WebhookHttpConfig model: method and the string maps are strings, and
// timeout_seconds is an integer.
func asHTTPConfig(raw json.RawMessage) (HTTPConfig, []httpx.ValidationError) {
	config := defaultHTTPConfig()
	if isNull(raw) {
		item := typeItem("model_attributes_type", []string{"body", "http_config"}, nil)
		return config, []httpx.ValidationError{item}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		item := typeItem("model_attributes_type", []string{"body", "http_config"}, decodeAny(raw))
		return config, []httpx.ValidationError{item}
	}
	var items []httpx.ValidationError
	if value, present := fields["method"]; present {
		if method, item := asString(value, []string{"body", "http_config", "method"}); item != nil {
			items = append(items, *item)
		} else {
			config.Method = method
		}
	}
	if value, present := fields["timeout_seconds"]; present {
		if seconds, item := asInt(value, []string{"body", "http_config", "timeout_seconds"}); item != nil {
			items = append(items, *item)
		} else {
			config.TimeoutSeconds = seconds
		}
	}
	if value, present := fields["headers"]; present {
		if headers, headerItems := asStringMap(value, []string{"body", "http_config", "headers"}); len(headerItems) > 0 {
			items = append(items, headerItems...)
		} else {
			config.Headers = headers
		}
	}
	if value, present := fields["params"]; present {
		if params, paramItems := asStringMap(value, []string{"body", "http_config", "params"}); len(paramItems) > 0 {
			items = append(items, paramItems...)
		} else {
			config.Params = params
		}
	}
	return config, items
}

// asInt validates an integer field with the framework's lax coercion: a whole
// number is taken as-is, a number with a fractional part is `int_from_float`, a
// boolean is read as 1 or 0, and a string is read the way the integer parser
// reads one (a string it will not parse is `int_parsing`). Any other kind is
// the wrong type outright.
func asInt(raw json.RawMessage, loc []string) (int, *httpx.ValidationError) {
	if isNull(raw) {
		item := typeItem("int_type", loc, nil)
		return 0, &item
	}
	var boolean bool
	if err := json.Unmarshal(raw, &boolean); err == nil {
		if boolean {
			return 1, nil
		}
		return 0, nil
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		if number != float64(int(number)) {
			item := typeItem("int_from_float", loc, decodeAny(raw))
			return 0, &item
		}
		return int(number), nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err == nil {
		if value, ok := httpx.LaxInteger(text); ok {
			return value, nil
		}
		item := typeItem("int_parsing", loc, text)
		return 0, &item
	}
	item := typeItem("int_type", loc, decodeAny(raw))
	return 0, &item
}

// asStringMap validates a string-valued map field, reporting the item for a
// non-object or a non-string member.
func asStringMap(raw json.RawMessage, loc []string) (map[string]string, []httpx.ValidationError) {
	if isNull(raw) {
		return nil, []httpx.ValidationError{typeItem("dict_type", loc, nil)}
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, []httpx.ValidationError{typeItem("dict_type", loc, decodeAny(raw))}
	}
	out := make(map[string]string, len(fields))
	var items []httpx.ValidationError
	for key, value := range fields {
		elementLoc := append(append([]string{}, loc...), key)
		if text, item := asString(value, elementLoc); item != nil {
			items = append(items, *item)
		} else {
			out[key] = text
		}
	}
	if len(items) > 0 {
		return nil, items
	}
	return out, nil
}
