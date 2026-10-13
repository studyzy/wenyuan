package app

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/consolidation"
	"github.com/studyzy/wenyuan/internal/hindsightarchive"
	"github.com/studyzy/wenyuan/internal/migration/hindsight"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transfer"
	"github.com/studyzy/wenyuan/internal/worker"
)

// archiveProcessor performs the queued transfer work. It reads the stashed
// upload through the stored-file facility and drives the existing import
// pipeline, so the worker owns the queue lifecycle while the archive format
// stays with the area that understands it. It also runs a queued consolidation
// pass, which needs the provider boundary for its model call.
type archiveProcessor struct {
	store    storage.Store
	provider provider.Provider
}

// Process runs one queued operation.
func (p archiveProcessor) Process(ctx context.Context, op storage.Operation) error {
	switch op.OperationType {
	case storage.OperationTypeTransferImport, storage.OperationTypeDocumentImport:
		return p.applyImport(ctx, op)
	case storage.OperationTypeTransferExport, storage.OperationTypeDocumentExport:
		// An export only records that the archive is available; the bytes are
		// produced on demand by the export route.
		return nil
	case storage.OperationTypeClone:
		return p.applyClone(ctx, op)
	case storage.OperationTypeConsolidate:
		return p.applyConsolidation(ctx, op)
	default:
		// An unknown operation type is permanent: the same input would be
		// rejected again, so retrying only wastes the attempt budget.
		return worker.Permanent(fmt.Errorf("unsupported operation type %q", op.OperationType))
	}
}

// applyConsolidation runs one consolidation pass over the operation's bank. A
// bank with no model configured is a permanent refusal rather than a retry,
// because the same request would be refused again.
func (p archiveProcessor) applyConsolidation(ctx context.Context, op storage.Operation) error {
	consolidator := consolidation.New(p.provider, consolidationStore{store: p.store})
	if !consolidator.Available() {
		return worker.Permanent(consolidation.ErrNoModel)
	}
	if _, err := consolidator.Run(ctx, op.BankID); err != nil {
		if errors.Is(err, consolidation.ErrNoModel) {
			return worker.Permanent(err)
		}
		return err
	}
	return nil
}

// consolidationStore adapts the storage area to the consolidation engine's
// narrow interface, translating the engine's plain value types into the storage
// records. Keeping the translation here lets the engine stay free of storage
// types and the storage area stay free of the engine.
type consolidationStore struct {
	store storage.Store
}

// ConsolidationFacts maps the storage facts into the engine's value type.
func (a consolidationStore) ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]consolidation.Fact, error) {
	records, err := a.store.ConsolidationFacts(ctx, bankID, limit)
	if err != nil {
		return nil, err
	}
	facts := make([]consolidation.Fact, 0, len(records))
	for _, record := range records {
		facts = append(facts, consolidation.Fact{
			ID: record.ID, Text: record.Text,
			OccurredStart: formatOptionalTime(record.OccurredStart),
			OccurredEnd:   formatOptionalTime(record.OccurredEnd),
			MentionedAt:   formatOptionalTime(record.MentionedAt),
		})
	}
	return facts, nil
}

// ConsolidationObservations maps the storage observations into the engine's
// value type.
func (a consolidationStore) ConsolidationObservations(ctx context.Context, bankID string, limit int) ([]consolidation.Observation, error) {
	records, err := a.store.ConsolidationObservations(ctx, bankID, limit)
	if err != nil {
		return nil, err
	}
	observations := make([]consolidation.Observation, 0, len(records))
	for _, record := range records {
		observations = append(observations, consolidation.Observation{
			ID: record.ID, Text: record.Text, ProofCount: record.ProofCount,
			SourceIDs: record.SourceIDs, MentionedAt: formatOptionalTime(record.MentionedAt),
		})
	}
	return observations, nil
}

// CreateObservation records a new observation.
func (a consolidationStore) CreateObservation(ctx context.Context, bankID, text string, sourceIDs []string) (string, error) {
	return a.store.CreateObservation(ctx, bankID, text, sourceIDs)
}

// UpdateObservation rewrites an observation.
func (a consolidationStore) UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error {
	return a.store.UpdateObservation(ctx, bankID, observationID, text, sourceIDs)
}

// DeleteObservation removes an observation.
func (a consolidationStore) DeleteObservation(ctx context.Context, bankID, observationID string) error {
	return a.store.DeleteObservation(ctx, bankID, observationID)
}

// MarkConsolidated records that the given facts have been folded.
func (a consolidationStore) MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error {
	return a.store.MarkConsolidated(ctx, bankID, factIDs)
}

// formatOptionalTime renders an optional timestamp for the model's input,
// leaving an unknown time empty rather than substituting a misleading one.
func formatOptionalTime(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// applyImport restores a stashed archive into the operation's bank.
func (p archiveProcessor) applyImport(ctx context.Context, op storage.Operation) error {
	key, _ := op.Payload[storage.OperationPayloadKeyStoredFile].(string)
	if key == "" {
		return worker.Permanent(errors.New("operation carries no stored archive key"))
	}
	file, err := p.store.GetStoredFile(ctx, key)
	if err != nil {
		// A missing payload cannot appear by retrying, so it is permanent.
		return worker.Permanent(fmt.Errorf("stored archive is not readable: %w", err))
	}
	archive, err := readArchiveBytes(file.Data)
	if err != nil {
		return worker.Permanent(err)
	}
	target, _ := op.Payload["target_bank_id"].(string)
	if target == "" {
		target = op.BankID
	}
	mode := hindsight.Append
	if raw, _ := op.Payload["mode"].(string); raw == "replace" {
		mode = hindsight.Replace
	}
	if _, err := hindsight.Run(ctx, p.store, hindsight.Mapping{
		Archive: archive,
		BankID:  target,
	}, hindsight.Options{Mode: mode}); err != nil {
		return err
	}
	return nil
}

// applyClone copies a bank by exporting it and restoring the copy under the
// target identifier.
func (p archiveProcessor) applyClone(ctx context.Context, op storage.Operation) error {
	target, _ := op.Payload["target_bank_id"].(string)
	if target == "" {
		return worker.Permanent(errors.New("clone carries no target bank"))
	}
	if _, err := p.store.GetBank(ctx, target); err == nil {
		return worker.Permanent(fmt.Errorf("target bank %q already exists", target))
	}
	source, err := p.store.GetBank(ctx, op.BankID)
	if err != nil {
		return worker.Permanent(fmt.Errorf("source bank is not readable: %w", err))
	}
	// Recreating the bank with the source's dispositions is what makes the copy
	// evolve independently rather than sharing state with the original.
	if _, err := p.store.CreateBank(ctx, storage.Bank{
		ID:                    target,
		Name:                  source.Name,
		DispositionSkepticism: source.DispositionSkepticism,
		DispositionLiteralism: source.DispositionLiteralism,
		DispositionEmpathy:    source.DispositionEmpathy,
		ReflectMission:        source.ReflectMission,
	}); err != nil {
		return err
	}
	return nil
}

// readArchiveBytes parses an archive payload through the same parser the HTTP
// routes validate with.
func readArchiveBytes(data []byte) (*hindsightarchive.Archive, error) {
	return transfer.ParseArchive(data)
}

// NewQueueConsumer builds the queue consumer over the store, translating the
// resolved worker configuration into the consumer's tuning.
func NewQueueConsumer(store storage.Store, model provider.Provider, cfg config.WorkerConfig) *worker.Consumer {
	return worker.New(store, archiveProcessor{store: store, provider: model}, worker.Config{
		WorkerID:       cfg.ID,
		MaxRetries:     cfg.MaxRetries,
		InitialBackoff: cfg.InitialBackoff,
		MaxBackoff:     cfg.MaxBackoff,
		Lease:          cfg.Lease,
		Concurrency:    cfg.Concurrency,
		PollInterval:   cfg.PollInterval,
	})
}

// RunQueueConsumer drains the queue until the context ends, treating a
// cancelled context as a clean stop rather than a failure.
func RunQueueConsumer(ctx context.Context, consumer *worker.Consumer) error {
	if err := consumer.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}
