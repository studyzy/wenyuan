//go:build pgx

package storage

import (
	"context"
	"testing"
)

// The PostgreSQL promotion branches per outcome exactly as the in-memory one
// does: only a completed parent folds the children's outcome counters and
// stamps completed_at; a failed one writes status, error message and
// updated_at; a cancelled one status and updated_at.
func TestPostgresFinalizeBatchParentBranchesPerOutcome(t *testing.T) {
	pg, _ := newClearMemoriesStore(t, "batch_parent")
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	type childOutcome struct {
		status OperationStatus
		err    string
		result map[string]any
	}
	tests := []struct {
		name          string
		children      []childOutcome
		wantStatus    OperationStatus
		wantError     string
		wantCompleted bool
		wantAggregate bool
	}{
		{
			name: "all completed",
			children: []childOutcome{
				{status: StatusCompleted, result: map[string]any{"unit_ids_count": 1}},
				{status: StatusCompleted, result: map[string]any{"unit_ids_count": 2}},
			},
			wantStatus:    StatusCompleted,
			wantCompleted: true,
			wantAggregate: true,
		},
		{
			name: "one child failed",
			children: []childOutcome{
				{status: StatusFailed, err: "extraction failed"},
				{status: StatusCompleted, result: map[string]any{"unit_ids_count": 2}},
			},
			wantStatus: StatusFailed,
			wantError:  "extraction failed",
		},
		{
			name: "all cancelled",
			children: []childOutcome{
				{status: StatusCancelled},
				{status: StatusCancelled},
			},
			wantStatus: StatusCancelled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			parentID := newID()
			if _, err := pg.PutOperation(ctx, Operation{
				OperationID: parentID, BankID: "b1", OperationType: AsyncRetainType,
				Status:         StatusProcessing,
				ResultMetadata: map[string]any{"is_parent": true, "items_count": 2},
			}); err != nil {
				t.Fatal(err)
			}
			for _, child := range test.children {
				childID := newID()
				metadata := map[string]any{"parent_operation_id": parentID}
				for key, value := range child.result {
					metadata[key] = value
				}
				if _, err := pg.PutOperation(ctx, Operation{
					OperationID: childID, BankID: "b1", OperationType: OperationTypeRetain,
					Status:         child.status,
					Error:          child.err,
					Payload:        map[string]any{"k": "v"},
					ResultMetadata: metadata,
				}); err != nil {
					t.Fatal(err)
				}
			}
			promoted, updated, err := pg.FinalizeBatchParent(ctx, "b1", parentID)
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
			if got := intFromMetadata(promoted.ResultMetadata, "unit_ids_count"); test.wantAggregate && got != 3 {
				t.Fatalf("unit_ids_count = %d, want the children's total of 3", got)
			} else if !test.wantAggregate && got != 0 {
				t.Fatalf("unit_ids_count = %d, want the parent's metadata untouched", got)
			}
		})
	}
}
