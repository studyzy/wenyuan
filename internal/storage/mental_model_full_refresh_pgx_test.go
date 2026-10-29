//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"
)

// fullRefreshStore opens a migrated store in its own schema and registers its
// teardown.
func fullRefreshStore(t *testing.T) *PostgresStore {
	t.Helper()
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the full-refresh stamp test was not run")
	}
	schema := fmt.Sprintf("mmfull_%d", time.Now().UnixNano())
	store, err := (&PostgresBackend{}).Open(context.Background(), BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	t.Cleanup(func() {
		_ = pg.DropSchemaForTest(schema)
		_ = pg.Close()
	})
	if err := pg.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return pg
}

// last_full_refreshed_at is a column of its own: the full-refresh stamp moves it
// while the refresh clock is untouched, a get→patch→put round trip preserves it,
// and the discovery routine hands it back with the full schedule it gates.
func TestPostgresFullRefreshStamp(t *testing.T) {
	pg := fullRefreshStore(t)
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutMentalModel(ctx, MentalModel{
		ID: "mm-1", BankID: "b1", Subtype: SubtypePinned, Name: "m", SourceQuery: "q",
		Trigger: map[string]any{"mode": "delta", "full_refresh_cron": "0 3 * * 0"},
	}); err != nil {
		t.Fatal(err)
	}
	fresh, err := pg.GetMentalModel(ctx, "b1", "mm-1")
	if err != nil {
		t.Fatal(err)
	}
	if fresh.LastFullRefreshedAt != nil {
		t.Fatalf("last_full_refreshed_at = %v on a new model, want null", fresh.LastFullRefreshedAt)
	}
	discovered, err := pg.MentalModelsWithSchedule(ctx, pg.defaultSchema)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if len(discovered) != 1 || discovered[0].FullRefreshCron != "0 3 * * 0" {
		t.Fatalf("discovery = %+v, want the model with its full schedule", discovered)
	}
	if !discovered[0].LastFullRefreshedAt.IsZero() {
		t.Fatalf("discovery reported last_full_refreshed_at = %v, want zero", discovered[0].LastFullRefreshedAt)
	}

	stamp := time.Date(2026, 10, 1, 3, 0, 0, 0, time.UTC)
	before, _ := pg.GetMentalModel(ctx, "b1", "mm-1")
	if err := pg.MarkMentalModelFullRefresh(ctx, "b1", "mm-1", stamp); err != nil {
		t.Fatalf("stamp: %v", err)
	}
	stamped, err := pg.GetMentalModel(ctx, "b1", "mm-1")
	if err != nil {
		t.Fatal(err)
	}
	if stamped.LastFullRefreshedAt == nil || !stamped.LastFullRefreshedAt.Equal(stamp) {
		t.Fatalf("last_full_refreshed_at = %v, want %v", stamped.LastFullRefreshedAt, stamp)
	}
	// The other clock did not move: a full stamp is not a refresh completion.
	if !stamped.LastRefreshedAt.Equal(before.LastRefreshedAt) {
		t.Fatalf("last_refreshed_at moved from %v to %v on a full stamp", before.LastRefreshedAt, stamped.LastRefreshedAt)
	}
	// A rename is a get→patch→put round trip through the shared projection; the
	// stamp must survive it.
	renamed, err := pg.UpdateMentalModel(ctx, "b1", "mm-1", MentalModelPatch{Name: strPtrFull("renamed")})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.LastFullRefreshedAt == nil || !renamed.LastFullRefreshedAt.Equal(stamp) {
		t.Fatalf("last_full_refreshed_at after a patch = %v, want %v", renamed.LastFullRefreshedAt, stamp)
	}
	discovered, err = pg.MentalModelsWithSchedule(ctx, pg.defaultSchema)
	if err != nil {
		t.Fatalf("discovery: %v", err)
	}
	if len(discovered) != 1 || !discovered[0].LastFullRefreshedAt.Equal(stamp) {
		t.Fatalf("discovery = %+v, want the stamped full clock", discovered)
	}
}

// PromoteOperationPayload writes the one-run mode into a queued refresh's
// task_payload and refuses a row that is no longer pending, which is what keeps
// a "full refresh now" from changing a run that already chose its mode.
func TestPostgresPromoteQueuedRefresh(t *testing.T) {
	pg := fullRefreshStore(t)
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	queued, err := pg.EnqueueOperation(ctx, EnqueueRequest{
		Operation: Operation{
			BankID:        "b1",
			OperationType: OperationTypeRefreshMentalModel,
			Payload:       map[string]any{MentalModelIDPayloadKey: "mm-1"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	promoted, err := PromoteQueuedRefreshToFull(ctx, pg, "b1", queued.Operation.OperationID)
	if err != nil {
		t.Fatalf("promote: %v", err)
	}
	if !promoted {
		t.Fatal("a queued refresh must be promoted to full")
	}
	stored, err := pg.GetOperation(ctx, "b1", queued.Operation.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Payload[RefreshModePayloadKey] != "full" {
		t.Fatalf("payload = %+v, want the full mode", stored.Payload)
	}
	// A second promote is a no-op: the key already holds the value.
	again, err := PromoteQueuedRefreshToFull(ctx, pg, "b1", queued.Operation.OperationID)
	if err != nil || again {
		t.Fatalf("second promote = (%v, %v), want a no-op", again, err)
	}
}

// strPtrFull keeps the patch seed readable.
func strPtrFull(value string) *string { return &value }
