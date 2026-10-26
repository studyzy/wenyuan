package hindsightarchive

import (
	"archive/zip"
	"bytes"
	"testing"
)

// A row file is written the way the reference's producer writes it:
// `json.dumps(rows, indent=2, default=_row_json_default)`
// (engine/transfer/export.py:918). That is a JSON list drawn at two-space
// indent, and each row's keys follow the table's column order because the row is
// a `SELECT *` — CPython's json.dumps preserves the dict's order.
//
// Restoring the previous encoding (compact `json.Marshal` of a map) fails this
// case: its members sit on one line, and its keys are sorted.
func TestWriteRowFileMatchesJSONDumpsIndent(t *testing.T) {
	rows := []map[string]any{{
		"bank_id": "b1", "name": "b1",
		"disposition": `{"empathy": 3, "literalism": 3, "skepticism": 3}`,
		"created_at":  "2026-10-26T06:07:10.463871+00:00",
		"updated_at":  "2026-10-26T06:07:56.315567+00:00",
		"mission":     "", "last_consolidated_at": nil, "mission_changed_at": nil,
		"config":      `{"retain_mission": "keep it"}`,
		"internal_id": "0f822cf9-c433-41e2-bb54-afce57e31cd7",
	}}
	got, err := EncodeRowFile(rows, ColumnOrder(BanksName))
	if err != nil {
		t.Fatal(err)
	}
	want := `[
  {
    "bank_id": "b1",
    "name": "b1",
    "disposition": "{\"empathy\": 3, \"literalism\": 3, \"skepticism\": 3}",
    "created_at": "2026-10-26T06:07:10.463871+00:00",
    "updated_at": "2026-10-26T06:07:56.315567+00:00",
    "mission": "",
    "last_consolidated_at": null,
    "mission_changed_at": null,
    "config": "{\"retain_mission\": \"keep it\"}",
    "internal_id": "0f822cf9-c433-41e2-bb54-afce57e31cd7"
  }
]`
	if string(got) != want {
		t.Fatalf("row file =\n%s\nwant\n%s", got, want)
	}
}

// An empty section is `[]`, which is what `json.dumps([])` produces and what the
// reference writes for a selected-but-empty table.
func TestWriteRowFileEmptySectionIsAnEmptyList(t *testing.T) {
	for _, rows := range [][]map[string]any{nil, {}} {
		got, err := EncodeRowFile(rows, ColumnOrder(BanksName))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "[]" {
			t.Fatalf("empty section = %q, want []", got)
		}
	}
}

// A column the recorded order does not name keeps its value, after the named
// ones, so a column added to the schema later is carried rather than dropped.
func TestWriteRowFileKeepsUnlistedColumns(t *testing.T) {
	rows := []map[string]any{{"name": "n", "bank_id": "b", "zzz_new_column": 7}}
	got, err := EncodeRowFile(rows, ColumnOrder(BanksName))
	if err != nil {
		t.Fatal(err)
	}
	want := "[\n  {\n    \"bank_id\": \"b\",\n    \"name\": \"n\",\n    \"zzz_new_column\": 7\n  }\n]"
	if string(got) != want {
		t.Fatalf("row file =\n%s\nwant\n%s", got, want)
	}
}

// Every archive entry declares the two general-purpose flags the reference's
// streaming writer sets: bit 3 (a data descriptor follows the payload) and bit
// 11 (the name is UTF-8). bit 11 in particular is NOT what Go computes for an
// ASCII name, so a writer that leaves Flags at zero renders 0x0008 and differs
// from every entry the reference produces.
func TestWriteSetsTheStreamingEntryFlags(t *testing.T) {
	archive := &Archive{}
	archive.Manifest.SchemaVersion = SupportedSchemaVersion
	archive.Manifest.SourceBankID = "b1"
	archive.Manifest.ArchiveType = "bank"
	archive.Manifest.Scope = &Scope{Data: true, BankConfig: true}
	data, err := Write(archive)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	if len(reader.File) == 0 {
		t.Fatal("archive carries no entries")
	}
	for _, file := range reader.File {
		if file.Name == DocumentsDir+"/" {
			// The placeholder directory entry an empty archive carries is a
			// directory, and archive/zip clears the data-descriptor bit for one
			// (there is no payload to describe).
			continue
		}
		if file.Flags&0x0008 == 0 {
			t.Fatalf("%s: general-purpose bit 3 (data descriptor) is clear, flags=%#x", file.Name, file.Flags)
		}
		if file.Flags&0x0800 == 0 {
			t.Fatalf("%s: general-purpose bit 11 (UTF-8 name) is clear, flags=%#x", file.Name, file.Flags)
		}
	}
}

// A jsonb member travels as the TEXT PostgreSQL renders it as
// (`transfer/export.py:431-440` dumps with `default=_row_json_default`, and the
// archive declares `bank_rows_json_encoding: "serialized"`). PostgreSQL's own
// text form is what the archive has to carry, so position, kind and padding are
// all part of the contract.
func TestNormalizeRowValueRendersTimestampsLikeIsoformat(t *testing.T) {
	cases := map[string]string{
		"2026-10-26T06:07:10Z":             "2026-10-26T06:07:10.000000+00:00",
		"2026-10-26T06:07:10.5Z":           "2026-10-26T06:07:10.500000+00:00",
		"2026-10-26 06:07:10.463871+00:00": "2026-10-26T06:07:10.463871+00:00",
	}
	for in, want := range cases {
		got, ok := NormalizeRowValue(in).(string)
		if !ok || got != want {
			t.Fatalf("%q → %v, want %q", in, NormalizeRowValue(in), want)
		}
	}
	// A value that is not a timestamp is ordinary text and must be left alone.
	if got := NormalizeRowValue("be nice"); got != "be nice" {
		t.Fatalf("plain text changed: %v", got)
	}
}

// A bank with no documents carries no `documents/` entry at all.
//
// The reference writes one `documents/{n:06d}.json` member per document and
// nothing else (engine/transfer/export.py:906-910), so an archive of an empty
// bank has no member under that prefix. A placeholder directory entry would be a
// member the reference never produces, and it is not needed: documentNames treats
// an absent directory as a document set.
func TestWriteEmitsNoDocumentsEntryForAnEmptyBank(t *testing.T) {
	archive := &Archive{}
	archive.Manifest.SchemaVersion = SupportedSchemaVersion
	archive.Manifest.SourceBankID = "b1"
	archive.Manifest.ArchiveType = "bank"
	archive.Manifest.Scope = &Scope{Data: true, BankConfig: true}
	data, err := Write(archive)
	if err != nil {
		t.Fatal(err)
	}
	reader, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range reader.File {
		if file.Name == DocumentsDir+"/" || file.Name == DocumentsDir {
			t.Fatalf("archive carries %q; the reference emits no documents entry for an empty bank", file.Name)
		}
		if len(file.Name) >= len(DocumentsDir) && file.Name[:len(DocumentsDir)] == DocumentsDir {
			t.Fatalf("unexpected documents entry %q", file.Name)
		}
	}
	// The archive still parses, and reads as zero documents.
	parsed, err := Read(&zipFS{files: reader.File})
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.Documents) != 0 {
		t.Fatalf("documents = %d, want 0", len(parsed.Documents))
	}
}
