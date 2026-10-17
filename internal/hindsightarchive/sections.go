package hindsightarchive

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
)

// Archive entry names beyond the manifest and the per-document payloads. A
// whole-bank transfer carries the bank-level sections under these fixed names;
// a documents-only export carries only the first two, so every section is read
// optionally and an absent one is an empty slice rather than an error.
const (
	ObservationsName   = "observations.json"
	MentalModelsName   = "mental_models.json"
	KnowledgePagesName = "knowledge_pages.json"
	BanksName          = "banks.json"
	DirectivesName     = "directives.json"
	WebhooksName       = "webhooks.json"
	// AttachmentsName lists the attachment rows whose bytes the archive also
	// carries, one entry per row under BlobsDir.
	AttachmentsName = "attachments.json"
	// BlobsDir holds one archive entry per carried attachment's bytes. The
	// bytes never travel inside the row, so a large image does not have to be
	// base64-decoded out of a JSON document.
	BlobsDir = "blobs"
	// MentalModelHistoryName carries a bank's mental model refresh log. It sits
	// in the data section rather than the history one: a model's history belongs
	// to the model, which is itself a reading of the bank's facts.
	MentalModelHistoryName = "mental_model_history.json"
	// HistoryDir holds the operational-history tables, one entry per table, and
	// is carried only when the history section is selected.
	HistoryDir = "history"
	// DataDir holds the bank-scoped state that is neither a document nor the
	// bank's configuration: the operations log and the curation archive. Its
	// rows travel verbatim, because their columns are not all part of any model
	// this build reads them through.
	DataDir = "data"
	// InvalidatedMemoryTable is the curation archive: units a caller
	// invalidated but can still revert.
	InvalidatedMemoryTable = "invalidated_memory_units"
	// AsyncOperationsTable is the operations log. Only finished operations are
	// carried: a pending one names work the source instance still owes, and a
	// target that restored it would run work that was never its own.
	AsyncOperationsTable = "async_operations"
	// AuditLogTable and LLMRequestsTable name the operational-history tables.
	AuditLogTable    = "audit_log"
	LLMRequestsTable = "llm_requests"
)

// HistoryTables are the operational-history tables an archive carries under
// HistoryDir when the history section is selected, in export order.
var HistoryTables = []string{AuditLogTable, LLMRequestsTable}

// DataTables are the bank-scoped state tables an archive carries under DataDir
// with the data section, in export order.
var DataTables = []string{AsyncOperationsTable, InvalidatedMemoryTable}

// HistoryEntry names the archive entry holding one operational-history table.
func HistoryEntry(table string) string { return HistoryDir + "/" + table + ".json" }

// DataEntry names the archive entry holding one bank-scoped state table.
func DataEntry(table string) string { return DataDir + "/" + table + ".json" }

// Attachment is one attachment row plus the archive entry holding its bytes.
//
// StorageKey is deliberately absent: the key encodes the source instance's
// tenant and bank, so the importing instance recomputes it and rewrites the
// row rather than reusing the source's. Entry is the ZIP member the bytes live
// in — an opaque ordinal, because a storage key is caller-shaped and must never
// become a path inside the archive.
type Attachment struct {
	BankID         string  `json:"bank_id"`
	DocumentID     string  `json:"document_id"`
	AttachmentHash string  `json:"attachment_hash"`
	ShortID        string  `json:"short_id"`
	MediaType      string  `json:"media_type"`
	ByteSize       int64   `json:"byte_size"`
	Kind           string  `json:"kind"`
	Filename       string  `json:"filename"`
	CreatedAt      *string `json:"created_at"`
	Entry          string  `json:"entry"`
}

// BlobEntry names the archive entry for the attachment at an ordinal. The name
// carries no caller-supplied component, so nothing an attachment is called can
// escape its own entry.
func BlobEntry(index int) string { return fmt.Sprintf("%s/%06d.bin", BlobsDir, index) }

// Scope is the section selector an archive records as the scope it was
// produced with. An archive written before the selector existed carries none,
// which a reader treats as data plus bank configuration (what a whole-bank
// export always was).
type Scope struct {
	Data       bool `json:"data"`
	BankConfig bool `json:"bank_config"`
	History    bool `json:"history"`
}

// ObservationSource references one source fact of an observation by the
// document it belongs to and its ordinal within that document's fact list.
// Observations span documents and cite their sources by unit id, and those ids
// do not survive a transfer, so each source travels as a position instead.
type ObservationSource struct {
	DocumentID string `json:"document_id"`
	FactIndex  int    `json:"fact_index"`
}

// Observation is a consolidated observation: a bank-level memory derived from
// facts rather than one document. It carries no embedding and no entity
// associations; retrieval reaches those through its source facts.
type Observation struct {
	SourceID          string              `json:"source_id"`
	Text              string              `json:"text"`
	CreatedAt         *string             `json:"created_at"`
	Tags              []string            `json:"tags"`
	EventDate         *string             `json:"event_date"`
	OccurredStart     *string             `json:"occurred_start"`
	OccurredEnd       *string             `json:"occurred_end"`
	MentionedAt       *string             `json:"mentioned_at"`
	ObservationScopes json.RawMessage     `json:"observation_scopes"`
	ProofCount        int                 `json:"proof_count"`
	Sources           []ObservationSource `json:"sources"`
}

// KnowledgePage is one node of the knowledge-base tree: a folder or a page.
// The identifier and a page's mental_model_id are preserved across a transfer,
// because every other node's parent link names them.
type KnowledgePage struct {
	ID            string  `json:"id"`
	ParentID      string  `json:"parent_id"`
	Kind          string  `json:"kind"`
	Name          string  `json:"name"`
	MentalModelID string  `json:"mental_model_id"`
	SortOrder     int     `json:"sort_order"`
	Managed       bool    `json:"managed"`
	CreatedAt     *string `json:"created_at"`
	UpdatedAt     *string `json:"updated_at"`
}

// ReadSection reads and decodes an optional JSON section. A section the
// archive does not carry yields a nil slice and no error, because a
// documents-only archive legitimately lacks every bank-level section.
func ReadSection[T any](fsys fs.FS, name string) ([]T, error) {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return nil, nil
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var out []T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("parse %s: %w", name, err)
	}
	return out, nil
}

// Write serialises an archive back into its ZIP representation. It is the
// inverse of Read: an archive this package produces is readable both here and
// by the reference's importer, and an archive the reference produces is
// readable here.
func Write(archive *Archive) ([]byte, error) {
	if archive == nil {
		return nil, fmt.Errorf("no archive to write")
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if err := writeArchive(zw, archive); err != nil {
		_ = zw.Close()
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteBankZipTo is Write's streaming-friendly form: it writes the archive's
// entries into an already-open zip writer, so a caller that owns the writer
// keeps control of where the bytes go.
func writeArchive(zw *zip.Writer, archive *Archive) error {
	writeEntry := func(name string, body []byte) error {
		w, err := zw.Create(name)
		if err != nil {
			return err
		}
		_, err = w.Write(body)
		return err
	}
	encode := func(value any) ([]byte, error) {
		// HTML escaping is off so a payload's text round-trips byte-for-byte
		// instead of turning a quote into an entity.
		var out bytes.Buffer
		enc := json.NewEncoder(&out)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(value); err != nil {
			return nil, err
		}
		return bytes.TrimRight(out.Bytes(), "\n"), nil
	}

	for index, document := range archive.Documents {
		body, err := encode(document)
		if err != nil {
			return err
		}
		if err := writeEntry(fmt.Sprintf("%s/%06d.json", DocumentsDir, index), body); err != nil {
			return err
		}
	}
	// The documents directory must exist even for an archive with no documents,
	// because the reader locates the payloads through it.
	if len(archive.Documents) == 0 {
		if err := writeEntry(DocumentsDir+"/", nil); err != nil {
			return err
		}
	}
	sections := []struct {
		name  string
		value any
		empty bool
	}{
		{ObservationsName, archive.Observations, len(archive.Observations) == 0},
		{MentalModelsName, archive.MentalModels, len(archive.MentalModels) == 0},
		// The knowledge tree is written even when empty for a whole-bank
		// archive: an empty list distinguishes "no pages" from an archive
		// produced before the tree existed.
		{KnowledgePagesName, archive.KnowledgePages,
			len(archive.KnowledgePages) == 0 && (archive.Manifest.Scope == nil || !archive.Manifest.Scope.Data)},
		{BanksName, archive.Banks, len(archive.Banks) == 0},
		{DirectivesName, archive.Directives, len(archive.Directives) == 0},
		{WebhooksName, archive.Webhooks, len(archive.Webhooks) == 0},
		{AttachmentsName, archive.Attachments, len(archive.Attachments) == 0},
		{MentalModelHistoryName, archive.MentalModelHistory, len(archive.MentalModelHistory) == 0},
	}
	for _, section := range sections {
		if section.empty {
			continue
		}
		body, err := encode(section.value)
		if err != nil {
			return err
		}
		if err := writeEntry(section.name, body); err != nil {
			return err
		}
	}
	// Operational history is written only when the archive carries it, so an
	// absent section means "not selected" rather than "empty".
	for _, table := range HistoryTables {
		rows := archive.History[table]
		if len(rows) == 0 {
			continue
		}
		body, err := encode(rows)
		if err != nil {
			return err
		}
		if err := writeEntry(HistoryEntry(table), body); err != nil {
			return err
		}
	}
	for _, table := range DataTables {
		rows := archive.Data[table]
		if len(rows) == 0 {
			continue
		}
		body, err := encode(rows)
		if err != nil {
			return err
		}
		if err := writeEntry(DataEntry(table), body); err != nil {
			return err
		}
	}
	// The bytes follow their rows, one entry per attachment, under the entry
	// name the row carries. A row without its bytes is refused rather than
	// written as a dangling name: the archive would claim to carry an
	// attachment it does not, which no importer can tell from an empty one.
	for _, attachment := range archive.Attachments {
		if attachment.Entry == "" {
			return fmt.Errorf("attachment %s/%s names no blob entry", attachment.DocumentID, attachment.ShortID)
		}
		data, ok := archive.Blobs[attachment.Entry]
		if !ok {
			return fmt.Errorf("attachment %s/%s names %s, which holds no bytes", attachment.DocumentID, attachment.ShortID, attachment.Entry)
		}
		if err := writeEntry(attachment.Entry, data); err != nil {
			return err
		}
	}
	manifest, err := encode(archive.Manifest)
	if err != nil {
		return err
	}
	return writeEntry(ManifestName, manifest)
}

// DocumentNames lists the per-document payload entries of a filesystem in
// archive order. It is exported so a caller can stream a large archive one
// document at a time without materialising every payload.
func DocumentNames(fsys fs.FS) ([]string, error) { return documentNames(fsys) }
