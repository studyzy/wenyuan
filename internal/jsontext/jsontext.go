// Package jsontext reads a JSON value that a producer may have written in
// either of two encodings: a decoded value, or the JSON text of that value.
//
// A database column of type jsonb hands back an object through one driver and
// a JSON string through another, and an archive records which convention its
// writer used. Every consumer that reads such a value has to accept both, or
// one producer's rows silently fall back to a default. This package holds that
// one rule so the readers do not drift apart.
package jsontext

import (
	"encoding/json"
	"strings"
)

// Value reads one value that may have been written in either JSON encoding.
//
// A value this service wrote is a decoded object or array where the column is
// jsonb. The reference writes the same column as its JSON *text* and declares
// `bank_rows_json_encoding: "serialized"` in the manifest, so an archive it
// produced carries `"{\"mode\": \"delta\"}"` where ours carries the object.
// Both have to read the same, or every jsonb field of an imported reference
// archive silently falls back to its default — which is what a trigger, a bank
// config or a history payload losing its contents looks like.
//
// Only a string that opens as a JSON object or array is parsed, so a text
// column that happens to hold braces keeps its literal value rather than being
// reinterpreted as structure.
func Value(value any) any {
	text, isText := value.(string)
	if !isText {
		return value
	}
	trimmed := strings.TrimSpace(text)
	if !strings.HasPrefix(trimmed, "{") && !strings.HasPrefix(trimmed, "[") {
		return value
	}
	var decoded any
	if err := json.Unmarshal([]byte(trimmed), &decoded); err != nil {
		return value
	}
	return decoded
}
