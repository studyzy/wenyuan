package httpx

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
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
	WriteJSON(w, status, failure)
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
