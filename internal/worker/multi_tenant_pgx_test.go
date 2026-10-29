//go:build pgx

package worker

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

type staticTenants struct {
	tenants []tenant.Tenant
}

func (s staticTenants) Authenticate(context.Context, tenant.Request) (tenant.Context, error) {
	return tenant.Context{}, tenant.ErrAuthentication
}

func (s staticTenants) ListTenants(context.Context) ([]tenant.Tenant, error) {
	return s.tenants, nil
}

type schemaRecorder struct {
	mu      sync.Mutex
	schemas map[string]int
}

func (r *schemaRecorder) Process(ctx context.Context, _ storage.Operation) error {
	schema, _ := storage.SchemaFrom(ctx)
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.schemas == nil {
		r.schemas = map[string]int{}
	}
	r.schemas[schema]++
	return nil
}

func (r *schemaRecorder) snapshot() map[string]int {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := map[string]int{}
	for key, value := range r.schemas {
		out[key] = value
	}
	return out
}

func TestConsumerDrainsMultipleTenantsPG(t *testing.T) {
	url := os.Getenv("WENYUAN_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("WENYUAN_TEST_DATABASE_URL is not set; the multi-tenant worker test was not run")
	}
	db, err := storage.OpenDatabaseWithBudget(url, storage.PoolBudget{MaxOpenConns: 4, MaxIdleConns: 2})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	suffix := time.Now().UnixNano()
	schemaA := fmt.Sprintf("w_a_%d", suffix)
	schemaB := fmt.Sprintf("w_b_%d", suffix)
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, schema := range []string{schemaA, schemaB} {
			_, _ = db.ExecContext(cleanupCtx, "DROP SCHEMA IF EXISTS \""+schema+"\" CASCADE")
		}
		_ = db.Close()
	})

	for _, schema := range []string{schemaA, schemaB} {
		if err := storage.NewPostgresStore(db, schema).Migrate(ctx); err != nil {
			t.Fatalf("migrate %s: %v", schema, err)
		}
	}
	store := storage.NewMultiTenantPostgresStore(db, schemaA)
	ctxA, err := storage.WithSchema(ctx, schemaA)
	if err != nil {
		t.Fatal(err)
	}
	ctxB, err := storage.WithSchema(ctx, schemaB)
	if err != nil {
		t.Fatal(err)
	}
	for _, schemaCtx := range []context.Context{ctxA, ctxB} {
		if _, err := store.CreateBank(schemaCtx, storage.Bank{BankID: "shared", Name: "bank"}); err != nil {
			t.Fatalf("create bank: %v", err)
		}
	}
	now := time.Now().UTC()
	for i, schemaCtx := range []context.Context{ctxA, ctxB} {
		opID := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		if _, err := store.PutOperation(schemaCtx, storage.Operation{
			OperationID: opID, BankID: "shared", OperationType: storage.OperationTypeTransferImport,
			Status: storage.StatusPending, CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatalf("put operation: %v", err)
		}
	}

	recorder := &schemaRecorder{}
	consumer := New(store, recorder, Config{
		WorkerID:      "multi-tenant-test",
		Lease:         time.Minute,
		Concurrency:   2,
		PollInterval:  10 * time.Millisecond,
		Tenants:       staticTenants{tenants: []tenant.Tenant{{Schema: schemaA}, {Schema: schemaB}}},
		TenantRefresh: time.Minute,
		Logger:        quietLogger(),
	})
	runCtx, stop := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- consumer.Run(runCtx) }()

	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if len(recorder.snapshot()) == 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("consumer did not stop")
	}

	seen := recorder.snapshot()
	if seen[schemaA] != 1 || seen[schemaB] != 1 {
		t.Fatalf("processed schemas = %+v, want one operation each", seen)
	}
}
