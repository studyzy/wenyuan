package bankapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

func newTestServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	return newMux(NewHandlers(store)), store
}

func doJSON(t *testing.T, mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

func post(t *testing.T, mux *http.ServeMux, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	return doJSON(t, mux, http.MethodPost, path, body)
}

// ts-0001 / ts-0003: the listing returns the page envelope with the documented
// field names and the pre-paging total, limit and offset.
func TestBankListingPageEnvelope(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "b1", Name: "First"})
	mustCreate(t, store, storage.Bank{ID: "b2", Name: "Second"})

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks?limit=1&offset=0", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"banks", "total", "limit", "offset"} {
		if _, ok := envelope[field]; !ok {
			t.Fatalf("page envelope missing %q: %s", field, rec.Body.String())
		}
	}
	if envelope["total"].(float64) != 2 {
		t.Fatalf("total = %v, want the pre-paging match count 2", envelope["total"])
	}
	if envelope["limit"].(float64) != 1 {
		t.Fatalf("limit = %v", envelope["limit"])
	}
	rows, ok := envelope["banks"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("page must carry one row: %s", rec.Body.String())
	}
}

// ts-0003: each row carries the bank fields plus the summary and watermark
// fields.
func TestBankListRowFields(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "b1", Name: "First"})
	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks", "")
	for _, field := range []string{
		"bank_id", "name", "disposition", "mission", "created_at", "updated_at",
		"fact_count", "last_document_at", "last_write_at",
	} {
		if !strings.Contains(rec.Body.String(), `"`+field+`"`) {
			t.Fatalf("list row missing documented field %q: %s", field, rec.Body.String())
		}
	}
	if !strings.Contains(rec.Body.String(), `"skepticism"`) {
		t.Fatalf("disposition object must carry its traits: %s", rec.Body.String())
	}
}

// ec-0004: a never-written bank carries no write watermark; a written bank
// does, and sorts most-recently-written first.
func TestBankListOrdersByRecency(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "older", Name: "Older"})
	mustCreate(t, store, storage.Bank{ID: "newer", Name: "Newer"})
	if _, err := store.RecordWrite(context.TODO(), storage.WriteRequest{BankID: "older", Content: "hello"}); err != nil {
		t.Fatal(err)
	}

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks", "")
	var envelope struct {
		Banks []BankListRow `json:"banks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Banks) != 2 {
		t.Fatalf("expected two rows: %s", rec.Body.String())
	}
	if envelope.Banks[0].BankID != "older" {
		t.Fatalf("most-recently-written bank must come first: %+v", envelope.Banks)
	}
	if envelope.Banks[0].LastWriteAt == nil {
		t.Fatalf("a written bank must carry a write watermark")
	}
	if envelope.Banks[1].LastWriteAt != nil {
		t.Fatalf("a never-written bank must not carry a write watermark")
	}
}

// ts-0002 / ec-0001: pages are disjoint with a real total, and an offset past
// the end returns an empty array with the real total.
func TestBankListPagingAndSearch(t *testing.T) {
	mux, store := newTestServer(t)
	for _, id := range []string{"alpha", "beta", "gamma", "delta"} {
		mustCreate(t, store, storage.Bank{ID: id, Name: id})
	}
	// Search matches a single bank's identifier.
	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks?q=alpha", "")
	var searched struct {
		Banks []BankListRow `json:"banks"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &searched); err != nil {
		t.Fatal(err)
	}
	if searched.Total != 1 || len(searched.Banks) != 1 {
		t.Fatalf("search must match one bank: %s", rec.Body.String())
	}

	rec = doJSON(t, mux, http.MethodGet, "/v1/default/banks?limit=2&offset=20", "")
	var past struct {
		Banks []BankListRow `json:"banks"`
		Total int           `json:"total"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &past); err != nil {
		t.Fatal(err)
	}
	if past.Total != 4 {
		t.Fatalf("past-the-end page must report the real total, got %d", past.Total)
	}
	if len(past.Banks) != 0 {
		t.Fatalf("past-the-end page must be empty: %s", rec.Body.String())
	}
}

// ts-0004: a row matched only through an alias reports the aliases that
// matched; a row matched on its own fields reports an empty list.
func TestBankListMatchedAliases(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "owner", Name: "Owner"})
	// An alias match is attributed to the owning bank.
	post(t, mux, "/v1/default/banks/owner/aliases", `{"alias":"nickname","primary":false}`)

	rec := doJSON(t, mux, http.MethodGet, "/v1/default/banks?q=nickname", "")
	var envelope struct {
		Banks []BankListRow `json:"banks"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Banks) != 1 {
		t.Fatalf("alias match must return the owning bank: %s", rec.Body.String())
	}
	matched := envelope.Banks[0].MatchedAliases
	if len(matched) != 1 || matched[0] != "nickname" {
		t.Fatalf("matched_aliases = %v, want [nickname]", matched)
	}

	rec = doJSON(t, mux, http.MethodGet, "/v1/default/banks?q=owner", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if len(envelope.Banks) != 1 || len(envelope.Banks[0].MatchedAliases) != 0 {
		t.Fatalf("identifier/name match must report no matched aliases: %v", envelope.Banks)
	}
}

// ts-0005 / ts-0006: create-or-replace applies the supplied fields, echoes the
// mission under background, and honours the documented precedence.
func TestCreateOrReplaceAndPrecedence(t *testing.T) {
	mux, _ := newTestServer(t)
	body := `{"name":"Fifth","disposition":{"skepticism":1,"literalism":2,"empathy":3},
	          "disposition_empathy":5,"mission":"m","background":"bg","reflect_mission":"explicit"}`
	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/b5", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var profile BankResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.BankID != "b5" {
		t.Fatalf("bank_id = %q", profile.BankID)
	}
	if profile.Disposition.Skepticism != 1 || profile.Disposition.Literalism != 2 {
		t.Fatalf("disposition object not applied: %+v", profile.Disposition)
	}
	if profile.Disposition.Empathy != 5 {
		t.Fatalf("individual trait must win over the disposition object: %+v", profile.Disposition)
	}
	if profile.Mission != "explicit" || profile.Background != "explicit" {
		t.Fatalf("reflect_mission must win and echo under background: %+v", profile)
	}

	rec = doJSON(t, mux, http.MethodPut, "/v1/default/banks/b5", `{"mission":"changed"}`)
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Mission != "changed" {
		t.Fatalf("replace must update the mission: %+v", profile)
	}
}

// ts-0007: a partial update applies only the supplied fields and does not
// create a missing bank.
func TestPatchUpdateAndMissingBank(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "b6", Name: "Six"})
	rec := doJSON(t, mux, http.MethodPatch, "/v1/default/banks/b6", `{"mission":"careful"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d", rec.Code)
	}
	var profile BankResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile.Mission != "careful" || profile.Name != "Six" {
		t.Fatalf("patch must apply only the supplied field: %+v", profile)
	}

	rec = doJSON(t, mux, http.MethodPatch, "/v1/default/banks/missing", `{"mission":"x"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("patching a missing bank must be not-found, got %d", rec.Code)
	}
}

// ts-0008 / ts-0009: out-of-range traits and malformed identifiers are refused.
func TestValidationFailures(t *testing.T) {
	mux, _ := newTestServer(t)
	if rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/b7", `{"skepticism":9,"disposition_skepticism":9}`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("out-of-range trait status = %d", rec.Code)
	}
	if rec := doJSON(t, mux, http.MethodPost, "/v1/default/banks/bad/memories", `{}`); rec.Code == http.StatusOK {
		t.Fatal("a control-character identifier must not succeed")
	}
}

// ts-0009: an over-long identifier is a client error.
func TestOversizeIdentifierRejected(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(store)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	long := strings.Repeat("a", storage.BankIDMaxBytes+1)
	rec := doJSON(t, mux, http.MethodPut, "/v1/default/banks/"+long, `{"name":"x"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("over-long identifier status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0010: the delete envelope carries success, message and deleted count;
// deleting a bank that never existed reports zero.
func TestDeleteEnvelope(t *testing.T) {
	mux, store := newTestServer(t)
	mustCreate(t, store, storage.Bank{ID: "b8", Name: "Eight"})
	rec := doJSON(t, mux, http.MethodDelete, "/v1/default/banks/b8", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d", rec.Code)
	}
	var envelope map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"success", "message", "deleted_count"} {
		if _, ok := envelope[field]; !ok {
			t.Fatalf("delete envelope missing %q: %s", field, rec.Body.String())
		}
	}
	if envelope["deleted_count"].(float64) != 1 {
		t.Fatalf("deleted_count = %v, want 1", envelope["deleted_count"])
	}

	rec = doJSON(t, mux, http.MethodDelete, "/v1/default/banks/never", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deleting a never-created bank must succeed, got %d", rec.Code)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["deleted_count"].(float64) != 0 {
		t.Fatalf("deleted_count = %v, want 0", envelope["deleted_count"])
	}
}

func mustCreate(t *testing.T, store *storage.MemoryStore, bank storage.Bank) {
	t.Helper()
	if _, err := store.CreateBank(context.TODO(), bank); err != nil {
		t.Fatal(err)
	}
}
