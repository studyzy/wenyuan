package storage

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

// newID returns a random opaque identifier used by the in-process store.
func newID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "00000000000000000000000000000000"
	}
	return hex.EncodeToString(buf)
}

// newUUID returns a random RFC 4122 version-4 shaped identifier with the
// canonical 8-4-4-4-12 hex layout, used where the public contract validates an
// identifier's shape.
func newUUID() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
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
