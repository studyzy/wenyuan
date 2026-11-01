//go:build pgx

package storage

import (
	"context"
	"encoding/json"
	"testing"
)

// A history row may hold its content in either JSON encoding: this service
// writes the keys the reader looks for, and a reference archive rewrites the
// same column as the JSON *text* of that object. The reader has to accept both,
// or the replaced content and reflect response of an imported row are read as
// absent and the history reads as a version with nothing behind it.
func TestPostgresMentalModelHistoryReadsEitherJSONEncoding(t *testing.T) {
	pg := fullRefreshStore(t)
	ctx := context.Background()
	if _, err := pg.CreateBank(ctx, Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.PutMentalModel(ctx, MentalModel{
		ID: "mm-1", BankID: "b1", Subtype: SubtypePinned, Name: "note", SourceQuery: "q",
		Content: "current",
	}); err != nil {
		t.Fatal(err)
	}
	// The object a row holds, under the reference's keys.
	written, err := json.Marshal(map[string]any{
		"previous_content":          "the version before",
		"previous_reflect_response": map[string]any{"based_on": map[string]any{"facts": "kept"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(string(written))
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO ` + quoteIdent(pg.Schema()) + `.mental_model_history
	          (mental_model_id, bank_id, content, changed_at)
	          VALUES ($1, $2, ($3::text)::jsonb, now() - ($4::int * interval '1 second'))`
	if _, err := pg.db.ExecContext(ctx, insert, "mm-1", "b1", string(written), 60); err != nil {
		t.Fatalf("insert object row: %v", err)
	}
	var echo []byte
	if err := pg.db.QueryRowContext(ctx, `SELECT content FROM `+quoteIdent(pg.Schema())+`.mental_model_history`).Scan(&echo); err != nil {
		t.Fatalf("read back: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(echo, &decoded); err != nil {
		t.Fatalf("ran: %v", err)
	}
	if _, isText := decoded.(string); isText {
		t.Skipf("driver returns jsonb as quoted text here: %s", echo)
	}
	if _, err := pg.db.ExecContext(ctx, insert, "mm-1", "b1", string(serialized), 0); err != nil {
		t.Fatalf("insert text row: %v", err)
	}
	history, err := pg.MentalModelHistory(ctx, "b1", "mm-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 2 {
		t.Fatalf("history entries = %d, want 2", len(history))
	}
	for index, entry := range history {
		if entry.PreviousContent == nil || *entry.PreviousContent != "the version before" {
			t.Fatalf("row %d previous_content = %v, want the replaced version", index, entry.PreviousContent)
		}
		basedOn, _ := entry.PreviousReflectResponse["based_on"].(map[string]any)
		if basedOn["facts"] != "kept" {
			t.Fatalf("row %d previous_reflect_response = %v, want the replaced response", index, entry.PreviousReflectResponse)
		}
	}
}
