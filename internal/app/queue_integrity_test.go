package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/storage"
)

// A queued async retain for a bank that is then deleted must not run, and must
// not bring the bank back. The queue is purged with the bank, so a running
// consumer has nothing to pick up; if the row survived, the child's write would
// recreate the bank through the write path's lazy create and then fail.
func TestAsyncRetainForDeletedBankIsNotResurrected(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SubmitAsyncRetain(ctx, storage.RetainRequest{
		BankID: "b1",
		Items:  []storage.RetainItem{{Content: "Alice moved to Berlin.", DocumentID: "doc-1"}},
	}, storage.DefaultRetainSubBatchTokens); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DeleteBank(ctx, "b1"); err != nil {
		t.Fatal(err)
	}

	// Run the real consumer for a moment: the purge leaves it no work, and the
	// deleted bank must stay deleted.
	consumer := NewQueueConsumer(store, none.New(), nil, nil, config.WorkerConfig{
		ID: "w-integrity", MaxRetries: 1, InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
		Lease: time.Minute, Concurrency: 1, PollInterval: 2 * time.Millisecond,
	}, config.WorkerTuning{}, nil, ExtractionSettings{})
	runCtx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()
	_ = consumer.Run(runCtx)

	if _, err := store.GetBank(ctx, "b1"); !errors.Is(err, storage.ErrBankNotFound) {
		t.Fatalf("the deleted bank must not be resurrected, got %v", err)
	}
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a deleted bank's queued operations must be purged, got %+v", page.Operations)
	}
}
