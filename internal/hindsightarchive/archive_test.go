package hindsightarchive

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// writeFixture builds a minimal Hindsight export archive on disk.
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
