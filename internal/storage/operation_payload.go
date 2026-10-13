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
