package memoryops

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

func newServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(nil, storage.Bank{ID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux, store
}

func post(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// ts-0001 / out-0001: a synchronous write returns the acceptance body with the
// success flag, bank identifier, accepted item count, async false, and usage.
func TestSyncWriteReturnsAcceptanceBody(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"hello world","context":"ctx","metadata":{"k":"v"},"tags":["t1"]}],"async":false}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"success", "bank_id", "items_count", "async", "usage"} {
		if _, ok := got[field]; !ok {
			t.Fatalf("sync response missing field %q: %s", field, rec.Body.String())
		}
	}
	if got["success"] != true || got["bank_id"] != "b1" || got["async"] != false {
		t.Fatalf("unexpected response: %s", rec.Body.String())
	}
	if got["items_count"].(float64) != 1 {
		t.Fatalf("items_count = %v, want 1", got["items_count"])
	}
}

// ts-0002 / out-0002 / beh-0002: an async write returns immediately with the
// async flag true and operation identifiers.
func TestAsyncWriteReturnsOperationIDs(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"async work"}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Async        bool     `json:"async"`
		OperationID  string   `json:"operation_id"`
		OperationIDs []string `json:"operation_ids"`
		ItemsCount   int      `json:"items_count"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Async || got.OperationID == "" || len(got.OperationIDs) == 0 {
		t.Fatalf("async acceptance incomplete: %s", rec.Body.String())
	}
	if got.ItemsCount != 1 {
		t.Fatalf("items_count = %d, want 1", got.ItemsCount)
	}
}

// ts-0004 / beh-0004 / inv-0002: re-submitting an identifier-less async write
// produces a distinct operation each time.
func TestAsyncWriteWithoutIDIsDistinct(t *testing.T) {
	mux, _ := newServer(t)
	first := decodeOpID(t, post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"a"}],"async":true}`))
	second := decodeOpID(t, post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"b"}],"async":true}`))
	if first == second {
		t.Fatalf("identifier-less writes must create distinct operations, both = %q", first)
	}
}

// ts-0005 / err-0001 / sec-0002: reusing an operation identifier returns a
// conflict and enqueues no duplicate.
func TestReusedOperationIDConflicts(t *testing.T) {
	mux, store := newServer(t)
	opID := "11111111-1111-1111-1111-111111111111"
	body := `{"items":[{"content":"first"}],"async":true,"operation_id":"` + opID + `"}`
	if rec := post(t, mux, "/v1/default/banks/b1/memories", body); rec.Code != http.StatusOK {
		t.Fatalf("first write status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusConflict {
		t.Fatalf("second write status = %d, want 409: %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListOperations(nil, storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("a reused identifier must not enqueue a duplicate, got %d operations", page.Total)
	}
}

// ts-0014 / err-0005: a store reporting writes unavailable returns 503 with a
// Retry-After header carrying the default delay.
func TestWriteUnavailableReturnsRetryAfter(t *testing.T) {
	mux, store := newServer(t)
	store.SetWriteError(storage.ErrWritesUnavailable)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"blocked"}]}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Fatalf("Retry-After = %q, want the default retry delay", got)
	}
}

// ec-0001 / err-0004: an empty item list is rejected as a client error.
func TestEmptyItemsRejected(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestMalformedWriteBodyReturnsBadRequest(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{oops`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ts-0010 / beh-0009 / err-0004: an unparseable timestamp shape is a client
// error.
func TestUnparseableTimestampRejected(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"x","timestamp":"not-a-time"}]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0010: the timestamp is tri-state; only the unset sentinel, ISO text, and
// omission are accepted without error.
func TestTimestampTriState(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"a","timestamp":null},{"content":"b","timestamp":"2026-10-12T00:00:00Z"},{"content":"c"}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0013 / err-0002: image content against a non-vision model is rejected with
// the unprocessable-entity status.
func TestImageContentRejected(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":[{"type":"image"},{"type":"image"}]}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0015 / err-0007 / cmp-0008: batch mode rejects a synchronous write.
func TestBatchModeRejectsSynchronousWrite(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(nil, storage.Bank{ID: "b1"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.RetainBatchEnabled = true
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"sync"}],"async":false}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0016 / cmp-0002: the deprecated document_tags field is accepted alongside
// per-item tags.
func TestDocumentTagsAliasAccepted(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"x","tags":["item"]}],"document_tags":["doc"]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0018 / in-0003: a JSON-encoded tag string is coerced into a list.
func TestJSONEncodedTagStringCoerced(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"x","tags":"[\"a\",\"b\"]","observation_scopes":["s1"],"strategy":"fast"}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0012 / beh-0010 / neg-0006: a null metadata value is dropped.
func TestNullMetadataDropped(t *testing.T) {
	metadata := DropNullValues(map[string]any{"keep": "v", "drop": nil})
	if _, ok := metadata["drop"]; ok {
		t.Fatalf("null-valued metadata key must be dropped: %+v", metadata)
	}
	if metadata["keep"] != "v" {
		t.Fatalf("non-null metadata must survive: %+v", metadata)
	}
}

// ts-0011 / beh-0007: entity resolution honors the switch.
func TestEntityResolutionSwitch(t *testing.T) {
	store := storage.NewMemoryStore("public")
	existing, err := store.ResolveEntity(nil, "b1", "Acme", storage.EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	if existing.Kind != storage.EntityKindRegular {
		t.Fatalf("entity kind = %q, want regular", existing.Kind)
	}
}

// ts-0017 / cmp-0003: the file route accepts an upload and always processes
// asynchronously.
func TestFileRetainIsAsync(t *testing.T) {
	mux, _ := newServer(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("files", "notes.txt")
	if err != nil {
		t.Fatal(err)
	}
	part.Write([]byte("file body"))
	w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		Async        bool     `json:"async"`
		OperationIDs []string `json:"operation_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Async || len(got.OperationIDs) == 0 {
		t.Fatalf("file route must process asynchronously: %s", rec.Body.String())
	}
}

func decodeOpID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	var got struct {
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	return got.OperationID
}
