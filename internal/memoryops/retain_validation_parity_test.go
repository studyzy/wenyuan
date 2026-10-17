package memoryops

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// validationItems decodes a 422 body into its ordered detail items.
func validationItems(t *testing.T, rec *httptest.ResponseRecorder) []map[string]any {
	t.Helper()
	var body struct {
		Detail []map[string]any `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return body.Detail
}

// locEquals compares a decoded loc slice against the expected path elements. A
// list position is emitted as a JSON number, so each element is compared by its
// string rendering.
func locEquals(got any, want ...string) bool {
	list, ok := got.([]any)
	if !ok || len(list) != len(want) {
		return false
	}
	for i, element := range list {
		if fmt.Sprint(element) != want[i] {
			return false
		}
	}
	return true
}

// 94 D6: the entities list takes objects only. A bare string element is the
// reference's model_attributes_type rejection at the element's own path, not an
// accepted entity.
func TestRetainRejectsBareStringEntity(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"x","entities":["Alice"]}],"async":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	items := validationItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item["type"] != "model_attributes_type" {
		t.Fatalf("type = %v", item["type"])
	}
	if !locEquals(item["loc"], "body", "items", "0", "entities", "0") {
		t.Fatalf("loc = %v", item["loc"])
	}
	if item["msg"] != "Input should be a valid dictionary or object to extract fields from" {
		t.Fatalf("msg = %v", item["msg"])
	}
	if item["input"] != "Alice" {
		t.Fatalf("input = %v", item["input"])
	}
}

// 94 D7: metadata is dict[str, str]. A non-string value is a string_type
// rejection at the value's key; a JSON null is likewise rejected rather than
// dropped, in the order the caller sent them.
func TestRetainRejectsNonStringMetadataValue(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"x","metadata":{"n":1,"s":null}}],"async":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	items := validationItems(t, rec)
	if len(items) != 2 {
		t.Fatalf("items = %+v", items)
	}
	if items[0]["type"] != "string_type" || !locEquals(items[0]["loc"], "body", "items", "0", "metadata", "n") {
		t.Fatalf("first item = %+v", items[0])
	}
	if items[0]["msg"] != "Input should be a valid string" || items[0]["input"] != float64(1) {
		t.Fatalf("first item = %+v", items[0])
	}
	if items[1]["type"] != "string_type" || !locEquals(items[1]["loc"], "body", "items", "0", "metadata", "s") {
		t.Fatalf("second item = %+v", items[1])
	}
	if items[1]["input"] != nil {
		t.Fatalf("second item input = %v", items[1]["input"])
	}
}

// A string-valued metadata map passes, so the dict[str, str] shape is what is
// enforced rather than metadata itself.
func TestRetainAcceptsStringMetadata(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"x","metadata":{"k":"v"}}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 94 D8: update_mode is a literal of 'replace' and 'append'. Any other value is
// a literal_error rejection naming the accepted set.
func TestRetainRejectsUnknownUpdateMode(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"x","document_id":"d","update_mode":"merge"}],"async":true}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	items := validationItems(t, rec)
	if len(items) != 1 {
		t.Fatalf("items = %+v", items)
	}
	item := items[0]
	if item["type"] != "literal_error" {
		t.Fatalf("type = %v", item["type"])
	}
	if !locEquals(item["loc"], "body", "items", "0", "update_mode") {
		t.Fatalf("loc = %v", item["loc"])
	}
	if item["msg"] != "Input should be 'replace' or 'append'" {
		t.Fatalf("msg = %v", item["msg"])
	}
	if item["input"] != "merge" {
		t.Fatalf("input = %v", item["input"])
	}
}

// Both accepted literals pass, so only an out-of-set value is rejected.
func TestRetainAcceptsKnownUpdateModes(t *testing.T) {
	for _, mode := range []string{"replace", "append"} {
		mux, _ := newServer(t)
		rec := post(t, mux, "/v1/default/banks/b1/memories",
			`{"items":[{"content":"x","document_id":"d","update_mode":"`+mode+`"}],"async":true}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("update_mode=%s status = %d, body = %s", mode, rec.Code, rec.Body.String())
		}
	}
}

// 94 D9: with the batch API enabled a synchronous write is refused with the
// reference's explanation, because a batch retain cannot finish in a request.
func TestSyncRetainRefusedWhenBatchAPIEnabled(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(store, 5, 30000)
	h.RetainBatchEnabled = true
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"x"}],"async":false}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["detail"] != retainBatchDisabledMessage {
		t.Fatalf("detail = %v", resp["detail"])
	}
	// The async path is unaffected.
	rec = post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"x"}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("async status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 94 D10: force_reextract is not a member of the reference's write model, so a
// client that sends it is accepted and the flag is ignored.
func TestRetainIgnoresForceReextract(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"x","force_reextract":true}],"async":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// 96 D6: a repeated scalar query parameter binds its LAST value, matching the
// reference's framework; the clear route's type filter follows that rule.
func TestClearMemoriesTypeLastValueWins(t *testing.T) {
	req := httptest.NewRequest(http.MethodDelete, "/v1/default/banks/b1/memories?type=world&type=experience", nil)
	if got := lastQueryValue(req, "type"); got != "experience" {
		t.Fatalf("lastQueryValue = %q, want %q", got, "experience")
	}
}

// 96 D6: the clear route takes the type verbatim, so a whitespace-only value is
// a real (non-empty) filter that matches nothing rather than a full-bank wipe.
func TestClearMemoriesTypeVerbatim(t *testing.T) {
	mux, _ := newServer(t)
	if rec := post(t, mux, "/v1/default/banks/b1/memories", `{"items":[{"content":"kept"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("seed status = %d, body = %s", rec.Code, rec.Body.String())
	}
	rec := do(t, mux, http.MethodDelete, "/v1/default/banks/b1/memories?type=%20")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var resp map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp["deleted_count"] != float64(0) {
		t.Fatalf("deleted_count = %v, want 0", resp["deleted_count"])
	}
	if resp["message"] != "Cleared 0 memory unit(s) of type ' ' from bank 'b1'" {
		t.Fatalf("message = %v", resp["message"])
	}
}

// 94 D11: the reference's RetainRequest declares only items, async,
// operation_id and document_tags, so the deprecated "background" and
// "reflect_mission" aliases are reported as unknown body fields.
func TestRetainReportsDeprecatedAliasesAsUnknown(t *testing.T) {
	for _, field := range []string{"background", "reflect_mission"} {
		body := []byte(`{"items":[],"` + field + `":true}`)
		unknown := httpx.UnknownBodyFields(body, retainBodyFields)
		if len(unknown) != 1 || unknown[0] != field {
			t.Fatalf("UnknownBodyFields(%s) = %v", field, unknown)
		}
	}
}
