package hindsightarchive

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// writeFixture builds a minimal reference export archive on disk.
func writeFixture(t *testing.T, manifest string, documents map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "bank.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	zw := zip.NewWriter(file)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write(ManifestName, manifest)
	for name, body := range documents {
		write(DocumentsDir+"/"+name, body)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestOpenZipParsesManifestAndDocuments(t *testing.T) {
	path := writeFixture(t,
		`{"schema_version":1,"source_bank_id":"src-bank","document_count":2,"fact_count":3,"archive_type":"bank"}`,
		map[string]string{
			"000000.json": `{"id":"doc-a","original_text":"alpha","tags":["t1"],
			  "chunks":[{"chunk_index":0,"chunk_text":"alpha"}],
			  "facts":[{"text":"fact one","fact_type":"world","entities":["Bob"],
			    "occurred_start":"2026-01-01T00:00:00Z","tags":["x"]}]}`,
			"000001.json": `{"id":"doc-b","original_text":"beta",
			  "chunks":[{"chunk_index":0,"chunk_text":"beta"}],
			  "facts":[
			    {"text":"fact two","fact_type":"experience","entities":["Alice"]},
			    {"text":"fact three","fact_type":"observation","entities":["Alice","Bob"],
			     "causal_relations":[{"relation_type":"causes","target_fact_index":0}]}
			  ]}`,
		})

	archive, err := OpenZip(path)
	if err != nil {
		t.Fatal(err)
	}
	if archive.Manifest.SchemaVersion != 1 {
		t.Fatalf("schema version = %d", archive.Manifest.SchemaVersion)
	}
	if archive.Manifest.SourceBankID != "src-bank" {
		t.Fatalf("source bank = %q", archive.Manifest.SourceBankID)
	}
	if len(archive.Documents) != 2 {
		t.Fatalf("documents = %d, want 2", len(archive.Documents))
	}
	if archive.Documents[0].ID != "doc-a" {
		t.Fatalf("documents must be in archive order: got %q first", archive.Documents[0].ID)
	}
	if got := archive.Documents[1].Facts; len(got) != 2 {
		t.Fatalf("doc-b facts = %d, want 2", len(got))
	}
	if got := archive.Documents[1].Facts[1].CausalRelations; len(got) != 1 || got[0].RelationType != "causes" {
		t.Fatalf("causal relations not parsed: %+v", got)
	}
	if archive.Documents[0].Facts[0].OccurredStart == nil {
		t.Fatal("occurred_start did not parse")
	}
}

func TestOpenZipRejectsUnsupportedSchemaVersion(t *testing.T) {
	path := writeFixture(t, `{"schema_version":99,"source_bank_id":"x"}`, nil)
	if _, err := OpenZip(path); err == nil {
		t.Fatal("an unsupported schema_version must be rejected")
	}
}

func TestReadRejectsDocumentWithoutID(t *testing.T) {
	path := writeFixture(t,
		`{"schema_version":1,"source_bank_id":"x"}`,
		map[string]string{"000000.json": `{"original_text":"no id"}`})
	if _, err := OpenZip(path); err == nil {
		t.Fatal("a document without an id must be rejected")
	}
}

// An archive with attachments round-trips its rows and their bytes: the bytes
// travel as their own entries, named by the row, so nothing caller-shaped
// becomes a path inside the archive.
func TestAttachmentBlobsRoundTrip(t *testing.T) {
	bytesOf := func(name string) []byte { return []byte("bytes of " + name) }
	archive := &Archive{
		Manifest: Manifest{
			SchemaVersion:   SupportedSchemaVersion,
			SourceBankID:    "b1",
			ArchiveType:     "bank",
			AttachmentCount: 2,
		},
		Attachments: []Attachment{
			{
				BankID: "b1", DocumentID: "d1", AttachmentHash: "h1", ShortID: "s1",
				MediaType: "image/png", ByteSize: int64(len(bytesOf("one"))), Kind: "image",
				Filename: "one.png", Entry: BlobEntry(0),
			},
			{
				BankID: "b1", DocumentID: "d1", AttachmentHash: "h2", ShortID: "s2",
				MediaType: "image/jpeg", ByteSize: int64(len(bytesOf("two"))), Kind: "image",
				Entry: BlobEntry(1),
			},
		},
		Blobs: map[string][]byte{BlobEntry(0): bytesOf("one"), BlobEntry(1): bytesOf("two")},
	}
	data, err := Write(archive)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	got, err := Read(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.Manifest.AttachmentCount != 2 || len(got.Attachments) != 2 {
		t.Fatalf("attachments = %d, manifest = %+v", len(got.Attachments), got.Manifest)
	}
	if got.Attachments[0].Filename != "one.png" || got.Attachments[0].Entry != BlobEntry(0) {
		t.Fatalf("row = %+v", got.Attachments[0])
	}
	if string(got.Blobs[BlobEntry(0)]) != "bytes of one" || string(got.Blobs[BlobEntry(1)]) != "bytes of two" {
		t.Fatalf("blobs = %q", got.Blobs)
	}
}

// A row whose entry the archive does not hold is an error, not an attachment
// that silently restores empty.
func TestAttachmentWithMissingBlobIsRejected(t *testing.T) {
	archive := &Archive{
		Manifest: Manifest{SchemaVersion: SupportedSchemaVersion, SourceBankID: "b1", ArchiveType: "bank"},
		Attachments: []Attachment{
			{BankID: "b1", DocumentID: "d1", ShortID: "s1", Entry: BlobEntry(0)},
		},
	}
	if _, err := Write(archive); err == nil {
		t.Fatal("a row naming bytes the archive does not hold must be refused")
	}

	// And a foreign archive that lies about carrying them is refused on read.
	honest := &Archive{
		Manifest: Manifest{SchemaVersion: SupportedSchemaVersion, SourceBankID: "b1", ArchiveType: "bank"},
	}
	data, err := Write(honest)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	got, err := Read(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	got.Attachments = []Attachment{{BankID: "b1", DocumentID: "d1", ShortID: "s1", Entry: BlobEntry(9)}}
	if _, err := Write(got); err == nil {
		t.Fatal("a reassembled dangling row must be refused")
	}
}

// A row naming no entry at all is refused rather than written as a dangling
// reference.
func TestAttachmentWithoutEntryIsRejected(t *testing.T) {
	archive := &Archive{
		Manifest:    Manifest{SchemaVersion: SupportedSchemaVersion, SourceBankID: "b1", ArchiveType: "bank"},
		Attachments: []Attachment{{BankID: "b1", DocumentID: "d1", ShortID: "s1"}},
	}
	if _, err := Write(archive); err == nil {
		t.Fatal("a row naming no entry must be refused")
	}
}

// The history sections round-trip: a model's refresh log rides with the data,
// and the operational tables are carried only when they are present.
func TestHistorySectionsRoundTrip(t *testing.T) {
	archive := &Archive{
		Manifest: Manifest{
			SchemaVersion: SupportedSchemaVersion, SourceBankID: "b1",
			ArchiveType: "bank", IncludesHistory: true,
		},
		MentalModelHistory: []map[string]any{
			{"mental_model_id": "mm-1", "bank_id": "b1", "content": map[string]any{"previous_content": "old"}, "changed_at": "2026-01-01T00:00:00Z"},
		},
		History: map[string][]map[string]any{
			AuditLogTable:    {{"id": "a-1", "action": "recall", "bank_id": "b1"}},
			LLMRequestsTable: {{"id": "l-1", "operation": "retain", "bank_id": "b1"}},
		},
	}
	data, err := Write(archive)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	names := map[string]bool{}
	for _, f := range reader.File {
		names[f.Name] = true
	}
	for _, want := range []string{MentalModelHistoryName, HistoryEntry(AuditLogTable), HistoryEntry(LLMRequestsTable)} {
		if !names[want] {
			t.Fatalf("archive missing %s: %v", want, names)
		}
	}
	got, err := Read(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(got.MentalModelHistory) != 1 || got.MentalModelHistory[0]["mental_model_id"] != "mm-1" {
		t.Fatalf("mental model history = %+v", got.MentalModelHistory)
	}
	if len(got.History[AuditLogTable]) != 1 || len(got.History[LLMRequestsTable]) != 1 {
		t.Fatalf("history = %+v", got.History)
	}
}

// An archive with no history carries no history entries, so an empty table is
// distinguishable from a table that was never selected.
func TestHistoryAbsentWhenNotSelected(t *testing.T) {
	archive := &Archive{
		Manifest: Manifest{SchemaVersion: SupportedSchemaVersion, SourceBankID: "b1", ArchiveType: "bank"},
		History:  map[string][]map[string]any{AuditLogTable: {}},
	}
	data, err := Write(archive)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("zip: %v", err)
	}
	got, err := Read(reader)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.History != nil {
		t.Fatalf("history = %+v, want nil", got.History)
	}
}
