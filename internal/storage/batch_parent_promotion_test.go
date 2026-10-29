package storage

import (
	"context"
	"testing"
	"time"
)

// The parent's promotion branches per outcome, exactly as the reference writes
// them (engine/memory_engine.py:6021-6068): completed folds the children's
// outcome counters and stamps completed_at, failed writes only status, error
// message and updated_at, and cancelled writes only status and updated_at.
func TestFinalizeBatchParentBranchesPerOutcome(t *testing.T) {
	// A one-token budget splits the two items into two children.
	const subBatchTokens = 1
	tests := []struct {
		name          string
		settle        func(t *testing.T, store *MemoryStore, ctx context.Context, children []ChildOperation)
		wantStatus    OperationStatus
		wantError     string
		wantCompleted bool
		wantAggregate bool // completed folds the counters; failed/cancelled must not
	}{
		{
			name: "all completed",
			settle: func(t *testing.T, store *MemoryStore, ctx context.Context, children []ChildOperation) {
				for index, child := range children {
					if _, err := store.AdvanceOperationWithResult(ctx, child.OperationID, StatusCompleted, "",
						map[string]any{"unit_ids_count": index + 1, "extraction_errors_count": 0}); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantStatus:    StatusCompleted,
			wantError:     "",
			wantCompleted: true,
			wantAggregate: true,
		},
		{
			name: "one child failed",
			settle: func(t *testing.T, store *MemoryStore, ctx context.Context, children []ChildOperation) {
				if _, err := store.AdvanceOperation(ctx, children[0].OperationID, StatusFailed, "extraction failed"); err != nil {
					t.Fatal(err)
				}
				if _, err := store.AdvanceOperationWithResult(ctx, children[1].OperationID, StatusCompleted, "",
					map[string]any{"unit_ids_count": 2}); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus:    StatusFailed,
			wantError:     "extraction failed",
			wantCompleted: false,
			wantAggregate: false,
		},
		{
			name: "a failure outranks a cancellation",
			settle: func(t *testing.T, store *MemoryStore, ctx context.Context, children []ChildOperation) {
				if _, err := store.CancelOperation(ctx, "b1", children[0].OperationID); err != nil {
					t.Fatal(err)
				}
				if _, err := store.AdvanceOperation(ctx, children[1].OperationID, StatusFailed, "provider down"); err != nil {
					t.Fatal(err)
				}
			},
			wantStatus:    StatusFailed,
			wantError:     "provider down",
			wantCompleted: false,
			wantAggregate: false,
		},
		{
			name: "all cancelled",
			settle: func(t *testing.T, store *MemoryStore, ctx context.Context, children []ChildOperation) {
				for _, child := range children {
					if _, err := store.CancelOperation(ctx, "b1", child.OperationID); err != nil {
						t.Fatal(err)
					}
				}
			},
			wantStatus:    StatusCancelled,
			wantError:     "",
			wantCompleted: false,
			wantAggregate: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore("public")
			ctx := context.Background()
			submission, err := store.SubmitAsyncRetain(ctx, RetainRequest{
				BankID: "b1",
				Items:  []RetainItem{{Content: "Alice moved to Berlin."}, {Content: "Alice plays cello."}},
			}, subBatchTokens)
			if err != nil {
				t.Fatal(err)
			}
			children, err := store.GetOperationChildren(ctx, "b1", submission.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			before, err := store.GetOperation(ctx, "b1", submission.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			test.settle(t, store, ctx, children)
			promoted, updated, err := store.FinalizeBatchParent(ctx, "b1", submission.OperationID)
			if err != nil {
				t.Fatal(err)
			}
			if !updated {
				t.Fatalf("parent = %+v, want it promoted", promoted)
			}
			if promoted.Status != test.wantStatus {
				t.Fatalf("status = %s, want %s", promoted.Status, test.wantStatus)
			}
			if promoted.Error != test.wantError {
				t.Fatalf("error = %q, want %q", promoted.Error, test.wantError)
			}
			if test.wantCompleted && promoted.CompletedAt == nil {
				t.Fatal("completed_at = nil, want it stamped on completion")
			}
			if !test.wantCompleted && promoted.CompletedAt != nil {
				t.Fatalf("completed_at = %v, want it left unset", *promoted.CompletedAt)
			}
			// The aggregate rides the completed branch alone.
			if got := intFromMetadata(promoted.ResultMetadata, "unit_ids_count"); test.wantAggregate && got != 3 {
				t.Fatalf("unit_ids_count = %d, want the children's total of 3", got)
			} else if !test.wantAggregate && got != intFromMetadata(before.ResultMetadata, "unit_ids_count") {
				t.Fatalf("unit_ids_count = %d, want the parent's metadata untouched", got)
			}
		})
	}
}

// The failed parent inherits the most common non-empty message among its FAILED
// children verbatim (worker/poller.py:256-280): no "(N of M sub-batches
// failed)" suffix, no message from a sibling that did not fail, and the legacy
// generic string when no failed child carries a message.
func TestSummarizeChildErrorsVerbatim(t *testing.T) {
	tests := []struct {
		name     string
		messages []string
		want     string
	}{
		{
			name:     "single message passes through",
			messages: []string{"extraction failed"},
			want:     "extraction failed",
		},
		{
			name:     "most common message wins verbatim",
			messages: []string{"quota exceeded", "quota exceeded", "provider down"},
			want:     "quota exceeded",
		},
		{
			name:     "no suffix is appended",
			messages: []string{"quota exceeded", "quota exceeded", "provider down", "provider down"},
			want:     "quota exceeded",
		},
		{
			name:     "empty messages fall back to the legacy string",
			messages: []string{"", "", ""},
			want:     "One or more sub-batches failed",
		},
		{
			name:     "no messages at all falls back to the legacy string",
			messages: nil,
			want:     "One or more sub-batches failed",
		},
		{
			name:     "surrounding whitespace is stripped",
			messages: []string{"  quota exceeded  "},
			want:     "quota exceeded",
		},
		{
			name:     "the same text with and without padding is one message",
			messages: []string{" quota exceeded", "quota exceeded ", "provider down"},
			want:     "quota exceeded",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := summarizeChildErrors(test.messages); got != test.want {
				t.Fatalf("summarizeChildErrors(%q) = %q, want %q", test.messages, got, test.want)
			}
		})
	}
}

// Only a failed child contributes its message to the parent: a completed
// sibling carrying a stale error string must not name the parent's cause.
func TestAggregateChildrenReadsOnlyFailedMessages(t *testing.T) {
	children := []batchChild{
		{Status: StatusFailed, ErrorMessage: "provider down"},
		{Status: StatusCompleted, ErrorMessage: "stale error from an earlier attempt"},
		{Status: StatusCancelled, ErrorMessage: "cancelled mid-flight"},
	}
	status, message, _ := aggregateChildren(children)
	if status != StatusFailed {
		t.Fatalf("status = %s, want failed", status)
	}
	if message != "provider down" {
		t.Fatalf("message = %q, want the failed child's own", message)
	}
}

// A cancelled parent keeps the write the reference makes: status and
// updated_at only, so its recorded error and completion time are untouched by
// the promotion (memory_engine.py:6037-6047).
func TestFinalizeBatchParentCancelledLeavesErrorUntouched(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	submission, err := store.SubmitAsyncRetain(ctx, RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: "Alice moved to Berlin."}, {Content: "Alice plays cello."}},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	children, err := store.GetOperationChildren(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	for _, child := range children {
		if _, err := store.CancelOperation(ctx, "b1", child.OperationID); err != nil {
			t.Fatal(err)
		}
	}
	promoted, updated, err := store.FinalizeBatchParent(ctx, "b1", submission.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	if !updated || promoted.Status != StatusCancelled {
		t.Fatalf("parent = %+v (updated=%v), want cancelled", promoted, updated)
	}
	if promoted.CompletedAt != nil {
		t.Fatalf("completed_at = %v, want nil for a stopped batch", *promoted.CompletedAt)
	}
	if promoted.UpdatedAt.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("updated_at = %v, want it stamped now", promoted.UpdatedAt)
	}
}
