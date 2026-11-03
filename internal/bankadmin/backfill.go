package bankadmin

// Filling in the vectors a replay left out.
//
// A replay can be asked to write a bank without vectors when the rows are
// needed before the bank is read, and embedding is the slow half of an import:
// one model request per unit. This pass is the other half — it walks the
// target's units that carry no vector, embeds their text exactly as the write
// path does, and stores the result, so re-running it is a no-op once every unit
// has one.

import (
	"context"
	"fmt"
	"io"

	"github.com/studyzy/wenyuan/internal/storage"
)

// backfillUnitBatch bounds one read of the unit page, which is also the batch
// the embedding generator sees at a time.
const backfillUnitBatch = 256

// runBackfill embeds the target bank's unembedded units.
func runBackfill(ctx context.Context, store storage.Store, embeddings storage.EmbeddingGenerator, flags ImportExportFlags, out io.Writer) error {
	if flags.TargetBank == "" {
		return NewUsageError("--backfill needs --target-bank")
	}
	if embeddings == nil {
		return fmt.Errorf("--backfill needs an embedding backend")
	}
	embedded, err := backfillBankUnits(ctx, store, embeddings, flags.TargetBank)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "Embedded %d unit(s) of bank %q\n", embedded, flags.TargetBank)
	return nil
}

// backfillBankUnits pages through the bank's unembedded units and embeds them,
// returning how many were written.
func backfillBankUnits(ctx context.Context, store storage.Store, embeddings storage.EmbeddingGenerator, bankID string) (int, error) {
	written := 0
	offset := 0
	for {
		page, err := store.ListMemories(ctx, storage.MemoryQuery{BankID: bankID, Limit: backfillUnitBatch, Offset: offset})
		if err != nil {
			return written, err
		}
		if len(page.Memories) == 0 {
			return written, nil
		}
		pending := make([]storage.MemoryUnit, 0, len(page.Memories))
		for _, unit := range page.Memories {
			if len(unit.Embedding) == 0 {
				pending = append(pending, unit)
			}
		}
		if len(pending) > 0 {
			texts := make([]string, len(pending))
			for index, unit := range pending {
				texts[index] = unit.Text
			}
			vectors, err := embeddings.EmbedPassages(ctx, texts)
			if err != nil {
				return written, err
			}
			if len(vectors) != len(texts) {
				return written, fmt.Errorf("embedding generator returned %d vectors for %d texts", len(vectors), len(texts))
			}
			for index, unit := range pending {
				if len(vectors[index]) == 0 {
					continue
				}
				unit.Embedding = vectors[index]
				if _, err := store.UpsertUnit(ctx, unit); err != nil {
					return written, fmt.Errorf("store vector of %s: %w", unit.ID, err)
				}
				written++
			}
		}
		if offset+len(page.Memories) >= page.Total {
			return written, nil
		}
		offset += len(page.Memories)
	}
}
