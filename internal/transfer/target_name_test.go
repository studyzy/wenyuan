package transfer

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// A restore under a new bank id follows the rename in the display name when the
// archived name is the source id — a bank's default name — and keeps a name
// someone actually chose.
func TestRestoreFollowsRenameInTheDisplayName(t *testing.T) {
	cases := []struct {
		name     string
		rowName  string
		wantName string
	}{
		{name: "the default name follows the rename", rowName: "coding-agent::wenyuan", wantName: "studyzy::wenyuan"},
		{name: "a chosen name is kept", rowName: "Project memory", wantName: "Project memory"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			archive := &transferarchive.Archive{
				Manifest: transferarchive.Manifest{
					SchemaVersion: transferarchive.SupportedSchemaVersion,
					SourceBankID:  "coding-agent::wenyuan",
					ArchiveType:   "bank",
				},
				Banks: []map[string]any{{
					"bank_id": "coding-agent::wenyuan",
					"name":    tc.rowName,
				}},
			}
			store := storage.NewMemoryStore("studyzy")
			if _, err := (ImportApplier{Store: store}).Apply(context.Background(), ImportRequest{
				Archive:      archive,
				BankID:       "coding-agent::wenyuan",
				TargetBankID: "studyzy::wenyuan",
				Mode:         ImportRestore,
				Scope:        DefaultExportScope(),
			}); err != nil {
				t.Fatalf("import: %v", err)
			}
			bank, err := store.GetBank(context.Background(), "studyzy::wenyuan")
			if err != nil {
				t.Fatalf("bank: %v", err)
			}
			if bank.Name != tc.wantName {
				t.Fatalf("bank name = %q, want %q", bank.Name, tc.wantName)
			}
		})
	}
}
