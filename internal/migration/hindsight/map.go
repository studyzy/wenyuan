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

// entityID derives a deterministic entity id from its canonical name. The
// archive lists entities by name and re-resolves them by name, so the id must
// be a pure function of the name.
func entityID(bankID, name string) string {
	return stableID("entity", bankID, name)
}

func linkID(bankID, fromID, toID, linkType string) string {
	return stableID("link", bankID, fromID, toID, linkType)
}

// stableID builds a deterministic identifier from its parts.
func stableID(kind string, parts ...string) string {
	h := sha256.New()
	h.Write([]byte(kind))
	for _, part := range parts {
		h.Write([]byte{0})
		h.Write([]byte(part))
	}
	sum := h.Sum(nil)
	return kind + "-" + hex.EncodeToString(sum[:12])
}

// unitFromFact converts one archive fact into a memory unit for the target bank.
func unitFromFact(bankID, documentID string, index int, fact hindsightarchive.Fact) storage.MemoryUnit {
	unit := storage.MemoryUnit{
		ID:            factUnitID(bankID, documentID, index),
		BankID:        bankID,
		Text:          fact.Text,
		Type:          storage.MemoryUnitType(fact.FactType),
		Context:       fact.Context,
		EventDate:     fact.EventDate,
		OccurredStart: fact.OccurredStart,
		OccurredEnd:   fact.OccurredEnd,
		MentionedAt:   fact.MentionedAt,
		Confidence:    confidenceFromMetadata(fact.Metadata),
		Metadata:      fact.Metadata,
		DocumentID:    documentID,
		Tags:          fact.Tags,
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

// confidenceFromMetadata reads an optional stored confidence value. The archive
// schema does not carry confidence as a top-level field, so a value is honored
// only when present in metadata under its source name.
func confidenceFromMetadata(metadata map[string]any) float64 {
	if metadata == nil {
		return 0
	}
	switch value := metadata["confidence_score"].(type) {
	case float64:
		return value
	case int:
		return float64(value)
	default:
		return 0
	}
}

// unmappedFields reports the archive fields that have no Wenyuan column. They
// are preserved in metadata by the writer where a bag exists, and reported here
// so nothing is dropped silently.
func unmappedFields() []string {
	return []string{
		"fact.source_id (archive-internal fact identity; not persisted)",
		"fact.observation_scopes (no column; regrouped on the target)",
		"fact.consolidated_at / consolidation_failed_at (no column)",
		"document.retain_params (no column on the target document)",
	}
}
