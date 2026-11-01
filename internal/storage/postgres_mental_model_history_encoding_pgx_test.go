//go:build pgx

package storage

import (
	"context"
	"encoding/json"
	"testing"
)

// A history row may hold its content in either JSON encoding: this service
// writes the object, and a reference archive rewrites the same column as the
// JSON *text* of that object (`bank_rows_json_encoding: "serialized"`). The
// row below is written the way the reference writes it, so the reader has to
// decode it: with the text form unread, previous_content and
// previous_reflect_response come back absent and the history reads as a
// version with nothing behind it.
//
// The object direction is pinned one layer up, in
// TestRestoreReadsHistoryContentInEitherJSONEncoding; this one pins the
// direction a live reference archive actually arrives in.
func TestPostgresMentalModelHistoryReadsSerializedContent(t *testing.T) {
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
	// The reference serialises the whole content object into a JSON string and
	// stores that string into the jsonb column, so the column holds a JSON
	// string whose value is the JSON text of the object.
	object, err := json.Marshal(map[string]any{
		"previous_content":          "the version before",
		"previous_reflect_response": map[string]any{"based_on": map[string]any{"facts": "kept"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	serialized, err := json.Marshal(string(object))
	if err != nil {
		t.Fatal(err)
	}
	insert := `INSERT INTO ` + quoteIdent(pg.Schema()) + `.mental_model_history
	          (mental_model_id, bank_id, content, changed_at)
	          VALUES ($1, $2, ($3::text)::jsonb, now() - ($4::int * interval '1 second'))`
	if _, err := pg.db.ExecContext(ctx, insert, "mm-1", "b1", string(serialized), 60); err != nil {
		t.Fatalf("insert serialized row: %v", err)
	}
	// Read the column back the way the reader will see it. A jsonb string that
	// the driver hands back already decoded cannot reach the reader as text, so
	// the case below cannot be exercised here and is pinned by the transfer test
	// instead.
	var echo []byte
	if err := pg.db.QueryRowContext(ctx, `SELECT content FROM `+quoteIdent(pg.Schema())+`.mental_model_history`).Scan(&echo); err != nil {
		t.Fatalf("read back: %v", err)
	}
	var decoded any
	if err := json.Unmarshal(echo, &decoded); err != nil {
		t.Fatalf("decode column: %v", err)
	}
	if _, isText := decoded.(string); !isText {
		t.Skipf("driver returns jsonb strings pre-decoded here, so the reader never sees text: %s", echo)
	}

	history, err := pg.MentalModelHistory(ctx, "b1", "mm-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("history entries = %d, want 1", len(history))
	}
	entry := history[0]
	if entry.PreviousContent == nil || *entry.PreviousContent != "the version before" {
		t.Fatalf("previous_content = %v, want the replaced version read out of the JSON text", entry.PreviousContent)
	}
	basedOn, _ := entry.PreviousReflectResponse["based_on"].(map[string]any)
	if basedOn["facts"] != "kept" {
		t.Fatalf("previous_reflect_response = %v, want the replaced response read out of the JSON text", entry.PreviousReflectResponse)
	}
}
