package consolidation

import (
	"context"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// mergeCall records one MergeObservationEvidence call the pass made.
type mergeCall struct {
	survivorID string
	mergedID   string
}

// capableStore is a stubStore that also carries the merge and chain
// capabilities, so a test can exercise the pass's optional paths.
type capableStore struct {
	*stubStore
	merges     []mergeCall
	successors []bool
}

func (s *capableStore) MergeObservationEvidence(_ context.Context, _, survivorID, mergedID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.merges = append(s.merges, mergeCall{survivorID: survivorID, mergedID: mergedID})
	return nil
}

func (s *capableStore) EnqueueConsolidationSuccessor(_ context.Context, _ string, callerRequested bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successors = append(s.successors, callerRequested)
	return nil
}

// scriptedProvider answers each call with the next response, holding the last
// one once the script runs out.
type scriptedProvider struct {
	stubProvider
	responses []provider.CompletionResult
	calls     int
}

func (p *scriptedProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	p.calls++
	i := p.calls - 1
	if i >= len(p.responses) {
		i = len(p.responses) - 1
	}
	p.response = p.responses[i]
	return p.stubProvider.Complete(ctx, req)
}

// A delete that marks itself a merge moves its evidence onto the survivor it
// names: the pass folds before deleting, so the old sources land on the
// observation that now carries the content.
func TestConsolidatorMergeDeleteFoldsIntoSurvivor(t *testing.T) {
	store := &capableStore{stubStore: &stubStore{
		facts: []Fact{{ID: "f1", Text: "Bob confirmed the waitlist shut"}},
		observations: []Observation{
			{ID: "o1", Text: "Bob is on the beta waitlist.", ProofCount: 1},
			{ID: "o2", Text: "Bob is waiting on the beta waitlist for an invite.", ProofCount: 1},
		},
	}}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"updates": []any{map[string]any{
				"observation_id": "o1", "text": "Bob uses open signup; the waitlist he was on shut.",
				"source_fact_ids": []any{"f1"}, "reason": "state change",
			}},
			"deletes": []any{map[string]any{
				"observation_id": "o2", "merged_into": "o1", "reason": "restates o1",
			}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Deleted != 1 || len(store.deleted) != 1 || store.deleted[0] != "o2" {
		t.Fatalf("deleted = %v / %+v, want o2 deleted", store.deleted, result)
	}
	if len(store.merges) != 1 || store.merges[0] != (mergeCall{survivorID: "o1", mergedID: "o2"}) {
		t.Fatalf("merges = %+v, want o2 folded into o1", store.merges)
	}
}

// A merge whose target the model could not name — an observation it was never
// shown, or one it deletes outright — still counts as a merge: the evidence is
// requeued with no survivor, not silently retired with the row.
func TestConsolidatorMergeDeleteWithoutUsableTargetRequeues(t *testing.T) {
	store := &capableStore{stubStore: &stubStore{
		facts: []Fact{{ID: "f1", Text: "a fact"}},
		observations: []Observation{
			{ID: "o1", Text: "an existing conclusion", ProofCount: 1},
			{ID: "o2", Text: "a twin worth folding", ProofCount: 1},
		},
	}}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"deletes": []any{
				// Names an observation the model was never shown.
				map[string]any{"observation_id": "o2", "merged_into": "o9", "reason": "merge"},
				// Names its own deletion target.
				map[string]any{"observation_id": "o1", "merged_into": "o1", "reason": "self merge"},
			},
		}},
	}
	if _, err := New(model, store).Run(context.Background(), "b1"); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.merges) != 2 {
		t.Fatalf("merges = %+v, want both merges attempted", store.merges)
	}
	for _, call := range store.merges {
		if call.survivorID != "" {
			t.Fatalf("merge target %q must not be usable", call.survivorID)
		}
	}
}

// A delete whose observation the same response also updates is dropped: the
// update carries the newer knowledge, so it wins and the row survives.
func TestConsolidatorUpdateWinsOverDelete(t *testing.T) {
	store := &capableStore{stubStore: &stubStore{
		facts:        []Fact{{ID: "f1", Text: "Alice sold the car"}},
		observations: []Observation{{ID: "o1", Text: "Alice owns a car", ProofCount: 1}},
	}}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"updates": []any{map[string]any{"observation_id": "o1", "text": "Alice sold her car", "source_fact_ids": []any{"f1"}}},
			"deletes": []any{map[string]any{"observation_id": "o1", "reason": "superseded"}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Deleted != 0 || len(store.deleted) != 0 {
		t.Fatalf("deleted = %+v, want the delete dropped", store.deleted)
	}
	if result.Updated != 1 || len(store.updated) != 1 {
		t.Fatalf("updated = %+v, want the update applied", store.updated)
	}
	if len(store.merges) != 0 {
		t.Fatalf("merges = %+v, want none", store.merges)
	}
}

// An update that names a batch FACT id as its target is a create: there is no
// observation to update, and dropping it loses the fact.
func TestConsolidatorReaimsMisaimedUpdateAsCreate(t *testing.T) {
	store := &stubStore{facts: []Fact{{ID: "f1", Text: "Alice moved to Berlin"}}}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"updates": []any{map[string]any{
				"observation_id": "f1", "text": "Alice moved to Berlin", "source_fact_ids": []any{"f1"},
			}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Updated != 0 || len(store.updated) != 0 {
		t.Fatalf("updated = %+v, want no update applied", store.updated)
	}
	if result.Created != 1 || len(store.created) != 1 || len(store.created[0].SourceIDs) != 1 {
		t.Fatalf("created = %+v, want the mis-aimed update recorded as a create", store.created)
	}
}

// A miscopied observation id — the head of a UUID copied, the tail invented —
// is repaired against the pool when exactly one candidate shares the head.
func TestConsolidatorRepairsMiscopiedTarget(t *testing.T) {
	poolID := "d0a3effa-1795-4c48-9f2e-1a2b3c4d5e6f"
	store := &stubStore{
		facts:        []Fact{{ID: "f1", Text: "a fact"}},
		observations: []Observation{{ID: poolID, Text: "the real observation", ProofCount: 1}},
	}
	model := &stubProvider{
		name: "openai",
		response: provider.CompletionResult{Structured: map[string]any{
			"updates": []any{map[string]any{
				"observation_id": "d0a3effa-1795-4ef0-0000-000000000000",
				"text":           "the real observation, extended", "source_fact_ids": []any{"f1"},
			}},
		}},
	}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Updated != 1 || len(store.updated) != 1 || store.updated[0].ObservationID != poolID {
		t.Fatalf("updated = %+v, want the target repaired to %s", store.updated, poolID)
	}
}

// An update or delete naming an observation the model was never shown is a
// warning and a skipped action: the batch continues, and the pass does not fail
// and requeue forever over a hallucinated target.
func TestConsolidatorSkipsUnknownTargets(t *testing.T) {
	tests := []struct {
		name     string
		response provider.CompletionResult
	}{
		{
			name: "update",
			response: provider.CompletionResult{Structured: map[string]any{
				"updates": []any{map[string]any{"observation_id": "o9", "text": "new text", "source_fact_ids": []any{"f1"}}},
			}},
		},
		{
			name: "delete",
			response: provider.CompletionResult{Structured: map[string]any{
				"deletes": []any{map[string]any{"observation_id": "o9", "reason": "gone"}},
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &stubStore{
				facts:        []Fact{{ID: "f1", Text: "a fact"}},
				observations: []Observation{{ID: "o1", Text: "an existing conclusion", ProofCount: 1}},
			}
			model := &stubProvider{name: "openai", response: test.response}
			result, err := New(model, store).Run(context.Background(), "b1")
			if err != nil {
				t.Fatalf("run: %v", err)
			}
			if len(store.updated) != 0 || len(store.deleted) != 0 {
				t.Fatalf("unknown target must not be written: updated=%v deleted=%v", store.updated, store.deleted)
			}
			if result.Skipped != 1 {
				t.Fatalf("skipped = %d, want the rejected action counted", result.Skipped)
			}
			// The batch was answered either way, so its fact is stamped.
			if len(store.marked) != 1 {
				t.Fatalf("marked = %v, want the batch stamped", store.marked)
			}
		})
	}
}

// A response carrying no usable JSON fails its batch rather than stamping it:
// facts the pass never actually read must stay pending, not be marked folded.
func TestConsolidatorFailsUnusableResponse(t *testing.T) {
	store := &stubStore{facts: []Fact{{ID: "f1", Text: "a fact"}}}
	model := &stubProvider{name: "openai", response: provider.CompletionResult{Content: "not json at all"}}
	result, err := New(model, store).Run(context.Background(), "b1")
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if result.Failed != 1 {
		t.Fatalf("failed = %d, want the batch failed", result.Failed)
	}
	if len(store.marked) != 0 || len(store.failed) != 1 {
		t.Fatalf("marked = %v failed = %v, want the fact marked failed and never stamped", store.marked, store.failed)
	}
}

// resolvePoolID returns an exact match untouched, repairs a miscopied head to
// its one candidate, and leaves anything ambiguous or unmatchable alone.
func TestResolvePoolID(t *testing.T) {
	pool := map[string]bool{
		"d0a3effa-1795-4c48-9f2e-1a2b3c4d5e6f": true,
		"11111111-1111-1111-1111-111111111111": true,
	}
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{name: "exact", raw: "d0a3effa-1795-4c48-9f2e-1a2b3c4d5e6f", want: "d0a3effa-1795-4c48-9f2e-1a2b3c4d5e6f"},
		{name: "miscopied tail", raw: "d0a3effa-1795-4ef0-0000-000000000000", want: "d0a3effa-1795-4c48-9f2e-1a2b3c4d5e6f"},
		{name: "unknown", raw: "99999999-9999-4999-9999-999999999999", want: "99999999-9999-4999-9999-999999999999"},
		{name: "too short to trust", raw: "d0a3effa", want: "d0a3effa"},
		{name: "empty", raw: "", want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := resolvePoolID(test.raw, pool); got != test.want {
				t.Fatalf("resolvePoolID(%q) = %q, want %q", test.raw, got, test.want)
			}
		})
	}
}

// prepareDeletes drops what the pass must not apply: a delete the same
// response updates, and one naming an observation the model was never shown.
// A merge target must be shown too, and never the deleted observation itself.
func TestPrepareDeletes(t *testing.T) {
	poolIDs := map[string]bool{"o1": true, "o2": true}
	updatedIDs := map[string]bool{"o1": true}
	tests := []struct {
		name string
		in   []Delete
		want []preparedDelete
	}{
		{
			name: "update wins",
			in:   []Delete{{ObservationID: "o1"}},
			want: nil,
		},
		{
			name: "unknown target rejected",
			in:   []Delete{{ObservationID: "o9"}},
			want: nil,
		},
		{
			name: "merge into shown survivor",
			in:   []Delete{{ObservationID: "o2", MergedInto: "o1"}},
			want: []preparedDelete{{observationID: "o2", mergedInto: "o1", isMerge: true}},
		},
		{
			name: "merge into itself is no merge",
			in:   []Delete{{ObservationID: "o2", MergedInto: "o2"}},
			want: []preparedDelete{{observationID: "o2", isMerge: true}},
		},
		{
			name: "merge into unshown target still merges",
			in:   []Delete{{ObservationID: "o2", MergedInto: "o9"}},
			want: []preparedDelete{{observationID: "o2", isMerge: true}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := prepareDeletes(test.in, updatedIDs, poolIDs)
			if len(got) != len(test.want) {
				t.Fatalf("prepared = %+v, want %+v", got, test.want)
			}
			for i := range got {
				if got[i] != test.want[i] {
					t.Fatalf("prepared[%d] = %+v, want %+v", i, got[i], test.want[i])
				}
			}
		})
	}
}

// The delete-merge fields ride the response schema and the parsed structs, so a
// model answering the v1.1.0 contract is not silently dropped by the decoder.
func TestDecisionParsesMergeFields(t *testing.T) {
	decision, err := parseDecision(nil, `{"creates": [],
		"updates": [{"observation_id": "o1", "current_text": "old text", "text": "new text"}],
		"deletes": [{"observation_id": "o2", "merged_into": "o1", "reason": "merge"}]}`)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(decision.Updates) != 1 || decision.Updates[0].CurrentText != "old text" {
		t.Fatalf("updates = %+v, want current_text parsed", decision.Updates)
	}
	if len(decision.Deletes) != 1 || decision.Deletes[0].MergedInto != "o1" {
		t.Fatalf("deletes = %+v, want merged_into parsed", decision.Deletes)
	}
	schema := decisionSchema()
	deletedProps := schema.Properties["deletes"].Items.Properties
	if _, ok := deletedProps["merged_into"]; !ok {
		t.Fatalf("delete schema = %+v, want merged_into advertised", deletedProps)
	}
	updatedProps := schema.Properties["updates"].Items.Properties
	if _, ok := updatedProps["current_text"]; !ok {
		t.Fatalf("update schema = %+v, want current_text advertised", updatedProps)
	}
	if strings.Join(schema.Properties["deletes"].Items.Required, ",") != "observation_id" {
		t.Fatalf("delete required = %v, want observation_id alone", schema.Properties["deletes"].Items.Required)
	}
}
