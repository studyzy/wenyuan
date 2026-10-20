package hindsightarchive

import (
	"archive/zip"
	"bytes"
	"testing"
	"time"
)

// Contract reference: transfer/export.py:916-969 — the reference writes every
// entry with `zipfile.writestr(name, data)`, which stamps it with the current
// local time. A zero modification time renders as 1980-01-01 and makes the same
// bank produce a different archive.
func TestWriteStampsEntriesWithTheCurrentTime(t *testing.T) {
	archive := &Archive{
		Manifest:  Manifest{ArchiveType: "bank"},
		Documents: []Document{{ID: "doc-1"}},
	}
	before := time.Now().Add(-2 * time.Second)
	raw, err := Write(archive)
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	after := time.Now().Add(2 * time.Second)
	reader, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	for _, file := range reader.File {
		if file.Modified.Before(before) || file.Modified.After(after) {
			t.Fatalf("%s modified = %v, want a time between %v and %v",
				file.Name, file.Modified, before, after)
		}
	}
}
