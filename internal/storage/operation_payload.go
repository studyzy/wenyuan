package storage

import "encoding/json"

// marshalMetadata encodes a metadata map for the jsonb column. A nil map is
// stored as an empty object so the column predicate stays simple.
func marshalMetadata(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(metadata)
}

// executablePayload normalises an operation's payload the way the jsonb column
// does: a nil map is stored as an empty object, never as SQL NULL. Only the
// batch parent — recorded with a literal NULL payload — stays payload-less, and
// that absence is exactly the mark a claim reads as "nothing to run". Applying
// this at every executable insert keeps the in-memory table's claim predicate
// ("payload is present") equivalent to the postgres column's.
func executablePayload(payload map[string]any) map[string]any {
	if payload == nil {
		return map[string]any{}
	}
	return payload
}

// unmarshalResultMetadata decodes the result-metadata column. A null or empty
// value yields nil.
func unmarshalResultMetadata(raw []byte) (map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}
