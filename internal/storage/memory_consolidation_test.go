package storage

import (
	"context"
	"testing"
	"time"
)

// seedConsolidationUnit writes one unit with the given timestamps, returning
// pointers the test does not retain.
func seedConsolidationUnit(t *testing.T, store *MemoryStore, unit MemoryUnit) {
	t.Helper()
	if _, err := store.UpsertUnit(context.Background(), unit); err != nil {
		t.Fatalf("seed unit %s: %v", unit.ID, err)
	}
}

func at(year int, month time.Month, day int) *time.Time {
	parsed := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	return &parsed
}

// Consolidation works oldest-first: candidates come back in created_at order,
// not by recency of their mentioned_at, and an undated fact sorts last.
func TestMemoryConsolidationFactsOldestFirst(t *testing.T) {
	store := NewMemoryStore("public")
	seedConsolidationUnit(t, store, MemoryUnit{ID: "late", BankID: "b1", Text: "written last", FactType: TypeExperience, CreatedAt: at(2025, 3, 3), MentionedAt: at(1999, 1, 1)})
	seedConsolidationUnit(t, store, MemoryUnit{ID: "early", BankID: "b1", Text: "written first", FactType: TypeWorld, CreatedAt: at(2025, 3, 1), MentionedAt: at(2099, 1, 1)})
	seedConsolidationUnit(t, store, MemoryUnit{ID: "middle", BankID: "b1", Text: "written second", FactType: TypeExperience, CreatedAt: at(2025, 3, 2), MentionedAt: at(2099, 1, 1)})
	seedConsolidationUnit(t, store, MemoryUnit{ID: "undated", BankID: "b1", Text: "no created_at", FactType: TypeWorld})

	facts, err := store.ConsolidationFacts(context.Background(), "b1", 10)
	if err != nil {
		t.Fatalf("read facts: %v", err)
	}
	got := make([]string, 0, len(facts))
	for _, fact := range facts {
		got = append(got, fact.ID)
	}
	// The reference's `sorted(by created_at, None last)` — mentioned_at plays
	// no part in the order.
	want := []string{"early", "middle", "late", "undated"}
	if len(got) != len(want) {
		t.Fatalf("facts = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("facts = %v, want %v", got, want)
		}
	}
}

// A year-1/9999 placeholder on an observation's own columns reads as an
// unknown bound when the observation is updated: the merged bound is the real
// date when one exists, and NULL when only placeholders remain.
func TestMemoryUpdateObservationSkipsPlaceholderBounds(t *testing.T) {
	tests := []struct {
		name           string
		observationEnd *time.Time
		sourceEnd      *time.Time
		want           *time.Time
	}{
		{
			name:           "placeholder yields to a real date",
			observationEnd: at(9999, 12, 31),
			sourceEnd:      at(2025, 6, 2),
			want:           at(2025, 6, 2),
		},
		{
			name:           "placeholder survives no real date",
			observationEnd: at(9999, 12, 31),
			sourceEnd:      nil,
			want:           nil,
		},
		{
			name:           "placeholders alone merge to NULL",
			observationEnd: at(9999, 12, 31),
			sourceEnd:      at(1, 1, 1),
			want:           nil,
		},
		{
			name:           "real bound survives a placeholder",
			observationEnd: at(2025, 6, 2),
			sourceEnd:      at(9999, 12, 31),
			want:           at(2025, 6, 2),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore("public")
			seedConsolidationUnit(t, store, MemoryUnit{ID: "obs", BankID: "b1", Text: "an observation", FactType: TypeObservation, OccurredEnd: test.observationEnd})
			seedConsolidationUnit(t, store, MemoryUnit{ID: "src", BankID: "b1", Text: "a source fact", FactType: TypeExperience, OccurredEnd: test.sourceEnd})
			if err := store.UpdateObservation(context.Background(), "b1", "obs", "an observation, extended", []string{"src"}); err != nil {
				t.Fatalf("update observation: %v", err)
			}
			unit := store.units["obs"]
			switch {
			case test.want == nil && unit.OccurredEnd != nil:
				t.Fatalf("occurred_end = %v, want NULL", unit.OccurredEnd)
			case test.want != nil && (unit.OccurredEnd == nil || !unit.OccurredEnd.Equal(*test.want)):
				t.Fatalf("occurred_end = %v, want %v", unit.OccurredEnd, test.want)
			}
		})
	}
}

// A merge folds the merged-away observation's evidence into the survivor it
// named: the survivor's sources grow and its dates widen, its text untouched.
func TestMemoryMergeObservationEvidenceFolds(t *testing.T) {
	store := NewMemoryStore("public")
	seedConsolidationUnit(t, store, MemoryUnit{
		ID: "survivor", BankID: "b1", Text: "Bob uses open signup", FactType: TypeObservation,
		OccurredStart: at(2025, 6, 2), MentionedAt: at(2025, 6, 2),
	})
	seedConsolidationUnit(t, store, MemoryUnit{
		ID: "merged", BankID: "b1", Text: "Bob is on the beta waitlist", FactType: TypeObservation,
		OccurredStart: at(2024, 1, 15), OccurredEnd: at(9999, 12, 31), MentionedAt: at(2025, 1, 1),
	})
	seedConsolidationUnit(t, store, MemoryUnit{ID: "old-fact", BankID: "b1", Text: "the waitlist fact", FactType: TypeExperience})
	store.observationSources["merged"] = []string{"old-fact"}

	if err := store.MergeObservationEvidence(context.Background(), "b1", "survivor", "merged"); err != nil {
		t.Fatalf("fold: %v", err)
	}
	survivor := store.units["survivor"]
	if survivor.Text != "Bob uses open signup" {
		t.Fatalf("text = %q, want the survivor's own text kept", survivor.Text)
	}
	if got := store.observationSources["survivor"]; len(got) != 1 || got[0] != "old-fact" {
		t.Fatalf("sources = %v, want the merged evidence folded in", got)
	}
	// The merged observation's own bounds widened the survivor's: its older
	// start wins, and its year-9999 placeholder end reads as unknown rather
	// than pushing the survivor's end to infinity.
	if survivor.OccurredStart == nil || !survivor.OccurredStart.Equal(*at(2024, 1, 15)) {
		t.Fatalf("occurred_start = %v, want the merged observation's earlier start", survivor.OccurredStart)
	}
	if survivor.OccurredEnd != nil {
		t.Fatalf("occurred_end = %v, want the placeholder treated as unknown", survivor.OccurredEnd)
	}
	if survivor.MentionedAt == nil || !survivor.MentionedAt.Equal(*at(2025, 6, 2)) {
		t.Fatalf("mentioned_at = %v, want the later mention kept", survivor.MentionedAt)
	}
}

// A merge whose target the model could not name — or whose survivor is not an
// observation — requeues the merged evidence instead: the consolidated stamps
// of its live sources are cleared, so the next pass attaches them.
func TestMemoryMergeObservationEvidenceRequeuesWithoutSurvivor(t *testing.T) {
	tests := []struct {
		name      string
		survivor  string
		stampedID string
	}{
		{name: "no target named", survivor: "", stampedID: "old-fact"},
		{name: "survivor gone", survivor: "vanished", stampedID: "old-fact"},
		{name: "survivor is a fact, not an observation", survivor: "old-fact", stampedID: "old-fact"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore("public")
			seedConsolidationUnit(t, store, MemoryUnit{ID: "merged", BankID: "b1", Text: "a merged observation", FactType: TypeObservation})
			seedConsolidationUnit(t, store, MemoryUnit{ID: "old-fact", BankID: "b1", Text: "the waitlist fact", FactType: TypeExperience, ConsolidatedAt: at(2025, 1, 1)})
			store.consolidated["old-fact"] = struct{}{}
			store.observationSources["merged"] = []string{"old-fact"}

			if err := store.MergeObservationEvidence(context.Background(), "b1", test.survivor, "merged"); err != nil {
				t.Fatalf("fold: %v", err)
			}
			unit := store.units[test.stampedID]
			if unit.ConsolidatedAt != nil {
				t.Fatalf("consolidated_at = %v, want the stamp cleared for the requeue", unit.ConsolidatedAt)
			}
			if _, pending := store.consolidated[test.stampedID]; pending {
				t.Fatal("the fact must leave the consolidated set")
			}
		})
	}
}

// The successor operation of a round-capped chain carries caller_requested
// when a caller chain requeues, and deduplicates against a pending full-bank
// pass — never against the one now running.
func TestMemoryEnqueueConsolidationSuccessor(t *testing.T) {
	tests := []struct {
		name            string
		callerRequested bool
		existing        *Operation
		wantEnqueued    bool
		wantPayloadKey  bool
	}{
		{
			name:            "caller chain carries the flag",
			callerRequested: true,
			wantEnqueued:    true,
			wantPayloadKey:  true,
		},
		{
			name:           "automatic chain carries none",
			wantEnqueued:   true,
			wantPayloadKey: false,
		},
		{
			name:            "a pending full-bank pass absorbs the requeue",
			callerRequested: true,
			existing:        &Operation{OperationID: "pending-1", BankID: "b1", OperationType: OperationTypeConsolidate, Status: StatusPending},
			wantEnqueued:    false,
		},
		{
			name:            "the running pass does not absorb it",
			callerRequested: true,
			existing:        &Operation{OperationID: "running-1", BankID: "b1", OperationType: OperationTypeConsolidate, Status: StatusProcessing},
			wantEnqueued:    true,
			wantPayloadKey:  true,
		},
		{
			name:            "a pending scoped pass does not absorb it",
			callerRequested: true,
			existing: &Operation{OperationID: "scoped-1", BankID: "b1", OperationType: OperationTypeConsolidate,
				Status: StatusPending, Payload: map[string]any{"observation_scopes": [][]string{{"topic:*"}}}},
			wantEnqueued:   true,
			wantPayloadKey: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := NewMemoryStore("public")
			if test.existing != nil {
				now := time.Now().UTC()
				op := *test.existing
				op.CreatedAt = now
				op.UpdatedAt = now
				store.operations[op.OperationID] = op
			}
			if err := store.EnqueueConsolidationSuccessor(context.Background(), "b1", test.callerRequested); err != nil {
				t.Fatalf("enqueue successor: %v", err)
			}
			enqueued := 0
			var successor Operation
			for _, op := range store.operations {
				if op.OperationType == OperationTypeConsolidate && op.OperationID != "pending-1" &&
					op.OperationID != "running-1" && op.OperationID != "scoped-1" {
					enqueued++
					successor = op
				}
			}
			if test.wantEnqueued && enqueued != 1 {
				t.Fatalf("enqueued = %d, want the successor recorded", enqueued)
			}
			if !test.wantEnqueued && enqueued != 0 {
				t.Fatalf("enqueued = %d, want the requeue deduplicated", enqueued)
			}
			if test.wantEnqueued {
				if _, carried := successor.Payload["caller_requested"]; carried != test.wantPayloadKey {
					t.Fatalf("payload = %v, want caller_requested carried = %v", successor.Payload, test.wantPayloadKey)
				}
			}
		})
	}
}
