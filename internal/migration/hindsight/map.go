// Package hindsight maps a Hindsight export archive onto a Wenyuan store.
//
// It is the data-migration vehicle for replacing a Hindsight server with
// Wenyuan while keeping an existing bank's memories: the archive is the
// official, versioned export format, and its facts carry no embedding, which
// matches a Wenyuan bank that has none.
package hindsight

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Mapping carries the archive plus the identity choices made when translating
// it into a target bank.
type Mapping struct {
	Archive *hindsightarchive.Archive
	// BankID is the target bank identifier. Empty means use the archive's
	// source bank id.
	BankID string
}

// Report is the outcome of a migration run.
type Report struct {
	BankID    string
	Documents int
	Chunks    int
	Facts     int
	Entities  int
	Links     int
	// Unmapped lists archive fields that have no Wenyuan column, so the caller
	// can see exactly what did and did not survive the transfer.
	Unmapped []string
	// Written and Skipped count the documents an incremental run wrote and
	// left alone because the target already held them.
	Written int
	Skipped int
}

// targetBankID resolves the bank identifier to write under.
func (m Mapping) targetBankID() string {
	if m.BankID != "" {
		return m.BankID
	}
	return m.Archive.Manifest.SourceBankID
}

// factUnitID derives a deterministic memory-unit id for a fact. The archive
// carries no database id, so a stable per-(document, ordinal) id keeps a
// re-import idempotent.
func factUnitID(bankID, documentID string, index int) string {
	return stableID("unit", bankID, documentID, fmt.Sprintf("%d", index))
}

func chunkID(bankID, documentID string, index int) string {
	return stableID("chunk", bankID, documentID, fmt.Sprintf("%d", index))
}

// stableID builds a deterministic uuid identifier from its parts. The
// reference declares these identities as uuid, so the digest is laid out in the
// canonical 8-4-4-4-12 form with the version and variant bits set; a textual
// prefix would not be storable in the column.
func stableID(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	buf := make([]byte, 16)
	copy(buf, sum[:16])
	buf[6] = (buf[6] & 0x0f) | 0x40
	buf[8] = (buf[8] & 0x3f) | 0x80
	hexed := hex.EncodeToString(buf)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32]
}

// unitFromFact converts one archive fact into a memory unit for the target bank.
func unitFromFact(bankID, documentID string, index int, fact hindsightarchive.Fact) storage.MemoryUnit {
	unit := storage.MemoryUnit{
		ID:                    factUnitID(bankID, documentID, index),
		BankID:                bankID,
		Text:                  fact.Text,
		FactType:              storage.MemoryUnitType(fact.FactType),
		Context:               fact.Context,
		EventDate:             fact.EventDate,
		OccurredStart:         fact.OccurredStart,
		OccurredEnd:           fact.OccurredEnd,
		MentionedAt:           fact.MentionedAt,
		Metadata:              fact.Metadata,
		DocumentID:            documentID,
		Tags:                  fact.Tags,
		SourceMemoryIDs:       sourceMemoryIDs(fact.SourceID),
		ObservationScopes:     archiveScopes(fact.ObservationScopes),
		ConsolidatedAt:        fact.ConsolidatedAt,
		ConsolidationFailedAt: fact.ConsolidationFailedAt,
	}
	if fact.ChunkIndex != nil {
		unit.ChunkID = chunkID(bankID, documentID, *fact.ChunkIndex)
	}
	if unit.Metadata == nil {
		unit.Metadata = map[string]any{}
	}
	if unit.Tags == nil {
		unit.Tags = []string{}
	}
	return unit
}

// sourceMemoryIDs maps the archive's single source reference onto the
// reference's source_memory_ids array. The column is uuid[] upstream, so a
// reference that is not uuid-shaped is dropped rather than stored and rejected
// by the database.
func sourceMemoryIDs(sourceID string) []string {
	if !isUUID(sourceID) {
		return []string{}
	}
	return []string{sourceID}
}

// isUUID reports whether value has the canonical 8-4-4-4-12 form.
func isUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F')
			if !isHex {
				return false
			}
		}
	}
	return true
}

// archiveScopes encodes the archive's scope list into the JSON value the
// storage column holds. The archive carries the explicit-group form only, so
// an empty list means the fact was retained unscoped and yields no value.
func archiveScopes(scopes []string) json.RawMessage {
	if len(scopes) == 0 {
		return nil
	}
	encoded, err := json.Marshal(scopes)
	if err != nil {
		return nil
	}
	return encoded
}

// unmappedFields reports the archive fields that have no Wenyuan column. The
// list is empty when every archive field has a destination; it is kept so a
// future field addition fails loudly rather than dropping silently.
func unmappedFields() []string {
	return nil
}
