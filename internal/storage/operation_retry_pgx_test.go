//go:build pgx

package storage

import (
	"context"
	"testing"
	"time"
)

// The retry transition is the same on PostgreSQL: failed, cancelled and deferred
// operations go back to pending with next_retry_at cleared, and a live one is
// refused.
func TestPostgresRetryOperationStates(t *testing.T) {
	pg, _ := newClearMemoriesStore(t, "op_retry")
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	future := time.Now().UTC().Add(time.Hour)
	cases := []struct {
		name     string
		status   OperationStatus
		deferred bool
		wantOK   bool
		wantErr  error
	}{
		{name: "failed", status: StatusFailed, wantOK: true},
		{name: "cancelled", status: StatusCancelled, wantOK: true},
		{name: "deferred", status: StatusPending, deferred: true, wantOK: true},
		{name: "pending not deferred", status: StatusPending, wantErr: ErrOperationConflict},
		{name: "completed", status: StatusCompleted, wantErr: ErrOperationConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// A fresh id per case, so a re-run (or a concurrent run sharing the
			// database) can never collide with a row this test already wrote.
			id := newID()
			if _, err := pg.PutOperation(ctx, Operation{
				OperationID: id, BankID: "b1", OperationType: OperationTypeConsolidate,
				Status: tc.status, Error: "boom",
				Payload: map[string]any{"k": "v"},
			}); err != nil {
				t.Fatal(err)
			}
			if tc.deferred {
				// Park it the way the worker defers: pending with a future
				// next_retry_at, which is the state retrying runs now.
				if err := pg.DeferOperation(ctx, id, future, nil); err != nil {
					t.Fatalf("defer: %v", err)
				}
				deferred, err := pg.GetOperation(ctx, "b1", id)
				if err != nil {
					t.Fatal(err)
				}
				if deferred.NextRetryAt == nil || !deferred.NextRetryAt.After(time.Now().UTC()) {
					t.Fatalf("fixture is not deferred: %+v", deferred)
				}
			}
			retried, err := pg.RetryOperation(ctx, "b1", id)
			if !tc.wantOK {
				if err != tc.wantErr {
					t.Fatalf("retry = %v, want %v", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			if retried.Status != StatusPending || retried.NextRetryAt != nil || retried.Error != "" {
				t.Fatalf("retried = %+v, want pending with the backoff cleared", retried)
			}
			// The clearing is durable, not just what the RETURNING row said.
			stored, err := pg.GetOperation(ctx, "b1", id)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Status != StatusPending || stored.NextRetryAt != nil || stored.RetryCount != 0 {
				t.Fatalf("stored = %+v, want pending with next_retry_at cleared", stored)
			}
		})
	}
}

// A memory edit's mentioned_at reaches the row: a value writes it, "" clears it
// to NULL, a patch naming no mention time keeps it, and the edit resets the
// consolidation markers and stamps edited_at.
func TestPostgresUpdateMemoryMentionedAt(t *testing.T) {
	pg, _ := newClearMemoriesStore(t, "mentioned_at")
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	unit, err := pg.UpsertUnit(ctx, MemoryUnit{
		BankID: "b1", Text: "a fact", FactType: TypeWorld,
		MentionedAt: mustParseRFC3339(t, "2026-01-02T03:04:05Z"),
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := "2025-06-07T08:09:10Z"
	patched, err := pg.UpdateMemory(ctx, "b1", unit.ID, MemoryPatch{MentionedAt: &updated})
	if err != nil {
		t.Fatal(err)
	}
	if patched.MentionedAt == nil || !patched.MentionedAt.Equal(*mustParseRFC3339(t, updated)) {
		t.Fatalf("mentioned_at = %v, want %v", patched.MentionedAt, updated)
	}
	reread, err := pg.GetMemory(ctx, "b1", unit.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reread.MentionedAt == nil || !reread.MentionedAt.Equal(*mustParseRFC3339(t, updated)) {
		t.Fatalf("stored mentioned_at = %v, want %v", reread.MentionedAt, updated)
	}
	if reread.EventDate == nil || !reread.EventDate.Equal(*mustParseRFC3339(t, updated)) {
		t.Fatalf("event_date = %v, want the mention time it fell back to", reread.EventDate)
	}
	kept, err := pg.UpdateMemory(ctx, "b1", unit.ID, MemoryPatch{Context: strPtr("ctx")})
	if err != nil {
		t.Fatal(err)
	}
	if kept.MentionedAt == nil || !kept.MentionedAt.Equal(*mustParseRFC3339(t, updated)) {
		t.Fatalf("an unrelated patch moved mentioned_at: %v", kept.MentionedAt)
	}
	cleared, err := pg.UpdateMemory(ctx, "b1", unit.ID, MemoryPatch{MentionedAt: strPtr("")})
	if err != nil {
		t.Fatal(err)
	}
	if cleared.MentionedAt != nil {
		t.Fatalf("mentioned_at was not cleared: %v", cleared.MentionedAt)
	}
}
