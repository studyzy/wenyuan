// Package hindsightarchive reads the reference implementation's whole-bank
// export archive.
//
// The archive is a ZIP produced by the reference's
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
	"errors"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"
)

// ErrManifestMissing reports an archive that has no manifest.json entry. It is
// a distinct sentinel so a caller can render the endpoint's own wording — the
// reference names the endpoint a valid archive comes from — rather than the
// generic file-read error the underlying filesystem returns.
var ErrManifestMissing = errors.New("manifest.json is missing")

// SupportedSchemaVersion is the archive layout this reader understands.
const SupportedSchemaVersion = 1

// The values Manifest.BankRowsJSONEncoding can carry. Each describes how the
// producing instance wrote a jsonb column in its bank/history row files.
const (
	// EncodingSerialized means a jsonb column travelled as its JSON text.
	EncodingSerialized = "serialized"
	// EncodingDecoded means a jsonb column travelled as a decoded object.
	EncodingDecoded = "decoded"
)

// ManifestName is the archive entry holding the manifest.
const ManifestName = "manifest.json"

// DocumentsDir is the archive directory holding the per-document payloads.
const DocumentsDir = "documents"

// Manifest is the top-level archive descriptor (manifest.json).
//
// A documents-only export populates the document and fact counts; a whole-bank
// export adds the bank-level counts and the scope it was produced with.
//
// Every field is always serialized, zero value or not, because the reference's
// producer emits the whole manifest on every export (a documents-only archive
// carries `"scope": null`, a bank archive with no observations carries
// `"observation_count": 0`). A reader tolerates a field an older producer
// lacked, but a producer that dropped zero-valued fields would not be a
// byte-for-byte match — so no field carries `omitempty`.
type Manifest struct {
	SchemaVersion          int     `json:"schema_version"`
	SourceBankID           string  `json:"source_bank_id"`
	ExportedAt             *string `json:"exported_at"`
	DocumentCount          int     `json:"document_count"`
	FactCount              int     `json:"fact_count"`
	ObservationCount       int     `json:"observation_count"`
	MentalModelCount       int     `json:"mental_model_count"`
	KnowledgePageCount     int     `json:"knowledge_page_count"`
	ArchiveType            string  `json:"archive_type"`
	DirectiveCount         int     `json:"directive_count"`
	WebhookCount           int     `json:"webhook_count"`
	IncludesHistory        bool    `json:"includes_history"`
	Scope                  *Scope  `json:"scope"`
	AttachmentCount        int     `json:"attachment_count"`
	OperationCount         int     `json:"operation_count"`
	InvalidatedMemoryCount int     `json:"invalidated_memory_count"`
	// BankRowsJSONEncoding records how JSON/JSONB column values in the
	// bank/history row files were represented by the producing instance: "serialized"
	// when a jsonb column travelled as its JSON text, "decoded" when it travelled as
	// an object. Absent on an archive whose producer did not say, which a reader
	// treats as decoded.
	BankRowsJSONEncoding *string `json:"bank_rows_json_encoding"`
}

// ScopeCarriesData reports whether the archive was produced with the data
// section selected. An archive that records no scope carries data, which is
// what every whole-bank export produced before the selector existed.
func (m Manifest) ScopeCarriesData() bool {
	if m.Scope == nil {
		return m.ArchiveType == "" || m.ArchiveType == "bank"
	}
	return m.Scope.Data
}

// ScopeCarriesBankConfig reports whether the archive was produced with the
// bank-configuration section selected.
func (m Manifest) ScopeCarriesBankConfig() bool {
	if m.Scope == nil {
		return m.ArchiveType == "" || m.ArchiveType == "bank"
	}
	return m.Scope.BankConfig
}

// Document is a single source document plus its chunks and extracted facts.
//
// Its timestamps are strings rather than time.Time because the archive's
// document section is serialized by the reference's pydantic models, whose JSON
// output (six fractional digits, 'Z' suffix, no trailing-zero trimming) differs
// from Go's encoding/json time encoding. Write them with
// httpx.FormatZuluTimestampPtr and read them with ParseTime.
type Document struct {
	ID           string         `json:"id"`
	OriginalText string         `json:"original_text"`
	RetainParams map[string]any `json:"retain_params"`
	Tags         []string       `json:"tags"`
	CreatedAt    *string        `json:"created_at"`
	Chunks       []Chunk        `json:"chunks"`
	Facts        []Fact         `json:"facts"`
}

// MarshalJSON writes the document with its list members as empty arrays rather
// than null. The reference's reader declares tags/chunks/facts as lists, so a
// null there is a validation error rather than "empty" — a document this
// service exports must carry `[]`.
func (d Document) MarshalJSON() ([]byte, error) {
	type alias Document
	out := alias(d)
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.Chunks == nil {
		out.Chunks = []Chunk{}
	}
	if out.Facts == nil {
		out.Facts = []Fact{}
	}
	return json.Marshal(out)
}

// Chunk is a raw text chunk of the source document, reused verbatim.
type Chunk struct {
	ChunkIndex int    `json:"chunk_index"`
	ChunkText  string `json:"chunk_text"`
}

// Fact is one extracted memory unit, without its embedding or database id.
type Fact struct {
	Text          string         `json:"text"`
	SourceID      string         `json:"source_id"`
	FactType      string         `json:"fact_type"`
	Context       string         `json:"context"`
	EventDate     *string        `json:"event_date"`
	OccurredStart *string        `json:"occurred_start"`
	OccurredEnd   *string        `json:"occurred_end"`
	MentionedAt   *string        `json:"mentioned_at"`
	Metadata      map[string]any `json:"metadata"`
	Tags          []string       `json:"tags"`
	// ObservationScopes is the fact's consolidation scoping kept as raw JSON.
	// The producer emits a string enum ("per_tag", "combined",
	// "all_combinations", "shared") or a list of tag lists, and the value is
	// stored back verbatim, so it is not decoded into a single Go type.
	ObservationScopes     json.RawMessage  `json:"observation_scopes"`
	ChunkIndex            *int             `json:"chunk_index"`
	Entities              []string         `json:"entities"`
	CausalRelations       []CausalRelation `json:"causal_relations"`
	CreatedAt             *string          `json:"created_at"`
	ConsolidatedAt        *string          `json:"consolidated_at"`
	ConsolidationFailedAt *string          `json:"consolidation_failed_at"`
}

// MarshalJSON writes the fact with its list and object members as empty
// collections rather than null, matching the reference's schema (tags,
// entities, causal_relations are lists and metadata is an object there, so a
// null is a validation error).
func (f Fact) MarshalJSON() ([]byte, error) {
	type alias Fact
	out := alias(f)
	if out.Tags == nil {
		out.Tags = []string{}
	}
	if out.Entities == nil {
		out.Entities = []string{}
	}
	if out.CausalRelations == nil {
		out.CausalRelations = []CausalRelation{}
	}
	if out.Metadata == nil {
		out.Metadata = map[string]any{}
	}
	return json.Marshal(out)
}

// CausalRelation is a causal edge from a fact to another fact in the same
// document. TargetFactIndex is the ordinal of the target within the document's
// facts list, not a database id, and it may name a fact that follows the
// source in the list, so a consumer must write every fact before any edge.
type CausalRelation struct {
	RelationType    string `json:"relation_type"`
	TargetFactIndex int    `json:"target_fact_index"`
}

// Archive is a parsed and validated export archive.
//
// Documents are always present. The bank-level sections are present only when
// the producer selected them, so an absent one is an empty slice rather than an
// error.
type Archive struct {
	Manifest       Manifest
	Documents      []Document
	Observations   []Observation
	MentalModels   []map[string]any
	KnowledgePages []KnowledgePage
	Banks          []map[string]any
	Directives     []map[string]any
	Webhooks       []map[string]any
	Attachments    []Attachment
	// Blobs holds each attachment's bytes, keyed by its Entry. It is populated
	// by Read and by the archive builder; the rows carry the keys, so a blob no
	// row names is dropped rather than restored as an orphan.
	Blobs map[string][]byte

	// MentalModelHistory is the bank's mental model refresh log, as verbatim
	// rows. The surrogate row id is dropped, because the target reassigns it
	// from its own identity sequence; the model it belongs to keeps its id.
	MentalModelHistory []map[string]any
	// History holds the operational-history tables the archive carries, keyed by
	// table name. It is empty unless the history section was selected, so an
	// absent table means "not carried" rather than "carried empty".
	History map[string][]map[string]any
	// Data holds the bank-scoped state tables the archive carries, keyed by
	// table name: verbatim rows whose columns no model here projects.
	Data map[string][]map[string]any
}

// Read parses the archive from a filesystem. The caller supplies fs.FS rooted
// at the unzipped archive (or a zip.Reader adapted to fs.FS).
func Read(fsys fs.FS) (*Archive, error) {
	manifestBytes, err := fs.ReadFile(fsys, ManifestName)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, ErrManifestMissing
		}
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
	observations, err := ReadSection[Observation](fsys, ObservationsName)
	if err != nil {
		return nil, err
	}
	mentalModels, err := ReadSection[map[string]any](fsys, MentalModelsName)
	if err != nil {
		return nil, err
	}
	knowledgePages, err := ReadSection[KnowledgePage](fsys, KnowledgePagesName)
	if err != nil {
		return nil, err
	}
	banks, err := ReadSection[map[string]any](fsys, BanksName)
	if err != nil {
		return nil, err
	}
	directives, err := ReadSection[map[string]any](fsys, DirectivesName)
	if err != nil {
		return nil, err
	}
	webhooks, err := ReadSection[map[string]any](fsys, WebhooksName)
	if err != nil {
		return nil, err
	}
	attachments, err := ReadSection[Attachment](fsys, AttachmentsName)
	if err != nil {
		return nil, err
	}
	mentalModelHistory, err := ReadSection[map[string]any](fsys, MentalModelHistoryName)
	if err != nil {
		return nil, err
	}
	history := map[string][]map[string]any{}
	for _, table := range HistoryTables {
		rows, err := ReadSection[map[string]any](fsys, HistoryEntry(table))
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			history[table] = rows
		}
	}
	if len(history) == 0 {
		history = nil
	}
	data := map[string][]map[string]any{}
	for _, table := range DataTables {
		rows, err := ReadSection[map[string]any](fsys, DataEntry(table))
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			data[table] = rows
		}
	}
	if len(data) == 0 {
		data = nil
	}
	blobs, err := readBlobs(fsys, attachments)
	if err != nil {
		return nil, err
	}
	return &Archive{
		Manifest:       manifest,
		Documents:      documents,
		Observations:   observations,
		MentalModels:   mentalModels,
		KnowledgePages: knowledgePages,
		Banks:          banks,
		Directives:     directives,
		Webhooks:       webhooks,
		Attachments:    attachments,
		Blobs:          blobs,

		MentalModelHistory: mentalModelHistory,
		History:            history,
		Data:               data,
	}, nil
}

// readBlobs loads the bytes of every attachment's entry. A row whose entry is
// absent from the archive is an error rather than a silently empty attachment:
// the row says the bytes travel with it, so an archive that contradicts itself
// should not restore a broken attachment.
func readBlobs(fsys fs.FS, attachments []Attachment) (map[string][]byte, error) {
	if len(attachments) == 0 {
		return nil, nil
	}
	blobs := make(map[string][]byte, len(attachments))
	for _, attachment := range attachments {
		if attachment.Entry == "" {
			return nil, fmt.Errorf("attachment %s/%s names no blob entry", attachment.DocumentID, attachment.ShortID)
		}
		if _, ok := blobs[attachment.Entry]; ok {
			continue
		}
		data, err := fs.ReadFile(fsys, attachment.Entry)
		if err != nil {
			return nil, fmt.Errorf("read %s for attachment %s: %w", attachment.Entry, attachment.ShortID, err)
		}
		blobs[attachment.Entry] = data
	}
	return blobs, nil
}

// documentNames lists the per-document payload entries in archive order. An
// archive with no documents directory is an empty archive rather than an
// error: a whole-bank export of an empty bank legitimately holds no payloads.
func documentNames(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, DocumentsDir)
	if err != nil {
		return nil, nil
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
