package storage

import "encoding/json"

// payloadMetadataKey is the wrapper key under which an operation's payload is
// stored inside the existing result_metadata column. The payload travels there
// because the schema has no dedicated payload column and this area must not
// change the schema.
const payloadMetadataKey = "payload"

// marshalMetadata encodes a metadata map for the jsonb column. A nil map is
// stored as an empty object so the column predicate stays simple.
func marshalMetadata(metadata map[string]any) ([]byte, error) {
	if metadata == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(metadata)
}

// unmarshalMetadata decodes the jsonb column back into the payload and the
// remaining result metadata. A null or empty value yields both as nil.
func unmarshalMetadata(raw []byte) (map[string]any, map[string]any, error) {
	if len(raw) == 0 {
		return nil, nil, nil
	}
	var all map[string]any
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, nil, err
	}
	if len(all) == 0 {
		return nil, nil, nil
	}
	nested, ok := all[payloadMetadataKey]
	if !ok {
		return nil, all, nil
	}
	delete(all, payloadMetadataKey)
	payload, _ := nested.(map[string]any)
	if len(all) == 0 {
		all = nil
	}
	return payload, all, nil
}
