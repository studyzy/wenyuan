package tenant

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// A team fact's identity is its normalized text, so two facts that differ only
// in spacing are the same fact. It lives here rather than in the merge package
// because the ledger writes the key and the storage layer reads it, while the
// merge package is a leaf the storage layer must not depend on.

// Normalize folds away the differences that must not create two team facts:
// surrounding whitespace and runs of internal whitespace.
//
// It deliberately does not lowercase or strip punctuation. Two facts that differ
// in case or punctuation are different statements as far as the model's output
// is concerned, and silently merging them would make the ledger claim a
// provenance the text does not have.
func Normalize(text string) string {
	return strings.Join(strings.Fields(text), " ")
}

// MergeKey is the identity of a team fact: the hex SHA-256 of its normalized
// text.
//
// It does not include the fact's sources: a team fact whose wording did not
// change keeps its row (and so its id, and so its observations) even when the
// members who produced it changed.
func MergeKey(text string) string {
	sum := sha256.Sum256([]byte(Normalize(text)))
	return hex.EncodeToString(sum[:])
}
