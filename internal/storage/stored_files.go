package storage

import (
	"errors"
	"fmt"
	"strings"
)

// Operation type identifiers for the archive transfer family. They are stored
// in the operation record so a consumer can tell which kind of work it is
// claiming.
const (
	OperationTypeDocumentExport = "document_export"
	OperationTypeDocumentImport = "document_import"
	OperationTypeTransferExport = "transfer_export"
	OperationTypeTransferImport = "transfer_import"
	OperationTypeClone          = "clone"
)

// OperationPayloadKeyStoredFile is the operation payload field carrying the
// storage key of a stashed upload. Only the key travels in the queue; the
// payload itself lives in stored-file storage.
const OperationPayloadKeyStoredFile = "stored_file_key"

// StoredFile is an opaque byte payload addressed by a caller-supplied key. The
// transfer family uses it to stash an uploaded archive so the queued operation
// carries a key rather than the bytes.
type StoredFile struct {
	// Key is the storage key. It is built by percent-encoding each segment so
	// a caller-chosen bank or document identifier cannot introduce a nesting
	// boundary or a parent-directory segment.
	Key string
	// ContentType is the declared media type of the payload.
	ContentType string
	// Data is the payload.
	Data []byte
}

// StoredFileKeyForBank builds the storage key for a payload scoped to one
// bank. Each segment is percent-encoded independently, so an identifier
// containing a separator or a parent-directory segment cannot escape its own
// prefix.
func StoredFileKeyForBank(bankID, name string) string {
	return "banks/" + encodeSegment(bankID) + "/" + encodeSegment(name)
}

// BankIDFromStoredFileKey recovers the owning bank identifier from a storage
// key. It reports false when the key is not in the bank-scoped layout, so a
// caller can answer not-found rather than guessing.
func BankIDFromStoredFileKey(key string) (string, bool) {
	segments := strings.Split(key, "/")
	if len(segments) < 3 || segments[0] != "banks" {
		return "", false
	}
	bankID, err := decodeSegment(segments[1])
	if err != nil || bankID == "" {
		return "", false
	}
	return bankID, true
}

// ErrInvalidStoredFileKey reports a key that cannot be resolved to a segment
// layout. It is reported as not-found by callers so a malformed key and an
// absent key are indistinguishable.
var ErrInvalidStoredFileKey = errors.New("invalid stored file key")

// ValidateStoredFileKey rejects a key that contains a parent-directory segment
// or an empty segment. It is the single guard every read path uses.
func ValidateStoredFileKey(key string) error {
	if key == "" {
		return ErrInvalidStoredFileKey
	}
	for _, segment := range strings.Split(key, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return fmt.Errorf("%w: %q", ErrInvalidStoredFileKey, segment)
		}
	}
	return nil
}

// encodeSegment percent-encodes a single path segment so separators and
// traversal sequences cannot survive into a key.
func encodeSegment(segment string) string {
	var b strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if isSegmentSafe(c) {
			b.WriteByte(c)
			continue
		}
		fmt.Fprintf(&b, "%%%02X", c)
	}
	return b.String()
}

// decodeSegment reverses encodeSegment.
func decodeSegment(segment string) (string, error) {
	var b strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		if c != '%' {
			b.WriteByte(c)
			continue
		}
		if i+2 >= len(segment) {
			return "", ErrInvalidStoredFileKey
		}
		var value byte
		if _, err := fmt.Sscanf(segment[i+1:i+3], "%02X", &value); err != nil {
			return "", ErrInvalidStoredFileKey
		}
		b.WriteByte(value)
		i += 2
	}
	return b.String(), nil
}

// isSegmentSafe reports whether a byte can appear literally in a key segment.
// Everything outside the unreserved set is encoded.
func isSegmentSafe(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	case c == '-', c == '_', c == '.':
		// A lone dot is safe; a dot used inside a traversal sequence is not,
		// because the separator itself is always encoded away.
		return true
	default:
		return false
	}
}
