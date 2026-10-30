package memoryops

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// timestampedDocument uploads one file under a document id with the given
// `timestamp`, and returns the document the retain stored.
func timestampedDocument(t *testing.T, store *storage.MemoryStore, timestamp string) storage.Document {
	t.Helper()
	mux := muxOver(t, store)
	request := `{"files_metadata":[{"document_id":"dated","timestamp":"` + timestamp + `"}]}`
	rec := uploadWithRequest(t, mux, "notes.txt", "Alice likes tea.", request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	document, err := store.GetDocument(context.TODO(), "b1", "dated")
	if err != nil {
		t.Fatalf("the converted upload stored no document: %v", err)
	}
	return document
}

// muxOver serves the file route over an existing store.
func muxOver(t *testing.T, store *storage.MemoryStore) *http.ServeMux {
	t.Helper()
	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}

// The file route's `files_metadata[].timestamp` reaches the document's event date
// as the caller wrote it: the reference validates it with `datetime.fromisoformat`
// and carries the value through unchanged (api/http.py FileRetainMetadata →
// memory_engine.py:23625-23632 → :4556-4560), so every form that parser accepts
// is a date — not only the fully-zoned RFC 3339 form.
func TestFileRetainAppliesEveryISOTimestampForm(t *testing.T) {
	for _, tc := range []struct {
		name      string
		timestamp string
		want      time.Time
	}{
		{"date only", "2024-01-15", time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)},
		{"naive datetime", "2024-01-15T10:30:00", time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)},
		{"zoned", "2024-01-15T10:30:00Z", time.Date(2024, 1, 15, 10, 30, 0, 0, time.UTC)},
		{"offset", "2024-01-15T10:30:00+02:00", time.Date(2024, 1, 15, 8, 30, 0, 0, time.UTC)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := storage.NewMemoryStore("public")
			if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
				t.Fatal(err)
			}
			document := timestampedDocument(t, store, tc.timestamp)
			raw, _ := document.RetainParams["event_date"].(string)
			if raw == "" {
				t.Fatalf("event_date absent from retain_params %v: the route dropped %q",
					document.RetainParams, tc.timestamp)
			}
			parsed, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				t.Fatalf("event_date = %q: %v", raw, err)
			}
			if !parsed.UTC().Equal(tc.want) {
				t.Fatalf("event_date = %v, want %v", parsed.UTC(), tc.want)
			}
		})
	}
}
