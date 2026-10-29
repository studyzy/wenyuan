package storage

import (
	"context"
	"testing"
	"time"
)

// Every scheduled retry merges the cause of the attempt just lost into
// result_metadata as last_retry_error (worker/poller.py:1095-1098), because
// completion clears error_message and the cause would otherwise be lost.
func TestScheduleOperationRetryMergesLastRetryError(t *testing.T) {
	ctx := context.Background()
	next := time.Now().UTC().Add(time.Minute)
	tests := []struct {
		name   string
		result map[string]any // the processor's report, when it made one
		want   map[string]any // the merged metadata after the retry
	}{
		{
			name:   "a retry with no reported result still records the cause",
			result: nil,
			want:   map[string]any{"last_retry_error": "provider 500"},
		},
		{
			name:   "the reported result merges beside the cause",
			result: map[string]any{"append_conflict_defers": 2},
			want:   map[string]any{"last_retry_error": "provider 500", "append_conflict_defers": 2},
		},
		{
			name:   "the cause is the retry's own, not a reported key",
			result: map[string]any{"last_retry_error": "stale"},
			want:   map[string]any{"last_retry_error": "provider 500"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore("public")
			if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
				t.Fatal(err)
			}
			// Earlier-attempt metadata must survive the merge.
			op, err := store.PutOperation(ctx, Operation{
				OperationID: newID(), BankID: "b1", OperationType: OperationTypeConsolidate,
				Status: StatusProcessing, Payload: map[string]any{"k": "v"},
				ResultMetadata: map[string]any{"earlier": "attempt"},
			})
			if err != nil {
				t.Fatal(err)
			}
			merge := func() error {
				if test.result == nil {
					return store.ScheduleOperationRetry(ctx, op.OperationID, 1, next, "provider 500")
				}
				return store.ScheduleOperationRetryWithResult(ctx, op.OperationID, 1, next, "provider 500", test.result)
			}
			if err := merge(); err != nil {
				t.Fatal(err)
			}
			got, err := store.GetOperation(ctx, "b1", op.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			for key, want := range test.want {
				if got.ResultMetadata[key] != want {
					t.Fatalf("result_metadata[%q] = %v, want %v", key, got.ResultMetadata[key], want)
				}
			}
			if got.ResultMetadata["earlier"] != "attempt" {
				t.Fatalf("result_metadata[earlier] = %v, want it kept", got.ResultMetadata["earlier"])
			}
			if got.Status != StatusPending || got.Error != "provider 500" {
				t.Fatalf("operation = %+v, want it back in the queue with the cause", got)
			}
		})
	}
}
