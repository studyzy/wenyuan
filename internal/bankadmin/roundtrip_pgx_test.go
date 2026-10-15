//go:build pgx

package bankadmin

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The backup/restore pair must work against a live database with the server's
// own schema: backup captures the server's binary COPY output, restore replays
// it into another schema, and the rows come back.

// openMigratedBackend opens a throwaway schema with the full migration series.
func openMigratedBackend(t *testing.T, prefix string) *storage.PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the backup round-trip test was not run")
	}
	schema := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	store, err := (&storage.PostgresBackend{}).Open(context.Background(), storage.BackendConfig{
		DatabaseURL: url,
		Schema:      schema,
	})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*storage.PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}

// TestBackupRestoreRoundTrip backs a seeded schema up and restores the archive
// into a second, empty schema: the seeded row must come back through the
// archive's binary COPY streams.
func TestBackupRestoreRoundTrip(t *testing.T) {
	source := openMigratedBackend(t, "bktrt")
	target := openMigratedBackend(t, "bktrt")
	ctx := context.Background()

	if _, err := source.CreateBank(ctx, storage.Bank{BankID: "roundtrip", Name: "Round Trip"}); err != nil {
		t.Fatalf("seed bank: %v", err)
	}

	path := filepath.Join(t.TempDir(), "roundtrip.zip")
	var backupOut bytes.Buffer
	if err := RunBackup(ctx, source, BackupFlags{Output: path}, &backupOut); err != nil {
		t.Fatalf("backup: %v", err)
	}

	if _, err := target.GetBank(ctx, "roundtrip"); err == nil {
		t.Fatal("the target schema must start empty")
	}
	if err := RunRestore(ctx, target, RestoreFlags{Input: path, Yes: true}, nil, &bytes.Buffer{}); err != nil {
		t.Fatalf("restore: %v", err)
	}

	bank, err := target.GetBank(ctx, "roundtrip")
	if err != nil {
		t.Fatalf("the restored archive must carry the seeded bank: %v", err)
	}
	if bank.Name != "Round Trip" {
		t.Fatalf("restored bank name = %q", bank.Name)
	}
}
