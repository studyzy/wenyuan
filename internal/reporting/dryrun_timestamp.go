package reporting

import (
	"bytes"
	"encoding/json"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/validation"
)

// dryRunTimestamp validates a dry-run request's `timestamp` member the way the
// reference's request model does. The member is a plain `datetime` there, so the
// framework parses it: a string that is not a datetime is a structured 422, and a
// number is a duration in seconds, which the dry run has no use for but must not
// refuse. The reference ignores the value's own instant here — the dry run keeps
// only what an ISO-8601 string names — so this reports whether the caller sent
// one and, when it is a string, hands it back for the extraction's reference time.
//
// raw is the member's raw JSON: nil when it was absent, the literal `null` when it
// was sent as JSON null.
func dryRunTimestamp(raw json.RawMessage) (string, bool, *httpx.ValidationFailure) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false, nil
	}
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", false, nil
	}
	switch typed := value.(type) {
	case nil:
		return "", false, nil
	case string:
		if item := validation.TimestampFailure(typed); item != nil {
			item.Loc = []string{"body", "timestamp"}
			return "", false, &httpx.ValidationFailure{Errors: []httpx.ValidationError{*item}}
		}
		return typed, true, nil
	case json.Number:
		// A number is a duration in seconds to the framework, which accepts any
		// numeric value; a dry run derives the same reference time either way.
		return "", false, nil
	default:
		loc := []string{"body", "timestamp"}
		return "", false, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValidationItem("datetime_type", loc,
				"Input should be a valid datetime", value, nil),
		}}
	}
}

// decodedRequestBody is the body as a caller's JSON value, which the framework
// carries as a missing request-level member's input. A body that is not an object
// leaves the empty object, which is the shape an absent body has.
func decodedRequestBody(raw []byte) any {
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil || value == nil {
		return map[string]any{}
	}
	return value
}
