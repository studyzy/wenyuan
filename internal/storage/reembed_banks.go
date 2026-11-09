package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
)

// BackfillVectorsForBanks reindexes every bank the caller names, reporting each
// one as it finishes. The banks run in sequence: the embedding backend is
// shared, so running several at once would compete with serving traffic for it.
func (s *PostgresStore) BackfillVectorsForBanks(ctx context.Context, bankIDs []string, embed EmbeddingGenerator, out io.Writer) (int, error) {
	total := 0
	for _, bankID := range bankIDs {
		report, err := s.BackfillVectors(ctx, bankID, embed)
		if err != nil {
			if errors.Is(err, ErrBankNotFound) {
				slog.Warn("reindex: bank not found", "bank_id", bankID)
				continue
			}
			return total, fmt.Errorf("reindex %q: %w", bankID, err)
		}
		total += report.RowsRebuilt
		_, _ = fmt.Fprintf(out, "%s: %d rows\n", bankID, report.RowsRebuilt)
		slog.Info("reindex: bank done", "bank_id", bankID, "rows", report.RowsRebuilt)
	}
	return total, nil
}
