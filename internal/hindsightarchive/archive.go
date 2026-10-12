// Package hindsightarchive reads a Hindsight whole-bank export archive.
//
// The archive is a ZIP produced by a Hindsight server's
// POST /v1/default/banks/{bank_id}/export endpoint:
//
//	manifest.json          -- Manifest
//	documents/000000.json  -- Document (one file per document)
//	documents/000001.json
//	...
//
// Documents are stored under a zero-padded index rather than their id, so the
// real id lives inside each payload. The reader exposes the manifest and an
// iterator over documents; it does not interpret the records.
package hindsightarchive

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// SupportedSchemaVersion is the archive layout this reader understands.
const SupportedSchemaVersion = 1

// ManifestName is the archive entry holding the manifest.
const ManifestName = "manifest.json"

// DocumentsDir is the archive directory holding the per-document payloads.
const DocumentsDir = "documents"

// Manifest is the top-level archive descriptor (manifest.json).
type Manifest struct {
	SchemaVersion    int       `json:"schema_version"`
	SourceBankID     string    `json:"source_bank_id"`
	ExportedAt       time.Time `json:"exported_at"`
	DocumentCount    int       `json:"document_count"`
	FactCount        int       `json:"fact_count"`
	ObservationCount int       `json:"observation_count"`
	ArchiveType      string    `json:"archive_type"`
	IncludesHistory  bool      `json:"includes_history"`
	AttachmentCount  int       `json:"attachment_count"`
	OperationCount   int       `json:"operation_count"`
}

// Document is a single source document plus its chunks and extracted facts.
type Document struct {
	ID           string         `json:"id"`
	OriginalText string         `json:"original_text"`
	RetainParams map[string]any `json:"retain_params"`
	Tags         []string       `json:"tags"`
	CreatedAt    *time.Time     `json:"created_at"`
	Chunks       []Chunk        `json:"chunks"`
	Facts        []Fact         `json:"facts"`
}

// Chunk is a raw text chunk of the source document, reused verbatim.
type Chunk struct {
	ChunkIndex int    `json:"chunk_index"`
	ChunkText  string `json:"chunk_text"`
}

// Fact is one extracted memory unit, without its embedding or database id.
type Fact struct {
	Text                  string           `json:"text"`
	SourceID              string           `json:"source_id"`
	FactType              string           `json:"fact_type"`
	Context               string           `json:"context"`
	EventDate             *time.Time       `json:"event_date"`
	OccurredStart         *time.Time       `json:"occurred_start"`
	OccurredEnd           *time.Time       `json:"occurred_end"`
	MentionedAt           *time.Time       `json:"mentioned_at"`
	Metadata              map[string]any   `json:"metadata"`
	Tags                  []string         `json:"tags"`
	ChunkIndex            *int             `json:"chunk_index"`
	Entities              []string         `json:"entities"`
	CausalRelations       []CausalRelation `json:"causal_relations"`
	CreatedAt             *time.Time       `json:"created_at"`
	ConsolidatedAt        *time.Time       `json:"consolidated_at"`
	ConsolidationFailedAt *time.Time       `json:"consolidation_failed_at"`
}

// CausalRelation is a causal edge from a fact to an earlier fact in the same
// document. TargetFactIndex is the ordinal of the target within the document's
// facts list, not a database id.
type CausalRelation struct {
	RelationType    string `json:"relation_type"`
	TargetFactIndex int    `json:"target_fact_index"`
}

// Archive is a parsed and validated export archive.
type Archive struct {
	Manifest  Manifest
	Documents []Document
}

// Read parses the archive from a filesystem. The caller supplies fs.FS rooted
// at the unzipped archive (or a zip.Reader adapted to fs.FS).
func Read(fsys fs.FS) (*Archive, error) {
	manifestBytes, err := fs.ReadFile(fsys, ManifestName)
	if err != nil {
		return nil, fmt.Errorf("read manifest: %w", err)
	}
	var manifest Manifest
	dec := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	dec.UseNumber()
	if err := dec.Decode(&manifest); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}
	if manifest.SchemaVersion != SupportedSchemaVersion {
		return nil, fmt.Errorf("unsupported archive schema_version %d (want %d)",
			manifest.SchemaVersion, SupportedSchemaVersion)
	}

	names, err := documentNames(fsys)
	if err != nil {
		return nil, err
	}
	documents := make([]Document, 0, len(names))
	for _, name := range names {
		raw, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		var doc Document
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if err := dec.Decode(&doc); err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}
		if doc.ID == "" {
			return nil, fmt.Errorf("%s is missing a document id", name)
		}
		documents = append(documents, doc)
	}
	return &Archive{Manifest: manifest, Documents: documents}, nil
}

// documentNames lists the per-document payload entries in archive order.
func documentNames(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, DocumentsDir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", DocumentsDir, err)
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, path.Join(DocumentsDir, entry.Name()))
		}
	}
	sort.Strings(names)
	return names, nil
}
