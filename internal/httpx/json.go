package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
)

// EncodeOptions carries per-request response shaping controls.
type EncodeOptions struct {
	// DropNullFields removes explicit JSON null object fields from the encoded
	// response body.
	DropNullFields bool
}

// WriteJSON writes a JSON response with the configured shaping options applied.
// Callers that need an empty body (for example 204) must not use it.
func WriteJSON(w http.ResponseWriter, status int, payload any, opts EncodeOptions) {
	buf, err := encodeJSON(payload, opts)
	if err != nil {
		WriteError(w, &HTTPError{Status: KindInternal, Detail: DetailInternal}, opts)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func encodeJSON(payload any, opts EncodeOptions) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	if opts.DropNullFields {
		enc.SetEscapeHTML(false)
	}
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	raw := bytes.TrimRight(buf.Bytes(), "\n")
	if !opts.DropNullFields {
		return raw, nil
	}
	return dropNullFields(raw)
}

// dropNullFields removes object members whose value is JSON null while
// preserving array elements. Values inside arrays are preserved because null
// list elements are meaningful for callers.
func dropNullFields(raw []byte) ([]byte, error) {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, err
	}
	cleaned := pruneNullObjects(decoded)
	return json.Marshal(cleaned)
}

func pruneNullObjects(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			if member == nil {
				continue
			}
			out[key] = pruneNullObjects(member)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, member := range v {
			out[i] = pruneNullObjects(member)
		}
		return out
	default:
		return v
	}
}

// WriteError emits a shared error envelope with the detail field.
func WriteError(w http.ResponseWriter, err error, opts EncodeOptions) {
	var failure *ValidationFailure
	if errors.As(err, &failure) {
		WriteValidationFailure(w, failure, opts)
		return
	}
	httpErr := asHTTPError(err)
	for name, values := range httpErr.Header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	if httpErr.Payload != nil {
		WriteJSON(w, httpErr.Status, PayloadEnvelope{Detail: httpErr.Payload}, opts)
		return
	}
	WriteJSON(w, httpErr.Status, ErrorEnvelope{Detail: httpErr.Detail}, opts)
}

// WriteValidationFailure emits the structured validation error-item body with
// the unprocessable-entity status. A validation item is contract surface rather
// than a model whose nulls are optional: the reference always carries loc, msg,
// type and input, so a null input is rendered instead of dropped.
func WriteValidationFailure(w http.ResponseWriter, failure *ValidationFailure, opts EncodeOptions) {
	opts.DropNullFields = false
	WriteJSON(w, KindUnprocessable, failure, opts)
}

func asHTTPError(err error) *HTTPError {
	if err == nil {
		return &HTTPError{Status: KindInternal, Detail: DetailInternal}
	}
	if httpErr, ok := err.(*HTTPError); ok {
		return httpErr
	}
	// A validation failure is an error like any other, so a caller that hands
	// one to the shared writer gets the structured body and the status the
	// reference uses for it.
	var failure *ValidationFailure
	if errors.As(err, &failure) {
		return &HTTPError{Status: KindUnprocessable, Detail: failure.Error()}
	}
	return &HTTPError{Status: KindInternal, Detail: DetailInternal}
}
