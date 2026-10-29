package storage

import (
	"strings"
	"unicode/utf8"
)

// ValidBankID reports whether a bank identifier or alias satisfies the
// admission rules: non-empty, within the byte budget of valid UTF-8, free of
// control characters, and not prefixed with the reserved '_' (which a
// store-owned backend keeps for its own bookkeeping objects — bank_utils.py
// refuses it for every new bank, alias and rename target).
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
	if strings.HasPrefix(id, "_") {
		return false
	}
	return !containsControl(id)
}

// containsControl reports whether the identifier carries a Unicode `Cc`
// character: the C0 controls (U+0000-U+001F), DEL (U+007F) and the C1 controls
// (U+0080-U+009F), which is what the reference rejects.
func containsControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
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
