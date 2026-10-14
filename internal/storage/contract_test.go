package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// newContractStore builds a memory-backed store holding one bank. The in-memory
// backend is the test double every other package's tests run against, so its
// behaviour is what those tests actually assert; this suite pins it directly.
func newContractStore(t *testing.T) (*MemoryStore, string) {
	t.Helper()
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	return store, "b1"
}

// Attachments persist per document, are idempotent on the content hash, and are
// listed back in a deterministic order.
func TestContractAttachments(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	attachment := Attachment{
		BankID: bank, DocumentID: "doc-1", AttachmentHash: "aa11", ShortID: "aa11bb22cc33",
		MediaType: "image/png", ByteSize: 9, StorageKey: "banks/b1/documents/doc-1/attachments/sha256-aa11",
		Kind: "file", Filename: "diagram.png",
	}
	if err := store.PutAttachment(ctx, attachment); err != nil {
		t.Fatalf("put attachment: %v", err)
	}
	// A repeat write of identical content must not duplicate the row.
	if err := store.PutAttachment(ctx, attachment); err != nil {
		t.Fatalf("repeat put attachment: %v", err)
	}
	if err := store.PutAttachment(ctx, Attachment{
		BankID: bank, DocumentID: "doc-1", AttachmentHash: "bb22", ShortID: "bb22cc33dd44",
	}); err != nil {
		t.Fatalf("put second attachment: %v", err)
	}

	got, err := store.ListAttachments(ctx, bank, "doc-1")
	if err != nil {
		t.Fatalf("list attachments: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("attachments = %d, want 2", len(got))
	}
	if got[0].AttachmentHash != "aa11" {
		t.Fatalf("attachments are not ordered by hash: %+v", got)
	}
	// Another document sees nothing.
	if other, err := store.ListAttachments(ctx, bank, "doc-2"); err != nil || len(other) != 0 {
		t.Fatalf("other document attachments = %v (err %v)", other, err)
	}
}

// The consolidation round trip folds facts into an observation, links the
// sources, and marks the facts consumed.
func TestContractConsolidation(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	for _, id := range []string{"f1", "f2"} {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{ID: id, BankID: bank, Text: "fact " + id, FactType: TypeWorld}); err != nil {
			t.Fatal(err)
		}
	}
	facts, err := store.ConsolidationFacts(ctx, bank, 10)
	if err != nil {
		t.Fatalf("facts: %v", err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want 2", len(facts))
	}

	observationID, err := store.CreateObservation(ctx, bank, "a conclusion", []string{"f1", "f2"})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}
	observations, err := store.ConsolidationObservations(ctx, bank, 10)
	if err != nil {
		t.Fatalf("observations: %v", err)
	}
	if len(observations) != 1 || observations[0].ProofCount != 2 {
		t.Fatalf("observations = %+v", observations)
	}

	if err := store.UpdateObservation(ctx, bank, observationID, "a revised conclusion", []string{"f1"}); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if err := store.UpdateObservation(ctx, bank, "missing", "x", nil); !errors.Is(err, ErrMemoryNotFound) {
		t.Fatalf("updating an unknown observation = %v, want ErrMemoryNotFound", err)
	}

	if err := store.MarkConsolidated(ctx, bank, []string{"f1", "f2"}); err != nil {
		t.Fatalf("mark consolidated: %v", err)
	}
	if facts, err = store.ConsolidationFacts(ctx, bank, 10); err != nil || len(facts) != 0 {
		t.Fatalf("facts after marking = %v (err %v), want none", facts, err)
	}

	if err := store.DeleteObservation(ctx, bank, observationID); err != nil {
		t.Fatalf("delete observation: %v", err)
	}
	if observations, err = store.ConsolidationObservations(ctx, bank, 10); err != nil || len(observations) != 0 {
		t.Fatalf("observations after delete = %v (err %v)", observations, err)
	}
	// Deleting an observation that is not there is a no-op, not an error.
	if err := store.DeleteObservation(ctx, bank, "missing"); err != nil {
		t.Fatalf("deleting an unknown observation: %v", err)
	}
}

// The operation queue claims one operation at a time, releases a claim, and
// schedules a retry.
func TestContractOperationQueue(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	op, err := store.PutOperation(ctx, Operation{
		OperationID: "op-1", BankID: bank, OperationType: OperationTypeConsolidate, Status: StatusPending,
	})
	if err != nil {
		t.Fatalf("put operation: %v", err)
	}

	claimed, err := store.ClaimNextOperation(ctx, "worker-a", time.Minute)
	if err != nil {
		t.Fatalf("claim: %v", err)
	}
	if claimed.OperationID != op.OperationID || claimed.Status != StatusProcessing {
		t.Fatalf("claimed = %+v", claimed)
	}
	// A second worker finds nothing claimable while the first holds the lease.
	if _, err := store.ClaimNextOperation(ctx, "worker-b", time.Minute); !errors.Is(err, ErrOperationNotFound) {
		t.Fatalf("second claim = %v, want ErrOperationNotFound", err)
	}

	if err := store.ReleaseOperationClaim(ctx, op.OperationID, "worker-a"); err != nil {
		t.Fatalf("release: %v", err)
	}
	if _, err := store.ClaimNextOperation(ctx, "worker-b", time.Minute); err != nil {
		t.Fatalf("reclaim after release: %v", err)
	}

	if err := store.ScheduleOperationRetry(ctx, op.OperationID, 1, time.Now().Add(time.Hour), "boom"); err != nil {
		t.Fatalf("schedule retry: %v", err)
	}
}

// The directive lifecycle works through the store, including its not-found
// behaviour.
func TestContractDirectives(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	if _, err := store.GetDirective(ctx, bank, "missing"); !errors.Is(err, ErrDirectiveNotFound) {
		t.Fatalf("unknown directive = %v, want ErrDirectiveNotFound", err)
	}
	created, err := store.PutDirective(ctx, Directive{ID: "d1", BankID: bank, Name: "tone", Content: "terse", Priority: 1, IsActive: true})
	if err != nil {
		t.Fatalf("put directive: %v", err)
	}
	if created.ID != "d1" {
		t.Fatalf("created = %+v", created)
	}
	priority := 9
	content := "very terse"
	updated, err := store.UpdateDirective(ctx, bank, "d1", DirectivePatch{Priority: &priority, Content: &content})
	if err != nil {
		t.Fatalf("update directive: %v", err)
	}
	if updated.Priority != 9 || updated.Content != "very terse" {
		t.Fatalf("updated = %+v", updated)
	}
	listed, err := store.ListDirectives(ctx, bank)
	if err != nil || len(listed) != 1 {
		t.Fatalf("list directives = %v (err %v)", listed, err)
	}
	if err := store.DeleteDirective(ctx, bank, "d1"); err != nil {
		t.Fatalf("delete directive: %v", err)
	}
	if _, err := store.GetDirective(ctx, bank, "d1"); !errors.Is(err, ErrDirectiveNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

// The alias routes add, promote and remove, reporting their conflicts.
func TestContractAliases(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	if _, err := store.AddAlias(ctx, bank, "work", false); err != nil {
		t.Fatalf("add alias: %v", err)
	}
	resolved, err := store.ResolveBankID(ctx, "work")
	if err != nil || resolved != bank {
		t.Fatalf("resolve alias = %q (err %v)", resolved, err)
	}
	aliases, err := store.ListAliases(ctx, bank)
	if err != nil || len(aliases) == 0 {
		t.Fatalf("list aliases = %v (err %v)", aliases, err)
	}
	if _, err := store.CreateBank(ctx, Bank{BankID: "other", Name: "Other"}); err != nil {
		t.Fatalf("create second bank: %v", err)
	}
	if _, err := store.AddAlias(ctx, "other", "work", false); !errors.Is(err, ErrAliasConflict) {
		t.Fatalf("conflicting alias = %v, want ErrAliasConflict", err)
	}
	if _, err := store.PromoteAlias(ctx, bank, "work"); err != nil {
		t.Fatalf("promote alias: %v", err)
	}
	if primary := store.PrimaryAlias(ctx, bank); primary != "work" {
		t.Fatalf("primary alias = %q", primary)
	}
	if _, err := store.RemoveAlias(ctx, bank, "work"); err != nil {
		t.Fatalf("remove alias: %v", err)
	}
	if _, err := store.RemoveAlias(ctx, bank, "work"); !errors.Is(err, ErrAliasNotFound) {
		t.Fatalf("removing an unknown alias = %v, want ErrAliasNotFound", err)
	}
}

// Documents are written, read back, appended to and deleted whole.
func TestContractDocuments(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()
	if _, err := store.PutDocument(ctx, bank, "doc-1", Document{
		ID: "doc-1", BankID: bank, OriginalText: "hello", Tags: []string{"t"},
	}); err != nil {
		t.Fatalf("put document: %v", err)
	}
	got, err := store.GetDocument(ctx, bank, "doc-1")
	if err != nil || got.OriginalText != "hello" {
		t.Fatalf("get document = %+v (err %v)", got, err)
	}
	if _, err := store.GetDocument(ctx, bank, "missing"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("unknown document = %v, want ErrDocumentNotFound", err)
	}
	page, err := store.ListDocuments(ctx, DocumentQuery{BankID: bank, Limit: 10})
	if err != nil || page.Total != 1 {
		t.Fatalf("list documents = %+v (err %v)", page, err)
	}
	if err := store.DeleteDocument(ctx, bank, "doc-1"); err != nil {
		t.Fatalf("delete document: %v", err)
	}
	if _, err := store.GetDocument(ctx, bank, "doc-1"); !errors.Is(err, ErrDocumentNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

// The bank lifecycle replaces, updates and deletes, reporting its conflicts.
func TestContractBankLifecycle(t *testing.T) {
	store, bank := newContractStore(t)
	ctx := context.Background()

	replaced, err := store.ReplaceBank(ctx, Bank{BankID: bank, Name: "Renamed"})
	if err != nil || replaced.Name != "Renamed" {
		t.Fatalf("replace bank = %+v (err %v)", replaced, err)
	}
	mission := "be helpful"
	updated, err := store.UpdateBank(ctx, bank, BankPatch{Mission: &mission})
	if err != nil || updated.Mission != mission {
		t.Fatalf("update bank = %+v (err %v)", updated, err)
	}
	page, err := store.ListBanks(ctx, BankQuery{Limit: 10})
	if err != nil || page.Total != 1 {
		t.Fatalf("list banks = %+v (err %v)", page, err)
	}
	// Creating a bank whose identifier is already taken is a conflict.
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "dup"}); !errors.Is(err, ErrOperationConflict) {
		t.Fatalf("duplicate bank = %v, want ErrOperationConflict", err)
	}
	if _, err := store.DeleteBank(ctx, bank); err != nil {
		t.Fatalf("delete bank: %v", err)
	}
	if _, err := store.GetBank(ctx, bank); !errors.Is(err, ErrBankNotFound) {
		t.Fatalf("get after delete = %v", err)
	}
}

// A store configured to fail writes reports the transient error on every write
// path, which is what the retryable 503 mapping keys on.
func TestContractWriteErrorPropagation(t *testing.T) {
	store, bank := newContractStore(t)
	store.SetWriteError(ErrWritesUnavailable)
	ctx := context.Background()
	if _, err := store.PutDirective(ctx, Directive{ID: "d1", BankID: bank, Name: "n"}); !errors.Is(err, ErrWritesUnavailable) {
		t.Fatalf("put directive = %v, want ErrWritesUnavailable", err)
	}
	if err := store.PutAttachment(ctx, Attachment{BankID: bank, DocumentID: "d", AttachmentHash: "h"}); !errors.Is(err, ErrWritesUnavailable) {
		t.Fatalf("put attachment = %v, want ErrWritesUnavailable", err)
	}
}

// A store configured to fail reads reports the read error, and a store that is
// not ready reports its readiness error.
func TestContractReadErrorAndReadiness(t *testing.T) {
	store, bank := newContractStore(t)
	store.SetReadError(ErrReadTimeout)
	if _, err := store.ListMemories(context.Background(), MemoryQuery{BankID: bank, Limit: 10}); !errors.Is(err, ErrReadTimeout) {
		t.Fatalf("list memories = %v, want ErrReadTimeout", err)
	}

	notReady := NewMemoryStore("public")
	notReady.SetReadyError(ErrWritesUnavailable)
	if err := notReady.Ready(context.Background()); !errors.Is(err, ErrWritesUnavailable) {
		t.Fatalf("ready = %v, want ErrWritesUnavailable", err)
	}
}
