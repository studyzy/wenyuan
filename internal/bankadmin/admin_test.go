package bankadmin

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// fakeTableStore adds the optional admin capability to the in-memory store so
// the archive format and its ordering can be exercised without a live
// PostgreSQL. The embedded store supplies the rest of the Store contract.
type fakeTableStore struct {
	*storage.MemoryStore
	columns     map[string][]storage.AdminColumn
	dump        map[string]string
	truncated   []string
	loaded      []string
	binaryLoads []storage.BinaryLoad
}

func newFakeTableStore() *fakeTableStore {
	return &fakeTableStore{
		MemoryStore: storage.NewMemoryStore("public"),
		columns:     map[string][]storage.AdminColumn{},
		dump:        map[string]string{},
	}
}

func (f *fakeTableStore) TableColumns(_ context.Context, table string) ([]storage.AdminColumn, error) {
	if columns, ok := f.columns[table]; ok {
		return columns, nil
	}
	return []storage.AdminColumn{{Name: "id", Type: "text"}}, nil
}

func (f *fakeTableStore) DumpTable(_ context.Context, table string) ([]byte, error) {
	if data, ok := f.dump[table]; ok {
		return []byte(data), nil
	}
	return []byte("[]"), nil
}

func (f *fakeTableStore) LoadTable(_ context.Context, table string, _ []byte) error {
	f.loaded = append(f.loaded, table)
	return nil
}

func (f *fakeTableStore) TruncateTables(_ context.Context, tables []string) error {
	f.truncated = append(f.truncated, tables...)
	return nil
}

func (f *fakeTableStore) RestoreBinary(_ context.Context, loads []storage.BinaryLoad) error {
	for _, load := range loads {
		f.truncated = append(f.truncated, load.Table)
		f.loaded = append(f.loaded, load.Table)
	}
	f.binaryLoads = append(f.binaryLoads, loads...)
	return nil
}

// backup lists every persistent table: the set is derived from the migrations,
// and the dependency order is the one a restore relies on.
func TestBackupWritesEveryPersistentTable(t *testing.T) {
	store := newFakeTableStore()
	store.dump["banks"] = `[{"bank_id":"old"}]`
	path := filepath.Join(t.TempDir(), "backup.zip")
	var out bytes.Buffer

	if err := RunBackup(context.Background(), store, BackupFlags{Output: path}, &out); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if !strings.Contains(out.String(), "across") {
		t.Fatalf("backup output = %q", out.String())
	}

	want, err := storage.PersistentTables()
	if err != nil {
		t.Fatal(err)
	}
	manifest := readManifest(t, path)
	if len(manifest.Tables) != len(want) {
		t.Fatalf("backup holds %d tables, want %d", len(manifest.Tables), len(want))
	}
	if manifest.Tables["banks"].Rows != 1 {
		t.Fatalf("banks rows = %d, want 1", manifest.Tables["banks"].Rows)
	}
}

// restore truncates in reverse dependency order and loads forward, so foreign
// keys survive; it replaces the target's rows with the archive's.
func TestRestoreOrdersTruncateAndLoad(t *testing.T) {
	source := newFakeTableStore()
	source.dump["banks"] = `[{"bank_id":"b1"}]`
	path := filepath.Join(t.TempDir(), "backup.zip")
	if err := RunBackup(context.Background(), source, BackupFlags{Output: path}, &bytes.Buffer{}); err != nil {
		t.Fatalf("backup: %v", err)
	}

	target := newFakeTableStore()
	var out bytes.Buffer
	if err := RunRestore(context.Background(), target, RestoreFlags{Input: path, Yes: true}, nil, &out); err != nil {
		t.Fatalf("restore: %v", err)
	}

	order, err := storage.PersistentTables()
	if err != nil {
		t.Fatal(err)
	}
	if len(target.loaded) != len(order) {
		t.Fatalf("loaded %d tables, want %d", len(target.loaded), len(order))
	}
	// Load order must place a parent before each of its children, and truncate
	// order must be its exact reverse.
	position := map[string]int{}
	for i, name := range target.loaded {
		position[name] = i
	}
	for _, edge := range [][2]string{
		{"banks", "async_operations"},
		{"documents", "chunks"},
		{"chunks", "memory_units"},
		{"entities", "memory_links"},
		{"mental_models", "knowledge_pages"},
	} {
		if position[edge[0]] > position[edge[1]] {
			t.Fatalf("load order places %s before its parent %s", edge[0], edge[1])
		}
	}
	// Every table is cleared, and in one statement: PostgreSQL refuses a
	// per-table TRUNCATE as soon as another table references the one named, so
	// the set has to travel together.
	if len(target.truncated) != len(order) {
		t.Fatalf("truncated %d tables, want %d: %v", len(target.truncated), len(order), target.truncated)
	}
	for i, name := range target.truncated {
		if name != order[i] {
			t.Fatalf("truncated %v, want the persistent set %v", target.truncated, order)
		}
	}
	if !strings.Contains(out.String(), "restored") {
		t.Fatalf("restore output = %q", out.String())
	}
}

// A backup whose column types no longer match the target is refused before any
// truncate happens, so the target is never left partially loaded.
func TestRestoreRefusesSchemaMismatch(t *testing.T) {
	source := newFakeTableStore()
	source.columns["banks"] = []storage.AdminColumn{{Name: "bank_id", Type: "text"}}
	path := filepath.Join(t.TempDir(), "backup.zip")
	if err := RunBackup(context.Background(), source, BackupFlags{Output: path}, &bytes.Buffer{}); err != nil {
		t.Fatalf("backup: %v", err)
	}

	target := newFakeTableStore()
	target.columns["banks"] = []storage.AdminColumn{{Name: "bank_id", Type: "uuid"}}
	err := RunRestore(context.Background(), target, RestoreFlags{Input: path, Yes: true}, nil, &bytes.Buffer{})
	if err == nil {
		t.Fatal("a mismatched column type must be refused")
	}
	if !strings.Contains(err.Error(), "banks") {
		t.Fatalf("refusal must name the offending table: %v", err)
	}
	if len(target.truncated) != 0 {
		t.Fatalf("a refused restore must not truncate anything: %v", target.truncated)
	}
}

// The in-memory store has no persistent schema to enumerate, so both commands
// refuse it with a clear message rather than writing or loading a partial
// archive.
func TestBackupAndRestoreRefuseMemoryStore(t *testing.T) {
	store := storage.NewMemoryStore("public")
	path := filepath.Join(t.TempDir(), "backup.zip")
	if err := RunBackup(context.Background(), store, BackupFlags{Output: path}, &bytes.Buffer{}); err == nil {
		t.Fatal("backup must refuse the in-memory store")
	} else if !strings.Contains(err.Error(), "in-memory") {
		t.Fatalf("refusal message = %q", err.Error())
	}
	if err := RunRestore(context.Background(), store, RestoreFlags{Input: path, Yes: true}, nil, &bytes.Buffer{}); err == nil {
		t.Fatal("restore must refuse the in-memory store")
	} else if !strings.Contains(err.Error(), "in-memory") {
		t.Fatalf("refusal message = %q", err.Error())
	}
}

// The backup and restore flags parse and require their path.
func TestParseBackupAndRestoreFlags(t *testing.T) {
	flags, err := ParseBackup([]string{"--output", "out.zip"})
	if err != nil || flags.Output != "out.zip" {
		t.Fatalf("backup flags = %+v, err = %v", flags, err)
	}
	if _, err := ParseBackup(nil); err == nil {
		t.Fatal("backup without an output must be rejected")
	}
	restore, err := ParseRestore([]string{"-i", "in.zip"})
	if err != nil || restore.Input != "in.zip" {
		t.Fatalf("restore flags = %+v, err = %v", restore, err)
	}
	if _, err := ParseRestore(nil); err == nil {
		t.Fatal("restore without an input must be rejected")
	}
}

// run-db-migration applies the store's migration series.
func TestRunDBMigrationApplies(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if store.Migrated() {
		t.Fatal("a fresh store must not report itself migrated")
	}
	var out bytes.Buffer
	if err := RunDBMigration(context.Background(), store, DBMigrationFlags{}, &out); err != nil {
		t.Fatalf("migration: %v", err)
	}
	if !store.Migrated() {
		t.Fatal("run-db-migration must apply the series")
	}
	if !strings.Contains(out.String(), "completed") {
		t.Fatalf("migration output = %q", out.String())
	}
}

// The embedding dimension is enforced against the width the series fixes, so a
// deployment pointed at a provider the schema cannot hold is told up front.
func TestRunDBMigrationEnforcesEmbeddingDimension(t *testing.T) {
	store := storage.NewMemoryStore("public")
	err := RunDBMigration(context.Background(), store, DBMigrationFlags{EmbeddingDimension: 1536}, &bytes.Buffer{})
	if err == nil || !strings.Contains(err.Error(), "1536") {
		t.Fatalf("err = %v", err)
	}
	if store.Migrated() {
		t.Fatal("a refused migration must not have run")
	}
	if err := RunDBMigration(context.Background(), store,
		DBMigrationFlags{EmbeddingDimension: provider.EmbeddingDimensions}, &bytes.Buffer{}); err != nil {
		t.Fatalf("the series' own width must be accepted: %v", err)
	}
}

// Decommissioning a worker releases its claims so another worker can claim the
// work immediately.
func TestDecommissionWorkerReleasesClaim(t *testing.T) {
	store := seedOperationStore(t)
	ctx := context.Background()
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	released, err := RunDecommissionWorker(ctx, store, DecommissionFlags{Worker: "worker-1", Yes: true}, nil, &out)
	if err != nil {
		t.Fatalf("decommission: %v", err)
	}
	if released != 1 {
		t.Fatalf("released %d task(s), want 1", released)
	}
	again, err := store.ClaimNextOperation(ctx, "worker-2", time.Hour)
	if err != nil {
		t.Fatalf("a released claim must become claimable: %v", err)
	}
	if again.WorkerID != "worker-2" {
		t.Fatalf("claim holder = %q, want worker-2", again.WorkerID)
	}
}

// Decommissioning one worker leaves another worker's claim untouched.
func TestDecommissionWorkerLeavesOtherWorkers(t *testing.T) {
	store := seedOperationStore(t)
	ctx := context.Background()
	// Make the first claim before the second is queued, so each worker holds a
	// distinct operation.
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-2", BankID: "bank-a", OperationType: "retain", Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Hour); err != nil {
		t.Fatal(err)
	}

	if _, err := RunDecommissionWorker(ctx, store, DecommissionFlags{Worker: "worker-1", Yes: true}, nil, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	held, err := store.GetOperation(ctx, "bank-a", "op-2")
	if err != nil {
		t.Fatal(err)
	}
	if held.WorkerID != "worker-2" || held.Status != storage.StatusProcessing {
		t.Fatalf("decommissioning worker-1 must not touch worker-2: %+v", held)
	}
}

// decommission-workers releases every processing operation regardless of owner
// and leaves terminal and pending work alone.
func TestDecommissionAllWorkers(t *testing.T) {
	store := seedOperationStore(t)
	ctx := context.Background()
	if _, err := store.ClaimNextOperation(ctx, "worker-1", time.Hour); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-done", BankID: "bank-a", OperationType: "retain", Status: storage.StatusCompleted,
	}); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	released, err := RunDecommissionWorkers(ctx, store, DecommissionWorkersFlags{Yes: true}, nil, &out)
	if err != nil {
		t.Fatal(err)
	}
	if released != 1 {
		t.Fatalf("released %d task(s), want 1", released)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-2", time.Hour); err != nil {
		t.Fatalf("released work must be claimable: %v", err)
	}
}

// The decommission-worker flag parser requires a worker identifier.
func TestParseDecommissionWorkerFlags(t *testing.T) {
	flags, err := ParseDecommissionWorker([]string{"--worker", "w1"})
	if err != nil || flags.Worker != "w1" {
		t.Fatalf("flags = %+v, err = %v", flags, err)
	}
	if _, err := ParseDecommissionWorker(nil); err == nil {
		t.Fatal("a missing worker must be rejected")
	}
}

// seedOperationStore builds a store with one bank and one pending operation.
func seedOperationStore(t *testing.T) *storage.MemoryStore {
	t.Helper()
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "bank-a", Name: "bank-a"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-1", BankID: "bank-a", OperationType: "retain", Status: storage.StatusPending,
	}); err != nil {
		t.Fatal(err)
	}
	return store
}

// readManifest decodes the manifest of an archive the backup wrote.
func readManifest(t *testing.T, path string) backupManifest {
	t.Helper()
	reader, err := zip.OpenReader(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	data, err := readZipEntry(&reader.Reader, "manifest.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest backupManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
