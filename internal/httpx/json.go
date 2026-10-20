package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
)

// WriteJSON writes a JSON response. Callers that need an empty body (for
// example 204) must not use it.
func WriteJSON(w http.ResponseWriter, status int, payload any) {
	buf, err := encodeJSON(payload)
	if err != nil {
		WriteError(w, &HTTPError{Status: KindInternal, Detail: DetailInternal})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	// The reference's JSON response sets Content-Length from the serialised
	// body, so a large payload is framed with a length rather than chunked.
	// Without this, writing the body after an explicit WriteHeader leaves the
	// length unknown and the standard library falls back to chunked encoding.
	w.Header().Set("Content-Length", strconv.Itoa(len(buf)))
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func encodeJSON(payload any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	// The reference emits `<`, `>` and `&` verbatim in strings (its framework
	// does not HTML-escape), so a detail sentence like "must be >= 1" must not
	// come back as "\u003e".
	enc.SetEscapeHTML(false)
	if err := enc.Encode(payload); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// WriteError emits a shared error envelope with the detail field.
func WriteError(w http.ResponseWriter, err error) {
	var failure *ValidationFailure
	if errors.As(err, &failure) {
		WriteValidationFailure(w, failure)
		return
	}
	httpErr := asHTTPError(err)
	for name, values := range httpErr.Header {
		for _, v := range values {
			w.Header().Add(name, v)
		}
	}
	if httpErr.Plain {
		// The reference's unhandled-exception fallback: the ASGI server answers the
		// reason phrase as text, not through the API's error envelope.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(httpErr.Status)
		_, _ = w.Write([]byte(http.StatusText(httpErr.Status)))
		return
	}
	if httpErr.Payload != nil {
		WriteJSON(w, httpErr.Status, PayloadEnvelope{Detail: httpErr.Payload})
		return
	}
	WriteJSON(w, httpErr.Status, ErrorEnvelope{Detail: httpErr.Detail})
}

// WriteValidationFailure emits the structured validation error-item body with
// the unprocessable-entity status. A validation item is contract surface: the
// reference always carries loc, msg, type and input, so a null input is
// rendered rather than omitted.
func WriteValidationFailure(w http.ResponseWriter, failure *ValidationFailure) {
	status := failure.Status
	if status == 0 {
		status = KindUnprocessable
	}
	WriteJSON(w, status, orderValidationInputs(failure))
}

// orderValidationInputs rewrites a failure's items so the body they echo renders
// in the order the client wrote its members.
//
// The reference's framework reports `input` as the request body it received, an
// insertion-ordered mapping, so `{"b":1,"a":2}` comes back as written. Go's map
// sorts its keys on the way out, which would report a body in an order the client
// never sent — visible to anyone comparing two responses byte for byte.
func orderValidationInputs(failure *ValidationFailure) *ValidationFailure {
	if failure == nil || failure.BodyOrder == nil || len(failure.Errors) == 0 {
		return failure
	}
	if failure.Detail != "" {
		// A failure that answers with a plain detail carries no items to echo.
		return failure
	}
	if failure.Status == KindInternal {
		return failure
	}
	ordered := *failure
	ordered.Errors = make([]ValidationError, len(failure.Errors))
	copy(ordered.Errors, failure.Errors)
	patched := false
	for i, item := range ordered.Errors {
		if _, isObject := item.Input.(map[string]any); !isObject {
			continue
		}
		ordered.Errors[i].Input = failure.BodyOrder
		patched = true
	}
	if !patched {
		return failure
	}
	return &ordered
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
