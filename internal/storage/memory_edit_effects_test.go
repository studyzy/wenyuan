package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// textEmbedder returns a vector derived from the input so an edit's re-embed is
// observable, and records every text it was handed so the augmentation can be
// checked.
type textEmbedder struct {
	sent []string
}

func (e *textEmbedder) EmbedPassages(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, text := range texts {
		e.sent = append(e.sent, text)
		sum := float32(0)
		for _, r := range text {
			sum += float32(r)
		}
		vec := make([]float32, EmbeddingDimensions)
		vec[0] = sum
		vec[1] = float32(len(text))
		out[i] = vec
	}
	return out, nil
}

func (e *textEmbedder) EmbedQuery(_ context.Context, _ string) ([]float32, error) {
	return make([]float32, EmbeddingDimensions), nil
}

// pendingOperationTypes lists the non-terminal operation types a bank has.
func pendingOperationTypes(t *testing.T, store *MemoryStore, bankID string) []string {
	t.Helper()
	store.mu.RLock()
	defer store.mu.RUnlock()
	var types []string
	for _, op := range store.operations {
		if op.BankID == bankID && !op.Status.Terminal() {
			types = append(types, op.OperationType)
		}
	}
	return types
}

func containsOpType(types []string, want string) bool {
	for _, t := range types {
		if t == want {
			return true
		}
	}
	return false
}

// A text edit does what the reference's curation does: it re-embeds the unit
// with retain's own date+entity augmentation, resets the consolidation markers
// so the fact is folded again, prunes the observations that rested on the old
// text, and submits the consolidation and graph-maintenance follow-ups.
func TestUpdateMemoryEditAppliesCurationEffects(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	embedder := &textEmbedder{}
	store.SetObservationEmbedder(embedder)
	store.SetAutoConsolidation(true)

	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items: []RetainItem{{
			Content:  "Alice works at Google.",
			Entities: []RetainEntity{{Text: "Google"}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryID := outcome.ItemUnitIDs[0][0]

	// Mark the fact already folded, so the reset the edit performs is visible.
	consolidated := time.Now().UTC()
	failed := consolidated
	unit, err := store.GetMemory(ctx, bank, memoryID)
	if err != nil {
		t.Fatal(err)
	}
	unit.ConsolidatedAt = &consolidated
	unit.ConsolidationFailedAt = &failed
	if _, err := store.UpsertUnit(ctx, unit); err != nil {
		t.Fatal(err)
	}

	// An observation resting on this fact alone: the edit makes it stale.
	observationID, err := store.CreateObservation(ctx, bank, "Alice is at Google.", []string{memoryID})
	if err != nil {
		t.Fatal(err)
	}

	newText := "Bob works at Globex."
	updated, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Text: &newText})
	if err != nil {
		t.Fatal(err)
	}

	if updated.ConsolidatedAt != nil || updated.ConsolidationFailedAt != nil {
		t.Fatalf("edit left consolidation markers set: consolidated=%v failed=%v",
			updated.ConsolidatedAt, updated.ConsolidationFailedAt)
	}
	if len(updated.Embedding) == 0 {
		t.Fatal("edit did not recompute the unit embedding")
	}
	// The re-embed ran over the new text augmented with the unit's entity names,
	// exactly as retain would have built it.
	reembedded := embedder.sent[len(embedder.sent)-1]
	if !strings.Contains(reembedded, newText) {
		t.Fatalf("re-embedded text %q does not carry the new text %q", reembedded, newText)
	}
	if !strings.Contains(reembedded, "Google") {
		t.Fatalf("re-embedded text %q does not carry the unit's entity names", reembedded)
	}

	if _, err := store.GetMemory(ctx, bank, observationID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("observation resting on the edited fact = %v, want ErrMemoryNotFound", err)
	}

	types := pendingOperationTypes(t, store, bank)
	if !containsOpType(types, OperationTypeGraphMaintenance) {
		t.Fatalf("edit submitted no graph maintenance: ops=%v", types)
	}
	if !containsOpType(types, OperationTypeConsolidate) {
		t.Fatalf("edit submitted no consolidation: ops=%v", types)
	}
}

// When the bank (or deployment) has automatic consolidation off, an edit still
// submits the graph-maintenance sweep but no consolidation pass.
func TestUpdateMemoryEditConsolidationGated(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	store.SetAutoConsolidation(false)

	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items:  []RetainItem{{Content: "Alice works at Google."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryID := outcome.ItemUnitIDs[0][0]
	newText := "Bob works at Globex."
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Text: &newText}); err != nil {
		t.Fatal(err)
	}
	types := pendingOperationTypes(t, store, bank)
	if containsOpType(types, OperationTypeConsolidate) {
		t.Fatalf("edit submitted consolidation with auto-consolidation off: ops=%v", types)
	}
	if !containsOpType(types, OperationTypeGraphMaintenance) {
		t.Fatalf("edit submitted no graph maintenance: ops=%v", types)
	}
}

// A bank's own enable_auto_consolidation override wins over the deployment
// default, the same resolution the reference's config does.
func TestUpdateMemoryEditBankConfigOverridesDefault(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	store.SetAutoConsolidation(false)
	enabled := true
	if _, err := store.UpdateBank(ctx, bank, BankPatch{Config: &map[string]any{"enable_auto_consolidation": enabled}}); err != nil {
		t.Fatal(err)
	}

	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items:  []RetainItem{{Content: "Alice works at Google."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryID := outcome.ItemUnitIDs[0][0]
	newText := "Bob works at Globex."
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{Text: &newText}); err != nil {
		t.Fatal(err)
	}
	types := pendingOperationTypes(t, store, bank)
	if !containsOpType(types, OperationTypeConsolidate) {
		t.Fatalf("bank override did not enable consolidation: ops=%v", types)
	}
}

// Invalidating a live fact prunes the observations resting on it and submits
// both follow-ups, while a repeat invalidation that only rewrites the reason
// leaves the background work alone.
func TestUpdateMemoryInvalidateAppliesCurationEffects(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	store.SetAutoConsolidation(true)

	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank,
		Items:  []RetainItem{{Content: "Alice works at Google."}},
	})
	if err != nil {
		t.Fatal(err)
	}
	memoryID := outcome.ItemUnitIDs[0][0]
	observationID, err := store.CreateObservation(ctx, bank, "Alice is at Google.", []string{memoryID})
	if err != nil {
		t.Fatal(err)
	}

	invalidated := "invalidated"
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{State: &invalidated}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMemory(ctx, bank, observationID); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("observation resting on the invalidated fact = %v, want ErrMemoryNotFound", err)
	}
	types := pendingOperationTypes(t, store, bank)
	if !containsOpType(types, OperationTypeGraphMaintenance) || !containsOpType(types, OperationTypeConsolidate) {
		t.Fatalf("invalidate follow-ups missing: ops=%v", types)
	}

	// Drain the queue, then repeat the invalidation with a new reason: the
	// reason-only path submits nothing.
	store.mu.Lock()
	for id, op := range store.operations {
		if op.BankID == bank {
			delete(store.operations, id)
		}
	}
	store.mu.Unlock()
	reason := "changed my mind"
	if _, err := store.UpdateMemory(ctx, bank, memoryID, MemoryPatch{State: &invalidated, Reason: &reason}); err != nil {
		t.Fatal(err)
	}
	if types := pendingOperationTypes(t, store, bank); len(types) != 0 {
		t.Fatalf("reason-only invalidation submitted follow-ups: ops=%v", types)
	}
}
