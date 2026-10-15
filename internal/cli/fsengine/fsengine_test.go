package fsengine

// Unit tests for the mirror engine: slug and collision rules, the
// front-matter serializer matrix, path encoding, and a full sync pass
// against a local stub server exercising drift, revert, prune and failure
// behavior.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestFSSlugify(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Billing Policy", "billing-policy"},
		{"Net-30 / Terms!", "net-30-terms"},
		{"A..B", "a.b"},
		{"!!!", "untitled"},
		{"", "untitled"},
		{"  x  ", "x"},
		{"UPPER_case", "upper_case"},
		{"a b  c", "a-b-c"},
		{"2026-01-02", "2026-01-02"},
	}
	for _, tc := range cases {
		if got := Slugify(tc.in); got != tc.want {
			t.Errorf("Slugify(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFSIDSuffix(t *testing.T) {
	cases := []struct{ id, want string }{
		{"", "x"},
		{"!@#", "x"},
		{"n2", "n2"},
		{"abcdefgh", "cdefgh"},
		{"abc", "abc"},
	}
	for _, tc := range cases {
		if got := IDSuffix(tc.id); got != tc.want {
			t.Errorf("IDSuffix(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestFSPlanCollision(t *testing.T) {
	tree := &Tree{Roots: []Node{
		{ID: "aaaa1111", Kind: "page", Name: "Config"},
		{ID: "bbbb2222", Kind: "page", Name: "Config"},
		{ID: "cccc3333", Kind: "page", Name: "Config"},
		{ID: "unique1", Kind: "page", Name: "Odd"},
		{ID: "!@#", Kind: "page", Name: "Odd"},
	}}
	plan := BuildPlan(tree)
	want := map[string]string{
		"aaaa1111": "config.md",
		"bbbb2222": "config-bb2222.md",
		"cccc3333": "config-cc3333.md",
		"unique1":  "odd.md",
		"!@#":      "odd-x.md",
	}
	for id, path := range want {
		if plan.PagePaths[id] != path {
			t.Errorf("page %s path = %q, want %q", id, plan.PagePaths[id], path)
		}
	}
}

func TestFSPlanFolderNesting(t *testing.T) {
	tree := &Tree{Roots: []Node{
		{ID: "f1", Kind: "folder", Name: "Ops Guides", Children: []Node{
			{ID: "f2", Kind: "folder", Name: "Deep"},
			{ID: "p1", Kind: "page", Name: "Leaf"},
		}},
	}}
	plan := BuildPlan(tree)
	if len(plan.Dirs) != 2 || plan.Dirs[0] != "ops-guides" || plan.Dirs[1] != "ops-guides/deep" {
		t.Fatalf("dirs = %v, want parents-first nesting", plan.Dirs)
	}
	if plan.PagePaths["p1"] != "ops-guides/leaf.md" {
		t.Fatalf("page path = %q", plan.PagePaths["p1"])
	}
}

func TestFSEncodePathSegment(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a b/c+d", "a%20b%2Fc%2Bd"},
		{"A-z_0.~", "A-z_0.~"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := EncodePathSegment(tc.in); got != tc.want {
			t.Errorf("EncodePathSegment(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFSFrontMatter(t *testing.T) {
	got := FrontMatter([]FMField{
		{Key: "plain", Value: "simple"},
		{Key: "colon", Value: "a: b"},
		{Key: "hash", Value: "#tag"},
		{Key: "dash", Value: "-dash"},
		{Key: "question", Value: "?q"},
		{Key: "word-bool", Value: "TRUE"},
		{Key: "word-null", Value: "null"},
		{Key: "word-yes", Value: "Yes"},
		{Key: "word-off", Value: "off"},
		{Key: "numeric", Value: "42"},
		{Key: "signed", Value: "-7"},
		{Key: "float", Value: "3.14"},
		{Key: "mixed", Value: "1x"},
		{Key: "padded", Value: " pad "},
		{Key: "empty", Value: ""},
		{Key: "date", Value: "2026-01-02"},
		{Key: "list", Value: []string{"a", "b,c"}},
		{Key: "empty-list", Value: []string{}},
		{Key: "nothing", Value: nil},
		{Key: "count", Value: 42},
	})
	want := `---
plain: simple
colon: "a: b"
hash: "#tag"
dash: "-dash"
question: "?q"
word-bool: "TRUE"
word-null: "null"
word-yes: "Yes"
word-off: "off"
numeric: "42"
signed: "-7"
float: "3.14"
mixed: "1x"
padded: " pad "
empty: ""
date: "2026-01-02"
list: [a, "b,c"]
empty-list: []
nothing: null
count: 42
---`
	if got != want {
		t.Fatalf("front matter =\n%s\nwant\n%s", got, want)
	}
}

func TestFSPlaceholderDoc(t *testing.T) {
	ts := "2026-01-02T03:04:05.000Z"
	got := PlaceholderDoc(PagePlan{ID: "p2", Name: "Net-30 / Terms!", Tags: []string{"billing"}, Timestamp: &ts})
	want := `---
id: p2
type: knowledge-page
title: "Net-30 / Terms!"
tags: [billing]
timestamp: "2026-01-02T03:04:05.000Z"
---

_This page has not been generated yet._
`
	if got != want {
		t.Fatalf("placeholder =\n%s\nwant\n%s", got, want)
	}
	null := PlaceholderDoc(PagePlan{ID: "p3", Name: "Plain", Timestamp: nil})
	if !strings.Contains(null, "timestamp: null\n") {
		t.Fatalf("null timestamp missing:\n%s", null)
	}
}

func TestFSRenderIndex(t *testing.T) {
	plan := BuildPlan(&Tree{Roots: []Node{
		{ID: "f1", Kind: "folder", Name: "Ops", Mission: strP("Run things"), Children: []Node{
			{ID: "p1", Kind: "page", Name: "Billing", Managed: true},
		}},
	}})
	got := RenderIndex("b1", "http://127.0.0.1:8888", plan)
	for _, needle := range []string{
		"bank: b1\n", `api_url: "http://127.0.0.1:8888"`, "folders: 1", "pages: 1",
		"# Knowledge base — `b1`", "- **Ops/** — _Run things_",
		"- [`Billing`](../ops/billing.md) ·auto",
	} {
		if !strings.Contains(got, needle) {
			t.Errorf("index missing %q:\n%s", needle, got)
		}
	}
	empty := RenderIndex("b1", "http://x", BuildPlan(&Tree{}))
	if !strings.Contains(empty, "_This bank has no knowledge base yet._") {
		t.Fatalf("empty index =\n%s", empty)
	}
}

func strP(s string) *string { return &s }

// fsStubServer serves canned tree and export payloads, counts the calls in
// order and can fail the tree request on demand.
type fsStubServer struct {
	mu       sync.Mutex
	url      string
	calls    []string
	tree     string
	export   string
	failTree bool
}

func newFSStubServer(t *testing.T, tree, export string) *fsStubServer {
	t.Helper()
	st := &fsStubServer{tree: tree, export: export}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/default/banks/", func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/knowledge-base/tree"):
			st.mu.Lock()
			st.calls = append(st.calls, "tree")
			fail := st.failTree
			st.mu.Unlock()
			if fail {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"boom"}`))
				return
			}
			_, _ = w.Write([]byte(st.tree))
		case strings.HasSuffix(r.URL.Path, "/knowledge-base/export"):
			st.mu.Lock()
			st.calls = append(st.calls, "export")
			st.mu.Unlock()
			_, _ = w.Write([]byte(st.export))
		default:
			http.NotFound(w, r)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	st.url = srv.URL
	return st
}

func (s *fsStubServer) callLog() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.calls...)
}

func (s *fsStubServer) setTree(tree string) {
	s.mu.Lock()
	s.tree = tree
	s.mu.Unlock()
}

func (s *fsStubServer) setExport(export string) {
	s.mu.Lock()
	s.export = export
	s.mu.Unlock()
}

const fsTreeJSON = `{"roots":[
 {"id":"f1","kind":"folder","name":"Ops Guides","managed":false,"tags":[],"mission":"Run things","children":[
   {"id":"p1","kind":"page","name":"Billing Policy","managed":true,"tags":[]}]},
 {"id":"p2","kind":"page","name":"Net-30 / Terms!","managed":false,"tags":["billing"],"timestamp":"2026-01-02T03:04:05.000Z"}]}`

const fsExportJSON = `{"files":[
 {"path":"index.md","content":"# index"},
 {"path":"p1.md","content":"---\ntitle: Billing\n---\n\nBody text"},
 {"path":"p1.log.md","content":"log line"}]}`

func TestFSSyncEndToEnd(t *testing.T) {
	st := newFSStubServer(t, fsTreeJSON, fsExportJSON)
	dir := t.TempDir()
	f := &Fetcher{BaseURL: st.url}
	opts := Options{Dir: dir, BankID: "b1", APIURL: "http://stub", Writable: false}

	res, err := Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if res.Pages != 2 || res.Folders != 1 || res.Written != 2 || res.Unchanged != 0 || res.Removed != 0 || res.Reverted != 0 {
		t.Fatalf("first sync result = %+v", res)
	}

	// Exactly two GETs per pass, tree first.
	calls := st.callLog()
	if len(calls) != 2 || calls[0] != "tree" || calls[1] != "export" {
		t.Fatalf("calls = %v", calls)
	}

	// Page doc written verbatim with a trailing newline added, mode 0444.
	pagePath := filepath.Join(dir, "ops-guides", "billing-policy.md")
	doc, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatalf("read page: %v", err)
	}
	if string(doc) != "---\ntitle: Billing\n---\n\nBody text\n" {
		t.Fatalf("page doc = %q", string(doc))
	}
	if info, err := os.Stat(pagePath); err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("page mode = %v (err %v), want 0444", info.Mode().Perm(), err)
	}

	// Placeholder for the page the export omits.
	placeholder, err := os.ReadFile(filepath.Join(dir, "net-30-terms.md"))
	if err != nil {
		t.Fatalf("read placeholder: %v", err)
	}
	if !strings.HasPrefix(string(placeholder), "---\nid: p2\ntype: knowledge-page\ntitle: \"Net-30 / Terms!\"") {
		t.Fatalf("placeholder = %q", string(placeholder))
	}

	// Export entries index.md and *.log.md are excluded by construction.
	if _, err := os.Stat(filepath.Join(dir, "index.md")); err == nil {
		t.Fatalf("index.md leaked into the mount root")
	}
	if _, err := os.Stat(filepath.Join(dir, "p1.log.md")); err == nil {
		t.Fatalf("log file leaked into the mount root")
	}

	// state.json carries camelCase keys, sorted file keys, tracked dirs
	// and a trailing newline.
	raw, err := os.ReadFile(StatePath(dir))
	if err != nil {
		t.Fatalf("read state: %v", err)
	}
	stateText := string(raw)
	for _, needle := range []string{
		`"version": 1`, `"bankId": "b1"`, `"apiUrl"`, `"lastSyncAt"`,
		`"lastSyncOk": true`, `"lastError": null`, `"ops-guides/billing-policy.md"`,
		`"dirs": [`, `"ops-guides"`,
	} {
		if !strings.Contains(stateText, needle) {
			t.Errorf("state missing %s:\n%s", needle, stateText)
		}
	}
	if !strings.HasSuffix(stateText, "}\n") {
		t.Errorf("state lacks trailing newline")
	}

	// index.md regenerated in the control directory with the same mode.
	index, err := os.ReadFile(IndexPath(dir))
	if err != nil {
		t.Fatalf("read index: %v", err)
	}
	if !strings.Contains(string(index), "# Knowledge base — `b1`") ||
		!strings.Contains(string(index), "- [`Billing Policy`](../ops-guides/billing-policy.md) ·auto") {
		t.Fatalf("index =\n%s", string(index))
	}
	if info, _ := os.Stat(IndexPath(dir)); info.Mode().Perm() != 0o444 {
		t.Fatalf("index mode = %v", info.Mode().Perm())
	}

	// Second pass with everything unchanged.
	res, err = Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if res.Unchanged != 2 || res.Written != 0 || res.Reverted != 0 {
		t.Fatalf("second sync result = %+v", res)
	}

	// Local tampering is reverted: byte drift with an unchanged hash, and
	// the mode is re-asserted.
	target := filepath.Join(dir, "ops-guides", "billing-policy.md")
	if err := os.Chmod(target, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(target, []byte("---\ntitle: Billing\n---\n\nBody text\nTAMPERED\n"), 0o644); err != nil {
		t.Fatalf("tamper: %v", err)
	}
	res, err = Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatalf("tamper sync: %v", err)
	}
	if res.Reverted != 1 || res.Written != 0 || res.Unchanged != 1 {
		t.Fatalf("tamper sync result = %+v", res)
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o444 {
		t.Fatalf("mode not re-asserted: %v", info.Mode().Perm())
	}
	if data, _ := os.ReadFile(target); strings.Contains(string(data), "TAMPERED") {
		t.Fatalf("tampering survived a pass")
	}

	// A server-side change counts as updated, not reverted.
	st.setExport(strings.Replace(fsExportJSON, "Body text", "Body text v2", 1))
	res, err = Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatalf("update sync: %v", err)
	}
	if res.Written != 1 || res.Reverted != 0 {
		t.Fatalf("update sync result = %+v", res)
	}

	// Pruning: dropping p2 from the tree removes its file after a
	// successful fetch, and only the file.
	st.setTree(`{"roots":[
 {"id":"f1","kind":"folder","name":"Ops Guides","managed":false,"tags":[],"mission":"Run things","children":[
   {"id":"p1","kind":"page","name":"Billing Policy","managed":true,"tags":[]}]}]}`)
	res, err = Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatalf("prune sync: %v", err)
	}
	if res.Removed != 1 {
		t.Fatalf("prune sync result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(dir, "net-30-terms.md")); !os.IsNotExist(err) {
		t.Fatalf("pruned file still present")
	}
	if _, err := os.Stat(filepath.Join(dir, "ops-guides")); err != nil {
		t.Fatalf("kept folder removed: %v", err)
	}

	// A failed fetch records the failure and touches nothing. Only a
	// force-chmod edit can land, mirroring the drift the engine exists to
	// catch.
	if err := os.Chmod(pagePath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	if err := os.WriteFile(pagePath, []byte("user edit\n"), 0o644); err != nil {
		t.Fatalf("seed user edit: %v", err)
	}
	st.mu.Lock()
	st.failTree = true
	st.mu.Unlock()
	_, err = Sync(context.Background(), f, opts)
	wantErr := `Failed to fetch knowledge-base tree knowledge base for bank "b1" (HTTP 500)`
	if err == nil || !strings.HasPrefix(err.Error(), wantErr) || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("failure error = %v, want prefix %q", err, wantErr)
	}
	st.mu.Lock()
	st.failTree = false
	st.mu.Unlock()
	if data, _ := os.ReadFile(pagePath); string(data) != "user edit\n" {
		t.Fatalf("failed fetch disturbed the mirror: %q", string(data))
	}
	state := LoadState(dir, "b1", "http://stub")
	if state.LastSyncOk {
		t.Fatalf("state not marked failed")
	}
	if state.LastError == nil || !strings.Contains(*state.LastError, "HTTP 500") {
		t.Fatalf("lastError = %v", state.LastError)
	}
	if state.LastSyncAt == nil {
		t.Fatalf("failure did not stamp lastSyncAt")
	}

	// The failure path never pruned, so the stale page is still tracked
	// and a later recovery sync reverts the user edit.
	res, err = Sync(context.Background(), f, opts)
	if err != nil {
		t.Fatalf("recovery sync: %v", err)
	}
	if res.Reverted != 1 || res.Removed != 0 {
		t.Fatalf("recovery sync result = %+v", res)
	}
}

func TestFSFetchErrorMessage(t *testing.T) {
	st := newFSStubServer(t, "", "")
	st.mu.Lock()
	st.failTree = true
	st.mu.Unlock()
	f := &Fetcher{BaseURL: st.url}
	_, err := f.FetchTree(context.Background(), "b1")
	if err == nil || !strings.Contains(err.Error(), `(HTTP 500)`) {
		t.Fatalf("err = %v", err)
	}
}

func TestFSConfigRoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfg := Config{APIURL: "http://x:1/", APIToken: "tok", BankID: "b1", IntervalSeconds: 12, Writable: true}
	if err := SaveConfig(dir, cfg); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, _ := os.ReadFile(ConfigPath(dir))
	if !strings.Contains(string(raw), `"api_url"`) || !strings.Contains(string(raw), `"interval_seconds"`) {
		t.Fatalf("config keys not snake_case:\n%s", raw)
	}
	if !strings.HasSuffix(string(raw), "}\n") {
		t.Fatalf("config lacks trailing newline")
	}
	if got := LoadConfig(dir); got != cfg {
		t.Fatalf("roundtrip = %+v", got)
	}
	if LoadConfig(t.TempDir()) != (Config{}) {
		t.Fatalf("missing config not zero")
	}
}

func TestFSStateVersionGuard(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(ControlDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(StatePath(dir), []byte(`{"version": 2, "bankId": "old"}`), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	st := LoadState(dir, "b1", "http://x")
	if st.Version != 1 || st.BankID != "b1" || st.Files == nil || len(st.Files) != 0 {
		t.Fatalf("version guard failed: %+v", st)
	}
}

func TestFSPIDRecordRoundTrip(t *testing.T) {
	dir := t.TempDir()
	rec := PIDRecord{PID: 7, BankID: "b1", IntervalSeconds: 30, StartedAt: "2026-01-02T03:04:05.000Z"}
	if err := SavePID(dir, rec); err != nil {
		t.Fatalf("save: %v", err)
	}
	raw, _ := os.ReadFile(PIDPath(dir))
	for _, needle := range []string{`"pid": 7`, `"bankId"`, `"intervalSeconds"`, `"startedAt"`} {
		if !strings.Contains(string(raw), needle) {
			t.Errorf("pid file missing %s:\n%s", needle, raw)
		}
	}
	got, ok := LoadPID(dir)
	if !ok || got != rec {
		t.Fatalf("roundtrip = %+v ok=%v", got, ok)
	}
	if _, ok := LoadPID(t.TempDir()); ok {
		t.Fatalf("missing pid reported present")
	}
}

func TestFSWriteAtomicReplacesReadOnly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "page.md")
	if err := os.WriteFile(target, []byte("old"), 0o444); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := writeFileAtomic(target, []byte("new"), 0o444); err != nil {
		t.Fatalf("atomic write: %v", err)
	}
	data, _ := os.ReadFile(target)
	if string(data) != "new" {
		t.Fatalf("content = %q", string(data))
	}
	if info, _ := os.Stat(target); info.Mode().Perm() != 0o444 {
		t.Fatalf("mode = %v", info.Mode().Perm())
	}
	matches, _ := filepath.Glob(filepath.Join(dir, "*.tmp.*"))
	if len(matches) != 0 {
		t.Fatalf("temp files left behind: %v", matches)
	}
}

func TestFSRemoveFileUnlinksReadOnly(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "page.md")
	if err := os.WriteFile(target, []byte("x"), 0o444); err != nil {
		t.Fatalf("seed: %v", err)
	}
	removeFile(target)
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatalf("read-only file survived unlink")
	}
}
