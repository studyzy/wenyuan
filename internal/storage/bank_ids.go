package storage

import (
	"strings"
	"unicode/utf8"
)

// ValidBankID reports whether a bank identifier or alias satisfies the
// admission rules: non-empty, within the byte budget of valid UTF-8, and free
// of control characters.
func ValidBankID(id string) bool {
	if id == "" {
		return false
	}
	if len(id) > BankIDMaxBytes {
		return false
	}
	if !utf8.ValidString(id) {
		return false
	}
	return !containsControl(id)
}

func containsControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// matchesSearch reports whether a search term matches a candidate
// case-insensitively as a substring. An empty search matches everything.
func matchesSearch(candidate, search string) bool {
	if search == "" {
		return true
	}
	return strings.Contains(strings.ToLower(candidate), strings.ToLower(search))
}
