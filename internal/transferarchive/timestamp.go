package transferarchive

import (
	"time"
)

// ParseTime reads one archive timestamp string back into a time.
//
// The archive's pydantic-serialized sections carry RFC 3339 strings with a 'Z'
// offset ("2026-10-16T15:26:20.622005Z"); an older producer or a hand-written
// archive may use a numeric offset instead, and a few sections that the
// reference emits through json.dumps use '+00:00'. All of those are valid
// RFC 3339, so one layout set covers them. A nil or unparsable value reads as
// nil, matching how the reader treats every other optional archive field.
func ParseTime(value *string) *time.Time {
	if value == nil {
		return nil
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, *value); err == nil {
			utc := parsed.UTC()
			return &utc
		}
	}
	return nil
}

// ParseTimeValue is ParseTime for a value held as an any, which is the shape the
// archive's data and history rows decode into.
func ParseTimeValue(value any) *time.Time {
	switch typed := value.(type) {
	case nil:
		return nil
	case time.Time:
		utc := typed.UTC()
		return &utc
	case *time.Time:
		if typed == nil {
			return nil
		}
		utc := typed.UTC()
		return &utc
	case string:
		return ParseTime(&typed)
	}
	return nil
}
