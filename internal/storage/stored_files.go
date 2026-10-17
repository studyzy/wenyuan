package storage

import (
	"errors"
	"fmt"
	"strings"
)

// Operation type identifiers. They are stored in the operation record so a
// consumer can tell which kind of work it is claiming.
//
// The string is the wire contract: it is what `operation_type` carries in the
// operations API, what the frontend filters and labels on, and what the
// service's own queries match against. The Go identifier is free to differ.
const (
	OperationTypeDocumentExport = "export_documents"
	OperationTypeDocumentImport = "import_documents"
	OperationTypeTransferExport = "export_bank"
	OperationTypeTransferImport = "import_bank"
	OperationTypeClone          = "clone_bank"
	// OperationTypeConsolidate is the consolidation pass, which folds related
	// memories into observations.
	OperationTypeConsolidate = "consolidation"
	// OperationTypeRefreshMentalModel regenerates one mental model from its
	// source query. It is the operation the scheduled refresh enqueues, and it
	// carries a serialization key so two refreshes of the same model cannot run
	// beside each other.
	OperationTypeRefreshMentalModel = "refresh_mental_model"
	// OperationTypeWebhookDelivery delivers one webhook event. It is a queued
	// operation so delivery retries and the delivery record share the queue's
	// lifecycle.
	OperationTypeWebhookDelivery = "webhook_delivery"
	// OperationTypeFileConvertRetain, OperationTypeGraphMaintenance and
	// OperationTypeVectorIndexMaintenance complete the reference's async
	// operation vocabulary. This service does that work without a queue row of
	// its own — an upload is converted at ingress and queued as a retain, and
	// graph and vector-index upkeep runs as a periodic maintenance job rather
	// than an operation — so nothing here enqueues them. They are declared so
	// the operation-type vocabulary this service serves, and the frontend that
	// filters on it, stays in step with the reference.
	OperationTypeFileConvertRetain      = "file_convert_retain"
	OperationTypeGraphMaintenance       = "graph_maintenance"
	OperationTypeVectorIndexMaintenance = "vector_index_maintenance"
)

// ExportOperationTypes are the operation types that own a stored archive,
// named in result_metadata.storage_key. The retention prune removes those
// archives with their rows so a blob never outlives its only handle; the
// operation is the archive's sole reference, so no key in this list means no
// blob to sweep.
var ExportOperationTypes = []string{OperationTypeDocumentExport, OperationTypeTransferExport}

// OperationPayloadKeyStoredFile is the operation payload field carrying the
// storage key of a stashed upload. Only the key travels in the queue; the
// payload itself lives in stored-file storage. The name is the reference's own
// task-payload member, so a restored operation row reads the same on both.
const OperationPayloadKeyStoredFile = "storage_key"

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

// AttachmentStorageKey is where one document's copy of an attachment lives.
// The document is part of the key because an attachment belongs to its
// document: two documents carrying the same image hold two copies, so deleting
// either one deletes exactly its own bytes without having to ask whether some
// other document still needs them. The document id is encoded as one segment,
// so an id containing a separator cannot nest under another document's prefix.
func AttachmentStorageKey(bankID, documentID, attachmentHash string) string {
	return "banks/" + encodeSegment(bankID) +
		"/documents/" + encodeSegment(documentID) +
		"/attachments/sha256-" + encodeSegment(attachmentHash)
}

// BankIDFromStoredFileKey recovers the owning bank identifier from a storage
// key. It reports false when the key is not in the bank-scoped layout, so a
// caller can answer not-found rather than guessing.
//
// Two layouts carry the bank identifier: the tenant-prefixed
// "tenants/{schema}/banks/{bank_id}/..." form and the
// "banks/{bank_id}/..." form written before keys carried the tenant. Both are
// resolved here so a caller replaying the reference's keys still authorizes.
func BankIDFromStoredFileKey(key string) (string, bool) {
	segments := strings.Split(key, "/")
	switch {
	case len(segments) > 4 && segments[0] == "tenants" && segments[2] == "banks" && segments[3] != "":
		return decodeBankSegment(segments[3])
	case len(segments) > 2 && segments[0] == "banks" && segments[1] != "":
		return decodeBankSegment(segments[1])
	default:
		return "", false
	}
}

// decodeBankSegment percent-decodes a bank segment and reports whether it
// names a bank.
func decodeBankSegment(segment string) (string, bool) {
	bankID, err := decodeSegment(segment)
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

// contentTypeForKey infers the media type a stored payload is served with.
func contentTypeForKey(key string) string {
	if strings.HasSuffix(key, ".zip") {
		return "application/zip"
	}
	return "application/octet-stream"
}
