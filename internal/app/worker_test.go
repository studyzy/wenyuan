package app

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/consolidation"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
	"github.com/studyzy/wenyuan/internal/worker"
)

// recordImportOperation stashes an archive and queues an import, returning the
// queued operation.
func recordImportOperation(t *testing.T, store *storage.MemoryStore, archive []byte) storage.Operation {
	t.Helper()
	ctx := context.Background()
	key := storage.StoredFileKeyForBank("b1", "import.zip")
	if err := store.PutStoredFile(ctx, storage.StoredFile{Key: key, ContentType: "application/zip", Data: archive}); err != nil {
		t.Fatal(err)
	}
	op, err := store.PutOperation(ctx, storage.Operation{
		OperationID: "op-import", BankID: "b1", OperationType: storage.OperationTypeTransferImport,
		Status: storage.StatusPending,
		Payload: map[string]any{
			storage.OperationPayloadKeyStoredFile: key,
			// A whole-bank restore writes into its target bank, which the
			// reference's task payload always names, and carries the requested
			// scope booleans (data + bank config on, history off).
			"target_bank_id":      "restored",
			"include_data":        true,
			"include_bank_config": true,
			"include_history":     false,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return op
}

// buildImportArchive builds a minimal zip archive in the reference's export
// format on disk and returns its bytes, which is what an import operation
// consumes.
func buildImportArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	write := func(name, body string) {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	write(transferarchive.ManifestName,
		`{"schema_version":1,"source_bank_id":"b1","document_count":1,"fact_count":1,"archive_type":"bank"}`)
	write(transferarchive.DocumentsDir+"/000000.json",
		`{"id":"doc-a","original_text":"alpha",
		  "chunks":[{"chunk_index":0,"chunk_text":"alpha"}],
		  "facts":[{"text":"Alice works at Google","fact_type":"world","entities":["Alice"]}]}`)
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The queue processor restores a stashed whole-bank archive into the target
// bank the task payload names.
func TestArchiveProcessorImportsStashedArchive(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	op := recordImportOperation(t, store, buildImportArchive(t))

	processor := archiveProcessor{store: store}
	if err := processor.Process(ctx, op); err != nil {
		t.Fatalf("process import: %v", err)
	}
	// The restore wrote the archive's document into the target bank.
	page, err := store.ListDocuments(ctx, storage.DocumentQuery{BankID: "restored", Limit: 10})
	if err != nil || page.Total == 0 {
		t.Fatalf("the import wrote no documents: %+v (err %v)", page, err)
	}
}

// An import operation with no stashed key is a permanent failure, because the
// same input would be rejected again.
func TestArchiveProcessorImportWithoutKey(t *testing.T) {
	store := storage.NewMemoryStore("public")
	err := archiveProcessor{store: store}.Process(context.Background(), storage.Operation{
		OperationID: "op-1", BankID: "b1", OperationType: storage.OperationTypeTransferImport,
	})
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("error = %v, want a permanent failure", err)
	}
}

// A clone copies the bank's profile under the target identifier.
func TestArchiveProcessorClonesBank(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{
		BankID: "b1", Name: "Source", Mission: "be helpful", Disposition: map[string]int{storage.DispositionKeyEmpathy: 7},
	}); err != nil {
		t.Fatal(err)
	}
	err := archiveProcessor{store: store}.Process(ctx, storage.Operation{
		OperationID: "op-clone", BankID: "b1", OperationType: storage.OperationTypeClone,
		Payload: map[string]any{"target_bank_id": "b2"},
	})
	if err != nil {
		t.Fatalf("process clone: %v", err)
	}
	clone, err := store.GetBank(ctx, "b2")
	if err != nil {
		t.Fatalf("the clone was not created: %v", err)
	}
	if clone.Mission != "be helpful" || clone.Trait(storage.DispositionKeyEmpathy) != 7 {
		t.Fatalf("clone = %+v, want the source's profile", clone)
	}
}

// A clone onto an existing bank is a permanent failure rather than an
// overwrite, because retrying cannot free the name.
func TestArchiveProcessorCloneOntoExistingBank(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	for _, id := range []string{"b1", "b2"} {
		if _, err := store.CreateBank(ctx, storage.Bank{BankID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	err := archiveProcessor{store: store}.Process(ctx, storage.Operation{
		OperationID: "op-clone", BankID: "b1", OperationType: storage.OperationTypeClone,
		Payload: map[string]any{"target_bank_id": "b2"},
	})
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("error = %v, want a permanent failure", err)
	}
}

// An export operation is a no-op: the bytes are produced by the read route, so
// the queue only records that the archive is available.
func TestArchiveProcessorExportIsNoop(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if err := (archiveProcessor{store: store}).Process(context.Background(), storage.Operation{
		OperationID: "op-export", BankID: "b1", OperationType: storage.OperationTypeTransferExport,
	}); err != nil {
		t.Fatalf("export operation: %v", err)
	}
}

// An unknown operation type is a permanent failure.
func TestArchiveProcessorUnknownType(t *testing.T) {
	store := storage.NewMemoryStore("public")
	err := archiveProcessor{store: store}.Process(context.Background(), storage.Operation{
		OperationID: "op-x", BankID: "b1", OperationType: "something-else",
	})
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("error = %v, want a permanent failure", err)
	}
}

// A consolidation operation with no model configured is a permanent refusal,
// and with a model it folds the bank's facts into an observation.
func TestArchiveProcessorConsolidation(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.RetainWrite(ctx, storage.RetainRequest{
		BankID: "b1", Items: []storage.RetainItem{{Content: "Alice works at Google.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	op := storage.Operation{OperationID: "op-c", BankID: "b1", OperationType: storage.OperationTypeConsolidate}

	// The no-model selection refuses permanently.
	err := archiveProcessor{store: store, provider: none.New()}.Process(ctx, op)
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("no-model consolidation = %v, want a permanent refusal", err)
	}

	// With a scripted model the pass applies a create.
	model := &scriptedProvider{}
	if err := (archiveProcessor{store: store, provider: model}).Process(ctx, op); err != nil {
		t.Fatalf("consolidation with a model: %v", err)
	}
	observations, err := store.ConsolidationObservations(ctx, "b1", 10)
	if err != nil || len(observations) != 1 {
		t.Fatalf("observations = %v (err %v), want one", observations, err)
	}
}

// scriptedProvider answers one completion with a fixed decision body.
type scriptedProvider struct{}

func (s *scriptedProvider) Name() string { return "openai" }
func (s *scriptedProvider) Complete(_ context.Context, _ provider.CompletionRequest) (provider.CompletionResult, error) {
	return provider.CompletionResult{Structured: map[string]any{
		"creates": []any{map[string]any{"text": "Alice works at Google", "source_fact_ids": []any{}}},
		"updates": []any{},
		"deletes": []any{},
	}}, nil
}
func (s *scriptedProvider) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
func (s *scriptedProvider) VerifyConnection(context.Context) error { return nil }
func (s *scriptedProvider) Dimensions() int                        { return provider.EmbeddingDimensions }

// The consolidation-store adapter maps between the engine's value types and the
// storage records.
func TestConsolidationStoreAdapter(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpsertUnit(ctx, storage.MemoryUnit{ID: "f1", BankID: "b1", Text: "a fact", FactType: storage.TypeWorld}); err != nil {
		t.Fatal(err)
	}
	adapter := consolidationStore{store: store}

	facts, err := adapter.ConsolidationFacts(ctx, "b1", 10)
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts = %v (err %v)", facts, err)
	}
	if facts[0].ID != "f1" {
		t.Fatalf("fact = %+v", facts[0])
	}
	observationID, err := adapter.CreateObservation(ctx, "b1", "a conclusion", []string{"f1"}, []string{"scope-a"})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}
	if err := adapter.UpdateObservation(ctx, "b1", observationID, "revised", nil); err != nil {
		t.Fatalf("update observation: %v", err)
	}
	if err := adapter.MarkConsolidated(ctx, "b1", []string{"f1"}); err != nil {
		t.Fatalf("mark consolidated: %v", err)
	}
	observations, err := adapter.ConsolidationObservations(ctx, "b1", []string{"scope-a"}, 10)
	if err != nil || len(observations) != 1 {
		t.Fatalf("observations = %v (err %v)", observations, err)
	}
	if len(observations[0].Scope) != 1 || observations[0].Scope[0] != "scope-a" {
		t.Fatalf("observation scope = %v, want [scope-a]", observations[0].Scope)
	}
	if err := adapter.DeleteObservation(ctx, "b1", observationID); err != nil {
		t.Fatalf("delete observation: %v", err)
	}
}

// formatOptionalTime renders an optional timestamp and leaves an unknown one
// empty rather than substituting a misleading value.
func TestFormatOptionalTime(t *testing.T) {
	if got := formatOptionalTime(nil); got != "" {
		t.Fatalf("nil time = %q, want empty", got)
	}
	at := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	if got := formatOptionalTime(&at); got != "2026-02-03T04:05:06Z" {
		t.Fatalf("time = %q", got)
	}
}

// The queue consumer is constructed with the resolved worker configuration.
func TestNewQueueConsumer(t *testing.T) {
	store := storage.NewMemoryStore("public")
	consumer := NewQueueConsumer(store, none.New(), nil, nil, config.WorkerConfig{ID: "w1", Concurrency: 2}, config.WorkerTuning{}, nil, ExtractionSettings{}, false, false, false, "public", nil)
	if consumer == nil {
		t.Fatal("the consumer must be constructed")
	}
}

// Ensure the consolidation engine's own contract is what the adapter satisfies.
var _ consolidation.Store = consolidationStore{}

// A subscribed webhook receives a queued delivery when an event fires.
func TestEnqueueWebhookEvent(t *testing.T) {
	store := storage.NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, storage.Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w1", BankID: "b1", URL: "https://example.test/hook",
		Secret: "s", EventTypes: []string{"consolidation.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutWebhook(ctx, storage.Webhook{
		ID: "w2", BankID: "b1", URL: "https://example.test/other",
		EventTypes: []string{"retain.completed"}, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	processor := archiveProcessor{store: store}
	processor.enqueueWebhookEvent(ctx, "b1", "consolidation.completed", map[string]any{})
	page, err := store.ListOperations(ctx, storage.OperationQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	deliveries := 0
	for _, op := range page.Operations {
		if op.OperationType == storage.OperationTypeWebhookDelivery {
			deliveries++
			if op.Payload["webhook_id"] != "w1" {
				t.Fatalf("delivery for the wrong webhook: %v", op.Payload)
			}
		}
	}
	if deliveries != 1 {
		t.Fatalf("queued %d deliveries, want only the subscribed webhook", deliveries)
	}
}

// A provider that rejects the configured credential (401/403) fails the
// operation once instead of walking the retry ladder: every replay would send
// the same key and earn the same rejection, so the budget can buy nothing
// (#5644). Asserted on the classification the consumer acts on, because that is
// what decides one attempt versus a full schedule.
func TestRetainAuthRejectionFailsOnce(t *testing.T) {
	cause := &provider.AuthError{
		Provider: "openai", ConfigKey: "WENYUAN_API_LLM_API_KEY", Reason: "Incorrect API key provided",
	}
	// The extractor wraps the provider's error on its way out, so the class has
	// to survive the wrap exactly as it does in the pipeline.
	wrapped := fmt.Errorf("fact extraction call failed: %w", cause)
	err := classifyRetainFailure(wrapped)
	if err == nil || !worker.IsPermanent(err) {
		t.Fatalf("classifyRetainFailure(%v) = %v, want a permanent failure", cause, err)
	}
	// The cause stays reachable, so the operation's error message names the
	// rejected key rather than only the wrap.
	if !provider.IsAuthRejected(err) {
		t.Fatalf("the classification must preserve the auth-rejected cause: %v", err)
	}
}

// The ordinary failures keep their existing classification: a transient model
// fault is still retried, so this change does not turn every provider error
// permanent.
func TestRetainTransientFailureStillRetries(t *testing.T) {
	cause := &provider.TransientError{Cause: errors.New("connection reset by peer")}
	err := classifyRetainFailure(fmt.Errorf("fact extraction call failed: %w", cause))
	if err == nil {
		t.Fatal("a failed retain must return its error")
	}
	if worker.IsPermanent(err) {
		t.Fatalf("a transient provider fault must stay retryable, got permanent: %v", err)
	}
}
