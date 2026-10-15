package storage

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strconv"
	"strings"
)

// newID returns a random opaque identifier used by the in-process store.
// newID returns a random RFC 4122 version-4 shaped identifier.
//
// The reference declares its primary keys as uuid, so an identifier that is
// not uuid-shaped cannot be stored in one. The canonical 8-4-4-4-12 layout is
// what both the database accepts and the spot checks accept.
func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "00000000-0000-4000-8000-000000000000"
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// NewDocumentID mints a document identifier. The retain ingress uses it when a
// multimodal item names no document, because an attachment row keys on the
// document that owns it and the id must exist before the bytes are stored.
func NewDocumentID() string { return newID() }

// newUUID returns a random RFC 4122 version-4 shaped identifier with the
// canonical 8-4-4-4-12 hex layout, used where the public contract validates an
// identifier's shape.
func newUUID() string {
	return newID()
}

func lower(s string) string { return strings.ToLower(s) }

func index(haystack, needle string) int {
	if needle == "" {
		return 0
	}
	if haystack == "" {
		return -1
	}
	if i := strings.Index(haystack, needle); i >= 0 {
		return i
	}
	return -1
}

// searchVectorExpr renders the expression that maintains
// memory_units.search_vector.
//
// The reference keeps this column rather than generating it, so every write
// that changes a unit's searched text has to set it; a row written without it
// is invisible to the reference's full-text arm. The value folds the unit's
// three searched sources under the default text-search language: its text, its
// context, and its text_signals (entity names and spelled-out event dates).
// Omitting any of them — and in particular building the vector from `text`
// alone — leaves a unit invisible to the entity- and date-enriched matches the
// keyword arm scores, so the published score comes out lower than the
// reference's.
//
// Each argument is the SQL for one source: a bind placeholder ($3), or a column
// reference for a source the statement does not change. A literal ” leaves the
// source out, which is what a write that carries no context or signals passes.
func searchVectorExpr(textParam, contextParam, signalsParam string) string {
	return "to_tsvector('english', COALESCE(" + textParam + ", '') || ' ' || COALESCE(" +
		contextParam + ", '') || ' ' || COALESCE(" + signalsParam + ", ''))"
}

// singleTagRename reports whether a retag is an unambiguous single-tag rename:
// exactly one tag removed and one added. Only then can a frozen observation
// scope be remapped without guessing intent; any other retag is left alone.
func singleTagRename(oldTags, newTags []string) (old, added string, ok bool) {
	oldSet := make(map[string]bool, len(oldTags))
	for _, tag := range oldTags {
		oldSet[tag] = true
	}
	newSet := make(map[string]bool, len(newTags))
	for _, tag := range newTags {
		newSet[tag] = true
	}
	removed := make([]string, 0, 1)
	addedTags := make([]string, 0, 1)
	for tag := range oldSet {
		if !newSet[tag] {
			removed = append(removed, tag)
		}
	}
	for tag := range newSet {
		if !oldSet[tag] {
			addedTags = append(addedTags, tag)
		}
	}
	if len(removed) != 1 || len(addedTags) != 1 {
		return "", "", false
	}
	return removed[0], addedTags[0], true
}

// renamedScopes carries a single-tag rename into a unit's frozen
// observation_scopes, returning the rewritten JSON or nil when nothing changes.
//
// The explicit list form ([[...], [...]]) freezes tag strings at retain time,
// so a retag that renames project:old -> project:new must rewrite them or
// consolidation rebuilds the observations under the old tag. A scalar mode
// ("per_tag"/"combined"/"shared") re-derives from the unit's fresh tags and
// needs nothing, and a retag that is not an unambiguous rename is left as-is.
func renamedScopes(raw []byte, oldTags, newTags []string) []byte {
	if len(raw) == 0 {
		return nil
	}
	var scopes [][]string
	if err := json.Unmarshal(raw, &scopes); err != nil {
		return nil
	}
	old, added, ok := singleTagRename(oldTags, newTags)
	if !ok {
		return nil
	}
	changed := false
	out := make([][]string, len(scopes))
	for i, scope := range scopes {
		renamed := make([]string, len(scope))
		for j, tag := range scope {
			if tag == old {
				renamed[j] = added
				changed = true
			} else {
				renamed[j] = tag
			}
		}
		out[i] = renamed
	}
	if !changed {
		return nil
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}

// intFromMetadata reads an integer-valued metadata key, tolerating the numeric
// shapes a jsonb round trip produces (float64) as well as native ints and
// numeric strings.
func intFromMetadata(metadata map[string]any, key string) int {
	switch value := metadata[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		if n, err := value.Int64(); err == nil {
			return int(n)
		}
	case string:
		if n, err := strconv.Atoi(value); err == nil {
			return n
		}
	}
	return 0
}

// stringFromMetadata reads a string-valued metadata key, returning "" when the
// key is absent or not a string.
func stringFromMetadata(metadata map[string]any, key string) string {
	if value, ok := metadata[key].(string); ok {
		return value
	}
	return ""
}
