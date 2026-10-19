package directives

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newServer builds the route table over an in-memory store holding one bank.
func newServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			// The real router supplies path values; the bare mux needs them set.
			r.SetPathValue("bank_id", "b1")
			if strings.Contains(pattern, "{directive_id}") {
				r.SetPathValue("directive_id", strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/v1/default/banks/b1/directives/"), ""))
			}
			handler(w, r)
		})
	})
	return mux, store
}

// send issues one request against the family's route table.
func send(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The full create → list → get → update → delete lifecycle through the routes.
func TestDirectiveLifecycle(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"

	created := send(mux, http.MethodPost, base, `{"name":"tone","content":"be terse","priority":5}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d, body = %s", created.Code, created.Body.String())
	}
	var directive Directive
	if err := json.Unmarshal(created.Body.Bytes(), &directive); err != nil {
		t.Fatal(err)
	}
	if directive.Name != "tone" || !directive.IsActive || directive.Priority != 5 {
		t.Fatalf("created directive = %+v", directive)
	}
	if directive.Tags == nil {
		t.Fatal("tags must be an array, not null")
	}

	listed := send(mux, http.MethodGet, base, "")
	if listed.Code != http.StatusOK {
		t.Fatalf("list: %d", listed.Code)
	}
	var list ListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 || list.Total != 1 {
		t.Fatalf("list = %+v", list)
	}

	got := send(mux, http.MethodGet, base+"/"+directive.ID, "")
	if got.Code != http.StatusOK {
		t.Fatalf("get: %d, body = %s", got.Code, got.Body.String())
	}

	updated := send(mux, http.MethodPatch, base+"/"+directive.ID, `{"priority":9,"is_active":false}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d, body = %s", updated.Code, updated.Body.String())
	}
	var after Directive
	if err := json.Unmarshal(updated.Body.Bytes(), &after); err != nil {
		t.Fatal(err)
	}
	if after.Priority != 9 || after.IsActive {
		t.Fatalf("updated directive = %+v", after)
	}

	deleted := send(mux, http.MethodDelete, base+"/"+directive.ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete: %d", deleted.Code)
	}
	var deletedBody DeleteResponse
	if err := json.Unmarshal(deleted.Body.Bytes(), &deletedBody); err != nil {
		t.Fatal(err)
	}
	if deletedBody.Status != "deleted" {
		t.Fatalf("delete body = %+v", deletedBody)
	}
	if got := send(mux, http.MethodGet, base+"/"+directive.ID, ""); got.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", got.Code)
	}
}

// A create without a name or without content is a structured 422.
func TestDirectiveCreateValidation(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	for _, body := range []string{`{"content":"x"}`, `{"name":"x"}`, `{}`} {
		rec := send(mux, http.MethodPost, base, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("create %s: %d, want 422", body, rec.Code)
		}
		var failure struct {
			Detail []struct {
				Loc []string `json:"loc"`
				Msg string   `json:"msg"`
			} `json:"detail"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
			t.Fatal(err)
		}
		if len(failure.Detail) == 0 || failure.Detail[0].Loc[0] != "body" {
			t.Fatalf("create %s detail = %+v", body, failure.Detail)
		}
	}
	if rec := send(mux, http.MethodPost, base, `not json`); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("malformed body: %d, want 422", rec.Code)
	}
}

// The list defaults to active-only and orders by priority, and untagged
// directives stay in scope when a tag filter is supplied.
func TestDirectiveListDefaultsAndOrdering(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	// A low-priority active directive, a high-priority one created first, an
	// inactive one, and an untagged global directive.
	send(mux, http.MethodPost, base, `{"name":"high","content":"h","priority":9}`)
	send(mux, http.MethodPost, base, `{"name":"low","content":"l","priority":1}`)
	send(mux, http.MethodPost, base, `{"name":"off","content":"x","priority":50,"is_active":false}`)
	send(mux, http.MethodPost, base, `{"name":"global","content":"g","priority":5}`)

	rec := send(mux, http.MethodGet, base, "")
	var list ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	// The inactive directive is excluded by default; the rest are priority-ordered.
	if list.Total != 3 {
		t.Fatalf("default list must exclude inactive directives: %+v", list)
	}
	if list.Items[0].Name != "high" || list.Items[1].Name != "global" || list.Items[2].Name != "low" {
		t.Fatalf("directives must be priority-ordered: %+v", list.Items)
	}
	if list.Limit != 100 {
		t.Fatalf("default limit = %d, want 100", list.Limit)
	}

	// active_only=false includes the inactive directive.
	rec = send(mux, http.MethodGet, base+"?active_only=false", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 4 {
		t.Fatalf("active_only=false list = %+v", list)
	}

	// An invalid tags_match is a structured 422 on the query parameter.
	rec = send(mux, http.MethodGet, base+"?tags_match=all_strict", "")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid tags_match: %d, want 422", rec.Code)
	}
	// An out-of-range limit is a structured 422.
	if rec := send(mux, http.MethodGet, base+"?limit=0", ""); rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("limit=0: %d, want 422", rec.Code)
	}
}

// A tag filter admits untagged (global) directives plus any tagged directive
// whose tags match the requested scope.
func TestDirectiveTagScoping(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	send(mux, http.MethodPost, base, `{"name":"global","content":"g"}`)
	send(mux, http.MethodPost, base, `{"name":"team","content":"t","tags":["team"]}`)
	send(mux, http.MethodPost, base, `{"name":"other","content":"o","tags":["other"]}`)

	rec := send(mux, http.MethodGet, base+"?tags=team", "")
	var list ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, item := range list.Items {
		names[item.Name] = true
	}
	if !names["global"] || !names["team"] || names["other"] {
		t.Fatalf("tag scoping = %+v", list.Items)
	}
}

// An explicit is_active of false is honored, which is what makes deactivation
// possible at creation time.
func TestDirectiveCreateHonorsExplicitInactive(t *testing.T) {
	mux, _ := newServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/b1/directives",
		`{"name":"n","content":"c","is_active":false}`)
	var directive Directive
	if err := json.Unmarshal(rec.Body.Bytes(), &directive); err != nil {
		t.Fatal(err)
	}
	if directive.IsActive {
		t.Fatal("an explicit false must not be overridden by the default")
	}
}

// An unknown bank answers not-found, so the route cannot be used to probe.
func TestDirectiveUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodGet, "/v1/default/banks/missing/directives", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}
}

// A missing directive identifier is not a 404: the id is bound to a `uuid`
// column, so a segment that is not a UUID fails while the driver binds the
// argument and the reference reports that failure as a 500. Only a well-formed
// UUID that no row carries reaches the handler's own not-found.
func TestDirectiveMissingMember(t *testing.T) {
	mux, _ := newServer(t)
	rec := send(mux, http.MethodGet, "/v1/default/banks/b1/directives/does-not-exist", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("non-uuid directive id: %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid UUID 'does-not-exist'") {
		t.Fatalf("body = %s, want the driver's invalid-UUID message", rec.Body.String())
	}

	rec = send(mux, http.MethodGet, "/v1/default/banks/b1/directives/00000000-0000-0000-0000-000000000000", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("absent uuid: %d, want 404", rec.Code)
	}
}

// asHTTPError maps each storage error to its envelope.
func TestDirectiveErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		expect int
	}{
		{storage.ErrBankNotFound, http.StatusNotFound},
		{storage.ErrDirectiveNotFound, http.StatusNotFound},
		{storage.ErrWritesUnavailable, http.StatusServiceUnavailable},
		// An error the storage layer did not name is a server fault, not a
		// malformed request: the reference's catch-all answers 500.
		{errors.New("storage exploded"), http.StatusInternalServerError},
		{nil, 0},
	}
	for _, tc := range cases {
		got := asHTTPError(tc.err)
		if tc.err == nil {
			if got != nil {
				t.Fatalf("nil error must map to nil")
			}
			continue
		}
		if got == nil || got.Status != tc.expect {
			t.Fatalf("error %v mapped to %+v, want status %d", tc.err, got, tc.expect)
		}
	}
}

// A store that cannot take writes answers service-unavailable rather than
// blaming the caller's body.
func TestDirectiveWriteUnavailableAnswers503(t *testing.T) {
	mux, store := newServer(t)
	store.SetWriteError(storage.ErrWritesUnavailable)
	rec := send(mux, http.MethodPost, "/v1/default/banks/b1/directives", `{"name":"tone","content":"be terse"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// A partial update whose members are all null leaves the directive unchanged:
// every member of the reference's update model is nullable, so an explicit null
// means "no change" rather than a rejected value, and the resulting empty patch
// must be applied without failing.
func TestDirectiveUpdateAllNullMembers(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	created := send(mux, http.MethodPost, base, `{"name":"tone","content":"be terse","priority":5}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d (body %s)", created.Code, created.Body.String())
	}
	var directive Directive
	if err := json.Unmarshal(created.Body.Bytes(), &directive); err != nil {
		t.Fatal(err)
	}
	rec := send(mux, http.MethodPatch, base+"/"+directive.ID,
		`{"name":null,"content":null,"priority":null,"is_active":null,"tags":null}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("all-null update: %d (body %s)", rec.Code, rec.Body.String())
	}
	var updated Directive
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatal(err)
	}
	if updated.Name != "tone" || updated.Content != "be terse" || updated.Priority != 5 || !updated.IsActive {
		t.Fatalf("an all-null update changed the directive: %+v", updated)
	}
}

// A body that is only whitespace is malformed, not absent: the parser rejects
// it, so the caller reads the json_invalid item rather than a missing body.
func TestDirectiveUpdateWhitespaceBody(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	created := send(mux, http.MethodPost, base, `{"name":"tone","content":"be terse"}`)
	var directive Directive
	if err := json.Unmarshal(created.Body.Bytes(), &directive); err != nil {
		t.Fatal(err)
	}
	rec := send(mux, http.MethodPatch, base+"/"+directive.ID, "   ")
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"type":"json_invalid"`) {
		t.Fatalf("detail = %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), `"type":"missing"`) {
		t.Fatalf("a whitespace body was reported as absent: %s", rec.Body.String())
	}
}

// The query selectors follow the reference's framework: a boolean parameter
// accepts its word list case-insensitively, a repeated scalar parameter takes
// its last occurrence, and a parameter present with no value is still validated
// rather than treated as absent.
func TestDirectiveListQuerySemantics(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	send(mux, http.MethodPost, base, `{"name":"a","content":"c"}`)
	send(mux, http.MethodPost, base, `{"name":"b","content":"c2"}`)

	// The framework's boolean word list is accepted where strconv's is not.
	for _, value := range []string{"yes", "no", "on", "off", "y", "n", "TRUE", "False"} {
		if rec := send(mux, http.MethodGet, base+"?active_only="+value, ""); rec.Code != http.StatusOK {
			t.Fatalf("active_only=%s: %d, want 200", value, rec.Code)
		}
	}
	// A word outside the list is the framework's bool_parsing item.
	rec := send(mux, http.MethodGet, base+"?active_only=maybe", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"type":"bool_parsing"`) {
		t.Fatalf("active_only=maybe: %d %s", rec.Code, rec.Body.String())
	}
	// A parameter present with no value is validated, not defaulted.
	for _, query := range []string{"active_only=", "limit=", "offset=", "tags_match="} {
		if rec := send(mux, http.MethodGet, base+"?"+query, ""); rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("%s: %d, want 422", query, rec.Code)
		}
	}
	// A repeated scalar parameter takes its last occurrence.
	rec = send(mux, http.MethodGet, base+"?limit=1&limit=2", "")
	var list ListResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Limit != 2 {
		t.Fatalf("repeated limit = %d, want the last occurrence 2", list.Limit)
	}
	rec = send(mux, http.MethodGet, base+"?offset=0&offset=1", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || list.Offset != 1 {
		t.Fatalf("repeated offset: %d, offset = %d, want 200 and the last occurrence 1", rec.Code, list.Offset)
	}
	// The framework's integer coercion reads the raw string and reports it.
	rec = send(mux, http.MethodGet, base+"?limit=%20abc", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"input":" abc"`) {
		t.Fatalf("limit= abc: %d %s", rec.Code, rec.Body.String())
	}
}

// An integer too large for int64 is not a validation failure: the reference
// parses it losslessly and the storage layer that cannot carry it fails, which
// the caller sees as a 500. A negative value of the same size breaks the lower
// bound instead and stays a validation item, and so does a limit, which has an
// upper bound.
func TestDirectiveListIntegerTooLargeForInt64(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/directives"
	send(mux, http.MethodPost, base, `{"name":"a","content":"c"}`)

	rec := send(mux, http.MethodGet, base+"?offset=99999999999999999999", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("huge offset: %d %s, want 500", rec.Code, rec.Body.String())
	}
	rec = send(mux, http.MethodGet, base+"?offset=-99999999999999999999", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"type":"greater_than_equal"`) {
		t.Fatalf("huge negative offset: %d %s, want 422 greater_than_equal", rec.Code, rec.Body.String())
	}
	rec = send(mux, http.MethodGet, base+"?limit=99999999999999999999", "")
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"type":"less_than_equal"`) {
		t.Fatalf("huge limit: %d %s, want 422 less_than_equal", rec.Code, rec.Body.String())
	}
}

// A body with several rejected fields reports every one of them, in the model's
// declaration order, not only the first the decoder stopped on.
func TestDirectiveCreateAccumulatesFieldErrors(t *testing.T) {
	mux, _ := newServer(t)
	rec := send(mux, http.MethodPost, "/v1/default/banks/b1/directives",
		`{"name":123,"content":4,"priority":"z","tags":[1,2]}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var failure struct {
		Detail []struct {
			Loc []any `json:"loc"`
		} `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &failure); err != nil {
		t.Fatal(err)
	}
	want := [][]any{
		{"body", "name"}, {"body", "content"}, {"body", "priority"},
		{"body", "tags", float64(0)}, {"body", "tags", float64(1)},
	}
	if len(failure.Detail) != len(want) {
		t.Fatalf("items = %+v, want %d", failure.Detail, len(want))
	}
	for i := range want {
		if !reflect.DeepEqual(failure.Detail[i].Loc, want[i]) {
			t.Fatalf("item %d loc = %v, want %v", i, failure.Detail[i].Loc, want[i])
		}
	}
}
