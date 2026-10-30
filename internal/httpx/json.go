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
	// Every 503 advertises a retry delay, which the reference pairs with the
	// status without exception: its `_store_unavailable` builds the 503 and the
	// `Retry-After` together (api/http.py:367-371), its app-level handler for a
	// store that shed the write does the same (api/http.py:5801-5806), and the
	// admission shed carries one too (api/http.py:5712-5717). A site here that
	// chooses its own value sets it above and keeps it; one that does not —
	// the storage-error mappers, which have no handler state to read a
	// configured value from — gets the reference's default rather than a 503
	// with no hint at all.
	if httpErr.Status == KindUnavailable && w.Header().Get(HeaderRetryAfter) == "" {
		w.Header().Set(HeaderRetryAfter, strconv.Itoa(RetryAfterSeconds))
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
// orderValueAsWritten rebuilds a rendered value so every object in it renders in
// the member order the client wrote. The reference echoes the request's own
// insertion-ordered mappings at every depth, so both the whole body and a single
// rejected member's object come back as they were sent; a Go map would sort them.
// A value with no recorded counterpart is returned unchanged.
func orderValueAsWritten(value any, recorded any) any {
	switch rendered := value.(type) {
	case map[string]any:
		entry, ok := recorded.(orderMap)
		if !ok || len(entry) != len(rendered) {
			return value
		}
		members := make(orderMap, 0, len(entry))
		for _, member := range entry {
			current, ok := rendered[member.key]
			if !ok {
				return value
			}
			members = append(members, orderedEntry{
				key:   member.key,
				value: orderedValue{raw: orderValueAsWritten(current, member.value.raw)},
			})
		}
		return members
	case []any:
		list, ok := recorded.([]any)
		if !ok || len(list) != len(rendered) {
			return value
		}
		ordered := make([]any, len(rendered))
		for i := range rendered {
			if entry, ok := list[i].(orderedValue); ok {
				ordered[i] = orderedValue{raw: orderValueAsWritten(rendered[i], entry.raw)}
				continue
			}
			ordered[i] = orderValueAsWritten(rendered[i], list[i])
		}
		return ordered
	}
	return value
}

// recordedMember returns the recorded value an item's loc points at, so a
// rejected member's own object renders in the order it was sent.
func recordedMember(recorded any, loc []string) any {
	current := recorded
	// The loc starts at the request body, which the recording already is.
	if len(loc) > 0 && loc[0] == "body" {
		loc = loc[1:]
	}
	for _, segment := range loc {
		switch node := current.(type) {
		case orderMap:
			found := false
			for _, member := range node {
				if member.key == segment {
					current = member.value.raw
					found = true
					break
				}
			}
			if !found {
				return nil
			}
		case orderedValue:
			current = node.raw
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(node) {
				return nil
			}
			current = node[index]
			if entry, ok := current.(orderedValue); ok {
				current = entry.raw
			}
		default:
			return nil
		}
	}
	return current
}

// sameBody reports whether a rendered body map is the body the failure echoes,
// rather than one member's own value.
func sameBody(input, body map[string]any) bool {
	if body == nil {
		return true
	}
	if len(input) != len(body) {
		return false
	}
	for key := range body {
		if _, ok := input[key]; !ok {
			return false
		}
	}
	return true
}

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
	ordered.Echo = nil
	patched := false
	for i, item := range ordered.Errors {
		input, isObject := item.Input.(map[string]any)
		if !isObject {
			continue
		}
		// The recorded order replaces the map only when the map IS the body: a
		// missing required member echoes the whole request, while a rejected
		// member's item echoes that member's own object, which must keep it.
		if sameBody(input, failure.Echo) {
			// The whole request body: render it in the order the client wrote.
			ordered.Errors[i].Input = failure.BodyOrder
			patched = true
			continue
		}
		// A member's own value: it keeps the order it was written in too.
		ordered.Errors[i].Input = orderValueAsWritten(input, recordedMember(failure.BodyOrder, item.Loc))
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
