package transfer

import (
	"context"
	"reflect"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/transferarchive"
)

// A history row may carry its content in either JSON encoding: an archive this
// service wrote holds the decoded object, and one the reference wrote holds the
// JSON *text* of that object (`bank_rows_json_encoding: "serialized"`). Both
// must read the same, or the reflect response a refresh replaced is dropped and
// the history reads as a version with no response behind it.
func TestRestoreReadsHistoryContentInEitherJSONEncoding(t *testing.T) {
	cases := []struct {
		name    string
		content any
	}{
		{
			name: "decoded object",
			content: map[string]any{
				"previous_content":          "the version before",
				"previous_reflect_response": map[string]any{"based_on": map[string]any{"facts": "kept"}},
			},
		},
		{
			name:    "json text",
			content: `{"previous_content": "the version before", "previous_reflect_response": {"based_on": {"facts": "kept"}}}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			archive := &transferarchive.Archive{
				Manifest: transferarchive.Manifest{
					SchemaVersion: transferarchive.SupportedSchemaVersion,
					SourceBankID:  "src",
					ArchiveType:   "bank",
				},
				MentalModels: []map[string]any{{
					"id": "mm-1", "bank_id": "src", "name": "ledger",
				}},
				MentalModelHistory: []map[string]any{{
					"mental_model_id": "mm-1",
					"bank_id":         "src",
					"changed_at":      "2026-01-02T00:00:00Z",
					"content":         tc.content,
				}},
			}
			store := storage.NewMemoryStore("public")
			if _, err := (ImportApplier{Store: store}).Apply(ctx, ImportRequest{
				Archive: archive, BankID: "src", TargetBankID: "dst",
				Mode: ImportRestore, Scope: DefaultExportScope(),
			}); err != nil {
				t.Fatalf("import: %v", err)
			}
			history, err := store.MentalModelHistory(ctx, "dst", "mm-1")
			if err != nil || len(history) != 1 {
				t.Fatalf("history entries = %d (err %v), want one", len(history), err)
			}
			if history[0].PreviousContent == nil || *history[0].PreviousContent != "the version before" {
				t.Fatalf("previous_content = %v, want the replaced version", history[0].PreviousContent)
			}
			basedOn, _ := history[0].PreviousReflectResponse["based_on"].(map[string]any)
			if basedOn["facts"] != "kept" {
				t.Fatalf("previous_reflect_response = %v, want the replaced response", history[0].PreviousReflectResponse)
			}
		})
	}
}

// The reproduction the work-19 note refers to. archiveJSONValue is the one
// place a carried value stops following the row encoding, so it is where the
// serialized loss is produced: a row in a sync map holds no jsonb cast, so
// nothing re-encodes the value on the way in and whatever this function
// returns is what the applier reads. Returning it unchanged is the reader as
// it stood before internal/jsontext, and every member that arrives as the JSON
// text of an object then falls back to its default instead of failing loudly:
// the silent loss, with no error to look at.
func TestArchiveJSONValueReproducesTheSerializedLoss(t *testing.T) {
	text := `{"previous_reflect_response": {"based_on": {"facts": "kept"}}}`
	object := map[string]any{"previous_reflect_response": map[string]any{"based_on": map[string]any{"facts": "kept"}}}
	if got := archiveJSONValue(text); got == text {
		t.Fatalf("archiveJSONValue returned the JSON text unchanged: every serialized member now reads as absent")
	}
	if got, _ := archiveJSONValue(text).(map[string]any); !reflect.DeepEqual(got, object) {
		t.Fatalf("archiveJSONValue(text) = %#v, want the object it encodes", archiveJSONValue(text))
	}
	// A decoded object has no text to decode and is read as it is.
	if got := archiveJSONValue(object); !reflect.DeepEqual(got, object) {
		t.Fatalf("archiveJSONValue(object) = %#v, want it unchanged", got)
	}
	// A column that merely holds braces stays literal: only text opening as an
	// object or array is structure.
	plain := "the value is {not json}"
	if got := archiveJSONValue(plain); got != plain {
		t.Fatalf("archiveJSONValue(%q) = %#v, want the literal text kept", plain, got)
	}
}
