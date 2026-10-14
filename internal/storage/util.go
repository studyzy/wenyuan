package storage

import (
	"crypto/rand"
	"encoding/hex"
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

// searchVectorSQL is the expression that maintains memory_units.search_vector.
//
// The reference keeps this column rather than generating it, so every write
// that changes a unit's searched text has to set it; a row written without it
// is invisible to the reference's full-text arm. The value is the unit's text
// under the reference's default text-search language.
const searchVectorSQL = `to_tsvector('english', COALESCE($3, ''))`
