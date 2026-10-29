package memoryops

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/textproto"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// postWithWriteScope is post with a write scope resolved on the request, the
// way the operation middleware records one for a confined caller.
func postWithWriteScope(t *testing.T, mux *http.ServeMux, path, body string, scope []string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req = httpx.WithWriteTagScope(req, scope)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// The scopes a write names are writes too: consolidation writes its
// observations at them, so a confined caller may not send scopes it could not
// write to. The reference refuses with its "observations" sentence
// (_check_retain_writes → _refuse_unwritable_scopes): "shared" writes the
// untagged scope, and an explicit list checks only its literal entries, since
// a glob only ever picks tags the memory itself carries.
func TestRetainWriteScopeChecksObservationScopes(t *testing.T) {
	cases := []struct {
		name    string
		scope   []string
		scopes  string
		status  int
		message string
	}{
		{
			name:    "a literal scope outside the write scope is refused",
			scope:   []string{"user:dan"},
			scopes:  `[["user:kate"]]`,
			status:  http.StatusForbidden,
			message: "You can't write observations tagged user:kate",
		},
		{
			name:   "a literal scope inside the write scope is accepted",
			scope:  []string{"user:dan"},
			scopes: `[["user:dan"]]`,
			status: http.StatusOK,
		},
		{
			name:    "shared writes the untagged scope, which nobody confined may write",
			scope:   []string{"user:dan"},
			scopes:  `"shared"`,
			status:  http.StatusForbidden,
			message: "You can't write observations tagged (untagged)",
		},
		{
			name:   "a group of only globs checks nothing",
			scope:  []string{"user:dan"},
			scopes: `[["topic:*"]]`,
			status: http.StatusOK,
		},
		{
			name:    "a group written empty checks the untagged scope",
			scope:   []string{"user:dan"},
			scopes:  `[[]]`,
			status:  http.StatusForbidden,
			message: "You can't write observations tagged (untagged)",
		},
		{
			name:    "a mixed group names only the tags outside the scope, sorted",
			scope:   []string{"user:dan"},
			scopes:  `[["user:zed","user:dan","user:kate"]]`,
			status:  http.StatusForbidden,
			message: "You can't write observations tagged user:kate, user:zed",
		},
		{
			name:   "another mode name derives scopes from the item's own tags",
			scope:  []string{"user:dan"},
			scopes: `"combined"`,
			status: http.StatusOK,
		},
		{
			name:   "the same shared scope from an unconfined caller is fine",
			scope:  nil,
			scopes: `"shared"`,
			status: http.StatusOK,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, _ := newServer(t)
			body := `{"items":[{"content":"hello","tags":["user:dan"],"observation_scopes":` + tc.scopes + `}]}`
			// A nil scope is no scope at all: the request carries none, which
			// is what WithWriteTagScope cannot express (a confined empty one).
			rec := post(t, mux, "/v1/default/banks/b1/memories", body)
			if tc.scope != nil {
				rec = postWithWriteScope(t, mux, "/v1/default/banks/b1/memories", body, tc.scope)
			}
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.status, rec.Body.String())
			}
			if tc.message != "" && !strings.Contains(rec.Body.String(), tc.message) {
				t.Fatalf("body = %s, want it to name %q", rec.Body.String(), tc.message)
			}
		})
	}
}

// The guard runs on the queued path at submit time as well, so a scoped caller
// learns the refusal from the POST instead of from the operation it polls.
func TestAsyncRetainRefusesUnwritableScopesAtSubmit(t *testing.T) {
	mux, store := newServer(t)
	rec := postWithWriteScope(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"hello","tags":["user:dan"],"observation_scopes":[["user:kate"]]}],"async":true}`,
		[]string{"user:dan"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "You can't write observations tagged user:kate") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	page, err := store.ListOperations(t.Context(), storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a refused submission queued %d operations", len(page.Operations))
	}
}

// An item without scopes of its own gets the bank's default at retain, so that
// default is a write by the caller too — the reference resolves it at check
// time and refuses it the same way (memory_engine.py:3731-3736).
func TestRetainWriteScopeChecksTheDefaultScopes(t *testing.T) {
	mux, store := newServer(t)
	config := map[string]any{}
	if err := json.Unmarshal([]byte(`{"default_observation_scopes":"shared"}`), &config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBank(t.Context(), "b1", storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	rec := postWithWriteScope(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"hello","tags":["user:dan"]}]}`, []string{"user:dan"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "You can't write observations tagged (untagged)") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// An explicit scope of the caller's own overrides the default and is fine.
	rec = postWithWriteScope(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"hello","tags":["user:dan"],"observation_scopes":[["user:dan"]]}]}`, []string{"user:dan"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// default_observation_scopes is strategy-overridable (config.py lists it among
// the fields a strategy may set), so the item's strategy overlays the bank's
// config before the default is resolved — at the guard above and where the
// default is stamped. A default only the strategy carries refuses a caller the
// bank-level default would have let through.
func TestRetainWriteScopeChecksTheStrategyDefaultScopes(t *testing.T) {
	mux, store := newServer(t)
	config := map[string]any{}
	if err := json.Unmarshal([]byte(
		`{"retain_strategies":{"work":{"default_observation_scopes":"shared"}}}`), &config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBank(t.Context(), "b1", storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	rec := postWithWriteScope(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"hello","tags":["user:dan"],"strategy":"work"}]}`, []string{"user:dan"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "You can't write observations tagged (untagged)") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	// Without the strategy the overlay carries nothing, so the write proceeds.
	rec = postWithWriteScope(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"hello","tags":["user:dan"]}]}`, []string{"user:dan"})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

// applyDefaultObservationScopes resolves each item's default under the item's
// own strategy: a strategy's default_observation_scopes wins over the bank's,
// the bank's over the deployment's, and an item naming scopes of its own is
// never touched. Upstream precedence: strategy value → bank value → env →
// combined (config.py:4134, memory_engine.py:3729-3731).
func TestApplyDefaultObservationScopesStrategyOverlay(t *testing.T) {
	cases := []struct {
		name       string
		config     string
		deployment string
		items      []storage.RetainItem
		want       []string
	}{
		{
			name:   "the strategy value wins over the bank's",
			config: `{"default_observation_scopes":[["topic:*"]],"retain_strategies":{"work":{"default_observation_scopes":"shared"}}}`,
			items:  []storage.RetainItem{{Strategy: "work"}},
			want:   []string{`"shared"`},
		},
		{
			name:   "a strategy without the key leaves the bank's value",
			config: `{"default_observation_scopes":[["topic:*"]],"retain_strategies":{"work":{}}}`,
			items:  []storage.RetainItem{{Strategy: "work"}},
			want:   []string{`[["topic:*"]]`},
		},
		{
			name:   "an item without a strategy takes the bank's value",
			config: `{"default_observation_scopes":[["topic:*"]]}`,
			items:  []storage.RetainItem{{}},
			want:   []string{`[["topic:*"]]`},
		},
		{
			name:       "the deployment value applies when the bank names none",
			config:     `{}`,
			deployment: `"shared"`,
			items:      []storage.RetainItem{{}},
			want:       []string{`"shared"`},
		},
		{
			name:       "the strategy value wins over the deployment's too",
			config:     `{"retain_strategies":{"work":{"default_observation_scopes":[["user:*"]]}}}`,
			deployment: `"shared"`,
			items:      []storage.RetainItem{{Strategy: "work"}},
			want:       []string{`[["user:*"]]`},
		},
		{
			name:   "an unknown strategy changes nothing",
			config: `{"default_observation_scopes":[["bank:*"]],"retain_strategies":{"work":{"default_observation_scopes":"shared"}}}`,
			items:  []storage.RetainItem{{Strategy: "home"}},
			want:   []string{`[["bank:*"]]`},
		},
		{
			name:   "items resolve under their own strategies in one write",
			config: `{"default_observation_scopes":[["topic:*"]],"retain_strategies":{"work":{"default_observation_scopes":"shared"}}}`,
			items:  []storage.RetainItem{{Strategy: "work"}, {}, {Strategy: "work"}},
			want:   []string{`"shared"`, `[["topic:*"]]`, `"shared"`},
		},
		{
			name:   "an item with scopes of its own is untouched",
			config: `{"default_observation_scopes":[["topic:*"]]}`,
			items:  []storage.RetainItem{{ObservationScopes: json.RawMessage(`[["own"]]`)}},
			want:   []string{`[["own"]]`},
		},
		{
			name:   "an invalid value is ignored",
			config: `{"default_observation_scopes":"bogus"}`,
			items:  []storage.RetainItem{{}},
			want:   []string{""},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			config := map[string]any{}
			if err := json.Unmarshal([]byte(tc.config), &config); err != nil {
				t.Fatal(err)
			}
			h := NewHandlers(nil, 5, 30000)
			if tc.deployment != "" {
				h.DefaultObservationScopes = json.RawMessage(tc.deployment)
			}
			write := storage.RetainRequest{Items: tc.items}
			h.applyDefaultObservationScopes(&write, storage.Bank{Config: config})
			for index, want := range tc.want {
				if got := string(write.Items[index].ObservationScopes); got != want {
					t.Fatalf("item %d scopes = %q, want %q", index, got, want)
				}
			}
		})
	}
}

// The stamped default reaches the stored units: a retain whose item names no
// scopes carries the strategy's resolved default, which is what consolidation
// then merges across.
func TestRetainStampsTheStrategyDefaultScopes(t *testing.T) {
	mux, store := newServer(t)
	config := map[string]any{}
	if err := json.Unmarshal([]byte(
		`{"default_observation_scopes":[["topic:*"]],"retain_strategies":{"work":{"default_observation_scopes":"shared"}}}`), &config); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBank(t.Context(), "b1", storage.BankPatch{Config: &config}); err != nil {
		t.Fatal(err)
	}
	rec := post(t, mux, "/v1/default/banks/b1/memories",
		`{"items":[{"content":"hello world","tags":["work"],"strategy":"work"}]}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	units := store.UnitsForTest("b1")
	if len(units) == 0 {
		t.Fatalf("no units stored: %s", rec.Body.String())
	}
	for _, unit := range units {
		if got := string(unit.ObservationScopes); got != `"shared"` {
			t.Fatalf("unit scopes = %q, want %q", got, `"shared"`)
		}
	}
}

// A file retain runs the same write-scope checks at submit, before the batch
// gates: the converting worker never sees the caller's scope, so a confined
// caller must not reach another scope's tags through an upload
// (memory_engine.py:23567-23570 checks file retains like a text retain).
func TestFileRetainRefusesUnwritableTagsAtSubmit(t *testing.T) {
	mux, store := newServer(t)
	rec := postFileRetainWithWriteScope(t, mux, `{"files_metadata":[{"tags":["user:kate"]}]}`,
		[]string{"user:dan"})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403, body = %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "You can't write memories tagged user:kate") {
		t.Fatalf("body = %s", rec.Body.String())
	}
	page, err := store.ListOperations(t.Context(), storage.OperationQuery{BankID: "b1"})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Operations) != 0 {
		t.Fatalf("a refused upload queued %d operations", len(page.Operations))
	}
	// Tags inside the scope pass the guard and queue the upload as before.
	ok := postFileRetainWithWriteScope(t, mux, `{"files_metadata":[{"tags":["user:dan"]}]}`,
		[]string{"user:dan"})
	if ok.Code != http.StatusOK {
		t.Fatalf("accepted upload status = %d, body = %s", ok.Code, ok.Body.String())
	}
}

// postFileRetainWithWriteScope uploads one text file with the given request
// part, the write scope resolved on the request the way the operation
// middleware records it.
func postFileRetainWithWriteScope(t *testing.T, mux *http.ServeMux, request string, scope []string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="files"; filename="note.txt"`)
	header.Set("Content-Type", "text/plain")
	part, err := writer.CreatePart(header)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("request", request); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", &buf)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req = httpx.WithWriteTagScope(req, scope)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}
