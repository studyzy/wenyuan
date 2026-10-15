package bankadmin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// writeBinaryArchive builds a version-2 archive: a manifest plus one binary
// COPY stream member per table. Columns carry the type names a live catalogue
// would report.
func writeBinaryArchive(t *testing.T, version any, tables map[string]backupTable, streams map[string][]byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "archive.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	archive := zip.NewWriter(file)
	encoded, err := json.Marshal(backupManifest{
		Version:   mustRawVersion(t, version),
		Schema:    "public",
		CreatedAt: "2026-10-13T15:12:43Z",
		Tables:    tables,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := writeZipEntry(archive, "manifest.json", encoded); err != nil {
		t.Fatal(err)
	}
	for name, data := range streams {
		if err := writeZipEntry(archive, name+".bin", data); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func mustRawVersion(t *testing.T, version any) json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(version)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func textColumns(names ...string) []storage.AdminColumn {
	columns := make([]storage.AdminColumn, 0, len(names))
	for _, name := range names {
		columns = append(columns, storage.AdminColumn{Name: name, Type: "text"})
	}
	return columns
}

func TestRestoreAcceptsBinaryCopyArchive(t *testing.T) {
	streams := map[string][]byte{
		"banks":        buildBinaryCopyStream([]field{[]byte("b1")}),
		"bank_aliases": buildBinaryCopyStream([]field{[]byte("a1"), []byte("b1")}),
	}
	tables := map[string]backupTable{
		"banks":        {Rows: 1, Columns: textColumns("bank_id")},
		"bank_aliases": {Rows: 1, Columns: textColumns("alias", "bank_id")},
	}
	target := newFakeTableStore()
	target.columns["banks"] = textColumns("bank_id")
	target.columns["bank_aliases"] = textColumns("alias", "bank_id")
	path := writeBinaryArchive(t, "2", tables, streams) // the version travels as a string

	var out bytes.Buffer
	if err := RunRestore(context.Background(), target, RestoreFlags{Input: path, Yes: true}, nil, &out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if len(target.binaryLoads) != 2 {
		t.Fatalf("store received %d load(s), want 2", len(target.binaryLoads))
	}
	// Dependency order: the parent bank row must land before its alias.
	if target.binaryLoads[0].Table != "banks" || target.binaryLoads[1].Table != "bank_aliases" {
		t.Fatalf("load order = %s, %s", target.binaryLoads[0].Table, target.binaryLoads[1].Table)
	}
	if !bytes.Equal(target.binaryLoads[0].Data, streams["banks"]) {
		t.Fatal("the bank stream must be replayed byte-for-byte")
	}
	if got := target.binaryLoads[1].Columns; len(got) != 2 || got[0] != "alias" || got[1] != "bank_id" {
		t.Fatalf("alias columns = %v", got)
	}
	if !strings.Contains(out.String(), "restored 2 row(s) across 2 table(s)") {
		t.Fatalf("restore output = %q", out.String())
	}
}

func TestRestoreBinaryRefusesTypeMismatch(t *testing.T) {
	tables := map[string]backupTable{
		"banks": {Rows: 1, Columns: []storage.AdminColumn{{Name: "bank_id", Type: "character varying(255)"}}},
	}
	path := writeBinaryArchive(t, "2", tables, map[string][]byte{
		"banks": buildBinaryCopyStream([]field{[]byte("b1")}),
	})
	target := newFakeTableStore()
	target.columns["banks"] = textColumns("bank_id") // same column, different type

	var out bytes.Buffer
	err := RunRestore(context.Background(), target, RestoreFlags{Input: path, Yes: true}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), `incompatible with target table "banks"`) {
		t.Fatalf("restore error = %v, want a column-type mismatch", err)
	}
	if len(target.binaryLoads) != 0 {
		t.Fatal("nothing may be restored once a type mismatch is found")
	}
}

func TestRestoreBinaryStripsColumnsTheTargetLacks(t *testing.T) {
	// The archive declares three columns; the target no longer has "legacy".
	stream := buildBinaryCopyStream([]field{[]byte("a1"), []byte("x"), []byte("b1")})
	tables := map[string]backupTable{
		"bank_aliases": {Rows: 1, Columns: textColumns("alias", "legacy", "bank_id")},
	}
	path := writeBinaryArchive(t, 2, tables, map[string][]byte{"bank_aliases": stream})

	target := newFakeTableStore()
	target.columns["bank_aliases"] = textColumns("alias", "bank_id")
	var out bytes.Buffer
	if err := RunRestore(context.Background(), target, RestoreFlags{Input: path, Yes: true}, nil, &out); err != nil {
		t.Fatalf("restore: %v", err)
	}
	loads := target.binaryLoads
	if len(loads) != 1 {
		t.Fatalf("store received %d load(s), want 1", len(loads))
	}
	if got := loads[0].Columns; len(got) != 2 || got[0] != "alias" || got[1] != "bank_id" {
		t.Fatalf("columns = %v, want the archive's minus the dropped one", got)
	}
	want := buildBinaryCopyStream([]field{[]byte("a1"), []byte("b1")})
	if !bytes.Equal(loads[0].Data, want) {
		t.Fatal("the dropped field was not stripped from the stream")
	}
	if !strings.Contains(out.String(), "ignoring 1 archive column(s) absent from the target schema: legacy") {
		t.Fatalf("restore output = %q", out.String())
	}
}

func TestRestoreRefusesUnknownVersion(t *testing.T) {
	path := writeBinaryArchive(t, "3", map[string]backupTable{}, map[string][]byte{})
	var out bytes.Buffer
	err := RunRestore(context.Background(), newFakeTableStore(), RestoreFlags{Input: path, Yes: true}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "unsupported backup version 3") {
		t.Fatalf("restore error = %v, want an unsupported-version refusal", err)
	}
}

func TestRestoreRefusesUnparseableVersion(t *testing.T) {
	path := writeBinaryArchive(t, "two", map[string]backupTable{}, map[string][]byte{})
	var out bytes.Buffer
	err := RunRestore(context.Background(), newFakeTableStore(), RestoreFlags{Input: path, Yes: true}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "unparseable version") {
		t.Fatalf("restore error = %v, want an unparseable-version refusal", err)
	}
}

func TestRestoreBinaryRefusesMemberThatIsNotAStream(t *testing.T) {
	tables := map[string]backupTable{
		"banks": {Rows: 1, Columns: textColumns("bank_id")},
	}
	path := writeBinaryArchive(t, "2", tables, map[string][]byte{
		"banks": []byte(`[{"bank_id":"b1"}]`), // JSON, not a binary COPY stream
	})
	var out bytes.Buffer
	err := RunRestore(context.Background(), newFakeTableStore(), RestoreFlags{Input: path, Yes: true}, nil, &out)
	if err == nil || !strings.Contains(err.Error(), "not in PostgreSQL binary COPY format") {
		t.Fatalf("restore error = %v, want a stream-format refusal", err)
	}
}
