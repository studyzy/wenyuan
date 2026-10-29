//go:build pgx

package storage

import (
	"context"
	"strings"
	"testing"
)

// A consolidation rewrite persists the reference's snapshot keys and reads back
// oldest-first, so a history view rebuilt from these rows can name the
// observation's prior text, tags and dates and the sources each change folded in.
func TestPostgresObservationHistorySnapshot(t *testing.T) {
	store := postgresVectorStore(t)
	ctx := context.Background()
	const bank = "history-snapshot-bank"
	if _, err := store.CreateBank(ctx, Bank{BankID: bank, Name: "History"}); err != nil {
		t.Fatalf("create bank: %v", err)
	}
	outcome, err := store.RetainWrite(ctx, RetainRequest{
		BankID: bank, Items: []RetainItem{{Content: "the deployment runs on Tuesdays."}},
	})
	if err != nil {
		t.Fatalf("retain: %v", err)
	}
	source := outcome.ItemUnitIDs[0][0]

	observationID, err := store.CreateObservation(ctx, bank, "v1", []string{source})
	if err != nil {
		t.Fatalf("create observation: %v", err)
	}
	if err := store.UpdateObservation(ctx, bank, observationID, "v2", []string{source}); err != nil {
		t.Fatalf("update observation: %v", err)
	}

	history, err := store.MemoryHistory(ctx, bank, observationID)
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history) != 1 {
		t.Fatalf("history = %+v, want one snapshot", history)
	}
	if history[0].PreviousText == nil || *history[0].PreviousText != "v1" {
		t.Fatalf("previous_text = %v, want \"v1\"", history[0].PreviousText)
	}
	if len(history[0].NewSourceMemoryIDs) != 1 || history[0].NewSourceMemoryIDs[0] != source {
		t.Fatalf("new_source_memory_ids = %v, want the folded-in source", history[0].NewSourceMemoryIDs)
	}
	if history[0].ChangedAt.IsZero() {
		t.Fatalf("changed_at not recorded: %+v", history[0])
	}

	// The stored blob carries the reference's own key names, so a row written
	// here reads the same as one the reference wrote.
	pg := store.(*PostgresStore)
	var content string
	if err := pg.reader().QueryRowContext(ctx,
		`SELECT content::text FROM `+quoteIdent(pg.defaultSchema)+`.observation_history
		 WHERE observation_id = $1`, observationID).Scan(&content); err != nil {
		t.Fatalf("read blob: %v", err)
	}
	for _, key := range []string{"previous_text", "previous_tags", "previous_occurred_start",
		"previous_occurred_end", "previous_mentioned_at", "new_source_memory_ids"} {
		if !strings.Contains(content, `"`+key+`"`) {
			t.Fatalf("snapshot missing %q: %s", key, content)
		}
	}
	if strings.Contains(content, `"text"`) {
		t.Fatalf("snapshot must not use the old single-key shape: %s", content)
	}
}
