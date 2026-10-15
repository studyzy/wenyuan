package memoryops

import (
	"bytes"
	"context"
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
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
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

// ts-0005 / err-0001 / sec-0002: a resubmitted identifier rejoins the original
// submission rather than queueing a second copy of the work.
//
// The reference resolves the identifier before it queues anything: an id that
// already names this bank's batch returns that operation, and the work is not
// duplicated. Only an identifier in use by another bank — or by another kind of
// operation — is a conflict.
func TestReusedOperationIDRejoinsTheSubmission(t *testing.T) {
	mux, store := newServer(t)
	opID := "11111111-1111-1111-1111-111111111111"
	body := `{"items":[{"content":"first"}],"async":true,"operation_id":"` + opID + `"}`
	if rec := post(t, mux, "/v1/default/banks/b1/memories", body); rec.Code != http.StatusOK {
		t.Fatalf("first write status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("second write status = %d, want 200 (a replay of the same submission): %s", rec.Code, rec.Body.String())
	}
	if got := decodeOpID(t, rec); got != opID {
		t.Fatalf("a resubmitted identifier must return the original operation, got %q", got)
	}
	page, err := store.ListOperations(context.TODO(), storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	// One parent and its one child: the resubmission must not add a second
	// batch to the queue.
	if page.Total != 2 {
		t.Fatalf("a resubmitted identifier must not enqueue a duplicate, got %d operations", page.Total)
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

func TestMalformedWriteBodyIsUnprocessable(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{oops`)
	// A rejected body is the reference framework's structured 422, not a 400
	// sentence.
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"json_invalid"`) {
		t.Fatalf("body = %s", rec.Body.String())
	}
}

// ts-0010 / beh-0009 / err-0004: an unparseable timestamp shape is a client
// error.
func TestUnparseableTimestampRejected(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"x","timestamp":"not-a-time"}]}`)
	// The reference framework rejects a malformed field value as a validation
	// item, so the status is 422 rather than 400.
	if rec.Code != http.StatusUnprocessableEntity {
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

// The "unset" sentinel stores the item without a timestamp, and is matched
// case-insensitively; an empty string means the same.
func TestTimestampUnsetSentinel(t *testing.T) {
	for _, raw := range []string{`"unset"`, `"UNSET"`, `"Unset"`, `""`} {
		t.Run(raw, func(t *testing.T) {
			mux, _ := newServer(t)
			body := `{"items":[{"content":"x","timestamp":` + raw + `}]}`
			if rec := post(t, mux, "/v1/default/banks/b1/memories", body); rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// An omitted timestamp defaults to the write time; an explicit "unset" leaves
// the item without one, so the two are distinguishable in the store.
func TestTimestampOmissionDefaultsToNow(t *testing.T) {
	mux, store := newServer(t)
	body := `{"items":[{"content":"dated"},{"content":"undated","timestamp":"unset"}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	var dated, undated bool
	for _, row := range page.Memories {
		if row.Text == "dated" && row.MentionedAt != nil {
			dated = true
		}
		if row.Text == "undated" && row.MentionedAt == nil {
			undated = true
		}
	}
	if !dated || !undated {
		t.Fatalf("dated defaulted = %v, undated cleared = %v", dated, undated)
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
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1"}); err != nil {
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

// ts-0018 / in-0003: a JSON-encoded tag string is coerced into a list, and the
// explicit observation-scope groups are accepted alongside it.
func TestJSONEncodedTagStringCoerced(t *testing.T) {
	mux, _ := newServer(t)
	body := `{"items":[{"content":"x","tags":"[\"a\",\"b\"]","observation_scopes":[["s1"]],"strategy":"fast"}]}`
	rec := post(t, mux, "/v1/default/banks/b1/memories", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// The observation scope accepts the named mode literal as well as explicit
// tag groups, and rejects a flat string list, which names neither.
func TestObservationScopeAcceptsModeAndGroups(t *testing.T) {
	for _, tc := range []struct {
		name string
		raw  string
		want int
	}{
		{"mode literal", `"per_tag"`, http.StatusOK},
		{"explicit groups", `[["a","b"],["c"]]`, http.StatusOK},
		{"flat list is not a form", `["s1"]`, http.StatusUnprocessableEntity},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := newServer(t)
			body := `{"items":[{"content":"x","observation_scopes":` + tc.raw + `}]}`
			rec := post(t, mux, "/v1/default/banks/b1/memories", body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
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
	existing, err := store.ResolveEntity(context.TODO(), "b1", "Acme", storage.EntityKindRegular, true)
	if err != nil {
		t.Fatal(err)
	}
	if existing.EntityKind != storage.EntityKindRegular {
		t.Fatalf("entity kind = %q, want regular", existing.EntityKind)
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
	_, _ = part.Write([]byte("file body"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var got struct {
		OperationIDs []string `json:"operation_ids"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.OperationIDs) == 0 {
		t.Fatalf("file route must return the queued operation ids: %s", rec.Body.String())
	}
}

// A6: the file route records the uploaded bytes content-addressed and gives
// the units it writes the attachment's short id, so provenance survives.
func TestFileRetainPersistsAttachment(t *testing.T) {
	mux, store := newServer(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("files", "diagram.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("png bytes"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}

	// The upload names no document, so the ingress mints one — and the
	// attachment is filed under that document, which is the id the memory units
	// will carry. Filing it under the file's name instead would leave the
	// attachment unreachable from the document it belongs to.
	documents, err := store.ListDocuments(context.TODO(), storage.DocumentQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(documents.Documents) != 1 {
		t.Fatalf("documents = %d, want 1", len(documents.Documents))
	}
	documentID := documents.Documents[0].ID
	attachments, err := store.ListAttachments(context.TODO(), "b1", documentID)
	if err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 {
		t.Fatalf("attachments for document %q = %d, want 1", documentID, len(attachments))
	}
	attachment := attachments[0]
	if attachment.Filename != "diagram.png" {
		t.Fatalf("filename = %q, want the uploaded name", attachment.Filename)
	}
	if len(attachment.ShortID) != attachmentShortIDLength {
		t.Fatalf("short id = %q, want %d chars", attachment.ShortID, attachmentShortIDLength)
	}
	// The bytes are served back by the download route under the storage key.
	file, err := store.GetStoredFile(context.TODO(), attachment.StorageKey)
	if err != nil {
		t.Fatalf("stored bytes: %v", err)
	}
	if string(file.Data) != "png bytes" {
		t.Fatalf("stored bytes = %q, want %q", file.Data, "png bytes")
	}

	// The memory unit written from the upload names the attachment.
	page, err := store.ListMemories(context.TODO(), storage.MemoryQuery{BankID: "b1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, unit := range page.Memories {
		for _, id := range unit.AttachmentIDs {
			if id == attachment.ShortID {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("no memory unit carried attachment id %q", attachment.ShortID)
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

// The retain entity-resolution switch defaults to true when omitted, matching
// the reference: an omitted resolve_entities resolves supplied entity names
// against the bank, and only an explicit false takes them literally.
func TestRetainResolveEntitiesDefaultsTrue(t *testing.T) {
	tests := []struct {
		name string
		body string
		want bool
	}{
		{"omitted defaults true", `{"items":[{"content":"x","entities":["Alice"]}]}`, true},
		{"explicit true", `{"items":[{"content":"x","entities":["Alice"],"resolve_entities":true}]}`, true},
		{"explicit false", `{"items":[{"content":"x","entities":["Alice"],"resolve_entities":false}]}`, false},
	}
	for _, tc := range tests {
		var req RetainRequest
		if err := json.Unmarshal([]byte(tc.body), &req); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := toStorageRetainItem(req.Items[0]).ResolveEntities; got != tc.want {
			t.Fatalf("%s: ResolveEntities = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// The retain timestamp accepts the naive ISO form the reference's
// datetime.fromisoformat accepts, not only the timezone-qualified RFC3339 form.
func TestRetainAcceptsNaiveISOTimestamp(t *testing.T) {
	var req RetainRequest
	if err := json.Unmarshal([]byte(`{"items":[{"content":"x","timestamp":"2024-01-15T10:30:00"}]}`), &req); err != nil {
		t.Fatalf("naive ISO timestamp rejected: %v", err)
	}
	stored := toStorageRetainItem(req.Items[0])
	if !stored.HasTimestamp || stored.Timestamp == nil {
		t.Fatalf("naive timestamp not captured: %+v", stored)
	}
}

// The file retain route reports not-found when the file-upload API is
// disabled, matching the reference's gate.
func TestFileRetainGatedWhenDisabled(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.FileUploadDisabled = true
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "notes.txt")
	_, _ = part.Write([]byte("body"))
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("disabled file upload = %d, want 404 (body %s)", rec.Code, rec.Body.String())
	}
}

// The file route validates the optional request parser against the allowlist.
func TestFileRetainRejectsUnknownParser(t *testing.T) {
	mux, _ := newServer(t)
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, _ := w.CreateFormFile("files", "notes.txt")
	_, _ = part.Write([]byte("body"))
	_ = w.WriteField("request", `{"parser":"nope"}`)
	_ = w.Close()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unknown parser = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
}

// When the deployment opts out of storing raw source text, a retain keeps the
// document's text out of storage while the content hash still comes from the
// real content.
func TestRetainDiscardsDocumentTextWhenDisabled(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.DiscardDocumentText = true
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"the raw secret","document_id":"d1"}],"async":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	doc, err := store.GetDocument(context.TODO(), "b1", "d1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.OriginalText != "" {
		t.Fatalf("raw text must not be stored, got %q", doc.OriginalText)
	}
	if doc.ContentHash != storage.ContentHash("the raw secret") {
		t.Fatalf("content hash must come from the real content")
	}
	chunks, err := store.ListChunks(context.TODO(), storage.ChunkQuery{BankID: "b1", DocumentID: "d1", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks.Chunks) == 0 {
		t.Fatal("chunks must still be stored")
	}
	for _, chunk := range chunks.Chunks {
		if chunk.ChunkText != "" {
			t.Fatalf("chunk text must not be stored, got %q", chunk.ChunkText)
		}
	}
}

// A bank's own store_document_text override wins over the deployment default,
// in both directions.
func TestBankStoreDocumentTextOverride(t *testing.T) {
	// Deployment keeps text; the bank opts out.
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{
		BankID: "b1", Name: "Bank", Config: map[string]any{"store_document_text": false},
	}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	if rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"bank opted out","document_id":"d1"}],"async":false}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	doc, _ := store.GetDocument(context.TODO(), "b1", "d1")
	if doc.OriginalText != "" {
		t.Fatalf("bank override must discard text, got %q", doc.OriginalText)
	}

	// Deployment discards; the bank opts back in.
	store2 := storage.NewMemoryStore("public")
	if _, err := store2.CreateBank(context.TODO(), storage.Bank{
		BankID: "b2", Name: "Bank", Config: map[string]any{"store_document_text": true},
	}); err != nil {
		t.Fatal(err)
	}
	h2 := NewHandlers(store2, 5, 30000)
	h2.DiscardDocumentText = true
	mux2 := http.NewServeMux()
	h2.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux2.HandleFunc(method+" "+pattern, handler)
	})
	if rec := post(t, mux2, "/v1/default/banks/b2/memories", `{"items":[{"content":"bank opted in","document_id":"d1"}],"async":false}`); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	doc2, _ := store2.GetDocument(context.TODO(), "b2", "d1")
	if doc2.OriginalText != "bank opted in" {
		t.Fatalf("bank override must keep text, got %q", doc2.OriginalText)
	}
}

// An append-mode write is refused when document text storage is off: there is
// no stored text to prepend to.
func TestAppendRefusedWhenTextStorageDisabled(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{
		BankID: "b1", Name: "Bank", Config: map[string]any{"store_document_text": false},
	}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"more","document_id":"d1","update_mode":"append"}],"async":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("append with text storage off = %d, want 400 (body %s)", rec.Code, rec.Body.String())
	}
}
