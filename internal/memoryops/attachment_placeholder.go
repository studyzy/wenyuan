package memoryops

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
)

// The canonical attachment placeholder is written in the Unicode
// mathematical-brackets block. Those delimiters are chosen by the reference so
// that a placeholder survives text sanitisation, is not a separator the chunker
// splits on, and is rare enough in prose that it collides with authored text
// only by forgery.
const (
	placeholderOpen  = "\u27e6" // ⟦
	placeholderClose = "\u27e7" // ⟧
	placeholderBody  = "hs-att:"
)

// AttachmentPlaceholder renders the token that stands in for an attachment
// whose bytes are stored under the given short id. The same bytes always hash
// to the same short id, so re-retaining identical content yields an identical
// placeholder rather than a fresh one.
func AttachmentPlaceholder(shortID string) string {
	return placeholderOpen + placeholderBody + shortID + placeholderClose
}

// AttachmentShortID is the prefix of a content digest that identifies an
// attachment. Accepts either a full digest or an already-shortened id, so a
// caller that holds one need not know which.
func AttachmentShortID(hash string) string {
	if len(hash) <= attachmentShortIDLength {
		return hash
	}
	return hash[:attachmentShortIDLength]
}

// attachmentHashOf returns the digest of decoded attachment bytes, so an inline
// attachment block is content-addressed exactly as an uploaded file is.
func attachmentHashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// decodeAttachmentData reads a base64 attachment payload. The reference sends
// standard padded base64, but an unpadded payload is accepted too rather than
// failing the whole retain over a missing '='.
func decodeAttachmentData(encoded string) ([]byte, bool) {
	if data, err := base64.StdEncoding.DecodeString(encoded); err == nil {
		return data, true
	}
	if data, err := base64.RawStdEncoding.DecodeString(encoded); err == nil {
		return data, true
	}
	return nil, false
}
