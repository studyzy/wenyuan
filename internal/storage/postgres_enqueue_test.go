//go:build pgx

package storage

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"
)

// The enqueue guard is decided inside the inserting statement, so concurrent
// callers cannot each act on the same "nothing in flight" snapshot.
//
// This is the property the whole design rests on: the background loop runs in
// every process of a deployment with no leader election, so if the check were a
// read in front of the insert every process would insert its own copy.
func TestPostgresEnqueueIsAtomicUnderConcurrency(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the concurrent enqueue test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("enqueue_race_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	defer func() {
		_, _ = pg.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)
		_ = pg.Close()
	}()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pg.CreateBank(ctx, Bank{ID: "b1", Name: "b1"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}

	const callers = 8
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		inserted int
		seen     = map[string]int{}
		errs     []error
	)
	start := make(chan struct{})
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := EnqueueRequest{
				Operation: Operation{
					BankID:        "b1",
					OperationType: OperationTypeRefreshMentalModel,
					Payload:       map[string]any{"mental_model_id": "mm-1"},
				},
				DedupeByPayloadKey:       "mental_model_id",
				DedupeIncludesProcessing: true,
			}
			<-start
			result, err := pg.EnqueueOperation(ctx, req)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				errs = append(errs, err)
				return
			}
			if !result.Deduplicated {
				inserted++
			}
			seen[result.Operation.ID]++
		}()
	}
	close(start)
	wg.Wait()

	for _, err := range errs {
		t.Errorf("enqueue failed: %v", err)
	}
	if inserted != 1 {
		t.Errorf("%d callers inserted, want exactly one — the guard is not atomic", inserted)
	}
	if len(seen) != 1 {
		t.Errorf("callers saw %d distinct operations, want the one survivor", len(seen))
	}

	// The queue holds one row, not one per caller.
	page, err := pg.ListOperations(ctx, OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("queue holds %d operations, want 1", page.Total)
	}
}

// A claim does not start an operation whose serialization key another live
// claim holds, which is what keeps two refreshes of one model apart.
func TestPostgresClaimRespectsSerializationKey(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the serialization test was not run")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	schema := fmt.Sprintf("serialize_key_%d", time.Now().UnixNano())
	backend := &PostgresBackend{}
	store, err := backend.Open(ctx, BackendConfig{DatabaseURL: url, Schema: schema})
	if err != nil {
		t.Fatalf("open postgres backend: %v", err)
	}
	pg := store.(*PostgresStore)
	defer func() {
		_, _ = pg.db.ExecContext(context.Background(), `DROP SCHEMA IF EXISTS `+quoteIdent(schema)+` CASCADE`)
		_ = pg.Close()
	}()
	if err := pg.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := pg.CreateBank(ctx, Bank{ID: "b1", Name: "b1"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}

	put := func(payload map[string]any, key string) {
		t.Helper()
		if _, err := pg.EnqueueOperation(ctx, EnqueueRequest{Operation: Operation{
			BankID: "b1", OperationType: OperationTypeRefreshMentalModel,
			Payload: payload, SerializationKey: key,
		}}); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	put(map[string]any{"mental_model_id": "mm-1"}, "mental_model:mm-1")
	put(map[string]any{"mental_model_id": "mm-1"}, "mental_model:mm-1")

	claimed, err := pg.ClaimNextOperation(ctx, "worker-1", time.Minute)
	if err != nil {
		t.Fatalf("first claim: %v", err)
	}
	if claimed.SerializationKey != "mental_model:mm-1" {
		t.Fatalf("claim returned key %q, want the serialization key round-tripped", claimed.SerializationKey)
	}
	if _, err := pg.ClaimNextOperation(ctx, "worker-2", time.Minute); err != ErrOperationNotFound {
		t.Fatalf("the second operation must wait for the key: err = %v", err)
	}
	if err := pg.ReleaseOperationClaim(ctx, claimed.ID, "worker-1"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := pg.ClaimNextOperation(ctx, "worker-2", time.Minute); err != nil {
		t.Fatalf("after release the key is free: %v", err)
	}
}
