package cli

// Tests for the knowledge-base command family. Every action runs against a
// local stub API that records the request; assertions cover the method,
// path, query, body fields and the pretty rendering.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// runKB resolves and executes one knowledge-base invocation with the client
// pointed at baseURL.
func runKB(t *testing.T, baseURL string, format Format, args []string, stdin io.Reader) commandResult {
	t.Helper()
	registry := NewRegistry()
	RegisterKBCommands(registry)
	cmd, rest, err := registry.Resolve(args)
	if err != nil {
		return commandResult{err: err}
	}
	positionals, err := cmd.ParseArgs(rest)
	if err != nil {
		return commandResult{err: err}
	}
	return executeKB(t, baseURL, format, cmd, positionals, stdin)
}

func executeKB(t *testing.T, baseURL string, format Format, cmd *SubCmd, positionals []string, stdin io.Reader) commandResult {
	t.Helper()
	var stdout, stderr strings.Builder
	deps := &Deps{
		Ctx:    context.Background(),
		Args:   positionals,
		Flags:  cmd.Flags(),
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  stdin,
		Format: format,
		Config: Config{APIURL: baseURL},
		Client: NewClient(baseURL, ""),
		UI:     NewUI(&stdout, &stderr, ColorEnv{}),
	}
	err := cmd.Execute(deps.Ctx, deps)
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func TestKBTree(t *testing.T) {
	const tree = `{"roots":[
	 {"id":"f1","kind":"folder","name":"Ops","managed":false,"tags":[],"children":[
	  {"id":"p1","kind":"page","name":"Billing","managed":true,"tags":[],"is_stale":true}]}]}`
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Method != "GET" || call.Path != "/v1/default/banks/b1/knowledge-base/tree" {
			t.Errorf("request = %s %s", call.Method, call.Path)
		}
		return 200, tree
	})

	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "tree", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "Knowledge Base: b1")
	wantContains(t, r.stdout, "  f1 Ops/")
	wantContains(t, r.stdout, "    p1 Billing (stale)")

	// Filters: repeated tags params, lowercased match mode, verbatim groups.
	// The flags run is the second invocation, so index the second call.
	r = runKB(t, srv.URL, FormatPretty, []string{
		"knowledge-base", "tree", "b1", "--tags", "a,b", "--tags-match", "ALL", "--tag-groups", `["g"]`,
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	query := calls()[1].RawQuery
	for _, want := range []string{"tags=a", "tags=b", "tags_match=all", "tag_groups="} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q missing %q", query, want)
		}
	}

	// Unknown match mode is a CLI error before any traffic.
	r = runKB(t, srv.URL, FormatPretty, []string{
		"knowledge-base", "tree", "b1", "--tags-match", "weird",
	}, nil)
	if r.err == nil || !strings.Contains(r.err.Error(), "--tags-match") {
		t.Fatalf("err = %v", r.err)
	}

	// Empty tree message.
	srv2, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"roots":[]}`
	})
	r = runKB(t, srv2.URL, FormatPretty, []string{"knowledge-base", "tree", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "No folders or pages found.")

	// Machine formats pass the payload through.
	r = runKB(t, srv.URL, FormatJSON, []string{"knowledge-base", "tree", "b1"}, nil)
	if r.err != nil || !strings.Contains(r.stdout, `"roots"`) {
		t.Fatalf("json = %q err=%v", r.stdout, r.err)
	}
}

func TestKBPathEncoding(t *testing.T) {
	// The escaped wire path matters: r.URL.Path arrives decoded at the
	// server, so the stub captures EscapedPath directly.
	var escaped string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		escaped = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"roots":[]}`))
	}))
	t.Cleanup(srv.Close)

	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "tree", "a b/c+d"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if escaped != "/v1/default/banks/a%20b%2Fc%2Bd/knowledge-base/tree" {
		t.Fatalf("escaped path = %q", escaped)
	}
}

func TestKBCreateFolder(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 201, `{"id":"f9","kind":"folder","name":"New","managed":false,"tags":[],"children":[]}`
	})
	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "create-folder", "b1", "New"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	call := calls()[0]
	if call.Method != "POST" || call.Path != "/v1/default/banks/b1/knowledge-base/folders" {
		t.Fatalf("request = %s %s", call.Method, call.Path)
	}
	if call.Body != `{"name":"New"}` {
		t.Fatalf("body = %q, parent_id must be omitted", call.Body)
	}
	wantContains(t, r.stdout, "Folder created: f9")

	r = runKB(t, srv.URL, FormatPretty, []string{
		"knowledge-base", "create-folder", "b1", "New", "--parent-id", "f1",
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if call := calls()[1]; call.Body != `{"name":"New","parent_id":"f1"}` {
		t.Fatalf("body = %q", call.Body)
	}
}

func TestKBCreatePageTriggerRule(t *testing.T) {
	const created = `{"page_id":"p9","mental_model_id":"m9","operation_id":"op1"}`
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 201, created
	})
	base := []string{"knowledge-base", "create-page", "b1", "Title", "question?"}

	// No override flags: no trigger field at all.
	r := runKB(t, srv.URL, FormatPretty, base, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if strings.Contains(calls()[0].Body, "trigger") {
		t.Fatalf("body = %q, trigger must be omitted", calls()[0].Body)
	}

	// --mode alone restates the default fact types.
	r = runKB(t, srv.URL, FormatPretty, append(append([]string{}, base...), "--mode", "DELTA"), nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if calls()[1].Body != `{"name":"Title","source_query":"question?","trigger":{"mode":"delta","fact_types":["observation"],"refresh_after_consolidation":true,"exclude_mental_models":true}}` {
		t.Fatalf("body = %q", calls()[1].Body)
	}

	// --fact-types alone restates the default mode.
	r = runKB(t, srv.URL, FormatPretty, append(append([]string{}, base...), "--fact-types", "World,EXPERIENCE"), nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if calls()[2].Body != `{"name":"Title","source_query":"question?","trigger":{"mode":"delta","fact_types":["world","experience"],"refresh_after_consolidation":true,"exclude_mental_models":true}}` {
		t.Fatalf("body = %q", calls()[2].Body)
	}

	// Optional fields pass through.
	r = runKB(t, srv.URL, FormatPretty, append(append([]string{}, base...),
		"--parent-id", "f1", "--tags", "x,y", "--max-tokens", "77"), nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if calls()[3].Body != `{"name":"Title","source_query":"question?","parent_id":"f1","tags":["x","y"],"max_tokens":77}` {
		t.Fatalf("body = %q", calls()[3].Body)
	}

	// Value validation happens before any traffic.
	for _, bad := range []struct{ args, msg []string }{
		{[]string{"--mode", "weird"}, []string{"--mode"}},
		{[]string{"--fact-types", "world,nope"}, []string{"--fact-types"}},
	} {
		r = runKB(t, srv.URL, FormatPretty, append(append([]string{}, base...), bad.args...), nil)
		if r.err == nil || !strings.Contains(r.err.Error(), bad.msg[0]) {
			t.Errorf("args %v: err = %v", bad.args, r.err)
		}
	}
	n := len(calls())
	if n != 4 {
		t.Fatalf("invalid values reached the API: %d calls", n)
	}

	// Pretty rendering with the background-operation hint.
	r = runKB(t, srv.URL, FormatPretty, base, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "Page created: p9")
	wantContains(t, r.stdout, "  Operation: op1")
	wantContains(t, r.stdout, "Content is generated in the background. Use 'wenyuan-admin operation get' to track it.")

	// Machine formats pass the payload through.
	r = runKB(t, srv.URL, FormatJSON, base, nil)
	if !strings.Contains(r.stdout, `"page_id"`) {
		t.Fatalf("json = %q", r.stdout)
	}
}

func TestKBGetPage(t *testing.T) {
	const page = `{"id":"p1","name":"Billing","type":"knowledge-page","description":"What?","timestamp":"2026-01-02T03:04:05.000Z","tags":["a","b"],"body":"Line one.\nLine two."}`
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Path != "/v1/default/banks/b1/knowledge-base/pages/p1" {
			t.Errorf("path = %s", call.Path)
		}
		return 200, page
	})
	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "get-page", "b1", "p1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	for _, want := range []string{
		"Billing", "  ID: p1", "  Type: knowledge-page", "  Question: What?",
		"  Last refresh: 2026-01-02T03:04:05.000Z", "  Tags: a, b", "Line one.", "Line two.",
	} {
		wantContains(t, r.stdout, want)
	}
	if len(calls()) != 1 {
		t.Fatalf("calls = %d", len(calls()))
	}

	// Optional fields disappear when absent.
	srv2, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"id":"p1","name":"Bare","type":"knowledge-page","tags":[]}`
	})
	r = runKB(t, srv2.URL, FormatPretty, []string{"knowledge-base", "get-page", "b1", "p1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if strings.Contains(r.stdout, "Question:") || strings.Contains(r.stdout, "Tags:") {
		t.Fatalf("optional fields rendered: %q", r.stdout)
	}
}

func TestKBSearch(t *testing.T) {
	const hits = `{"results":[
	  {"id":"p1","name":"Billing","score":0.5,"snippet":"a long snippet that goes well past one hundred characters so the ellipsis path gets exercised properly in this test case here"},
	  {"id":"p2","name":"Terms","score":0.25,"snippet":"short"}],
	 "total":2}`
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, hits
	})
	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "search", "b1", "billing", "--limit", "7"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "Search Results: b1")
	wantContains(t, r.stdout, "  p1 Billing (0.500)")
	wantContains(t, r.stdout, "    ")
	if !strings.Contains(r.stdout, "...") {
		t.Fatalf("long snippet not truncated: %q", r.stdout)
	}
	wantContains(t, r.stdout, "    short")
	query := calls()[0].RawQuery
	for _, want := range []string{"limit=7", "q=billing"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q missing %q", query, want)
		}
	}

	// An omitted --limit leaves the server default in place.
	srvNoLimit, callsNoLimit := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, hits
	})
	if r := runKB(t, srvNoLimit.URL, FormatPretty, []string{"knowledge-base", "search", "b1", "q"}, nil); r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if q := callsNoLimit()[0].RawQuery; strings.Contains(q, "limit=") {
		t.Errorf("unset --limit must be omitted, query = %q", q)
	}

	// Client-side validation errors.
	r = runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "search", "b1", ""}, nil)
	if r.err == nil {
		t.Fatalf("empty query accepted")
	}
	r = runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "search", "b1", "q", "--limit", "0"}, nil)
	if r.err == nil || r.err.Error() != "invalid --limit 0: expected a value between 1 and 50" {
		t.Fatalf("limit err = %v", r.err)
	}

	// No hits.
	srv2, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"results":[],"total":0}`
	})
	r = runKB(t, srv2.URL, FormatPretty, []string{"knowledge-base", "search", "b1", "q"}, nil)
	wantContains(t, r.stdout, "  No pages matched.")

	// Machine formats.
	r = runKB(t, srv.URL, FormatJSON, []string{"knowledge-base", "search", "b1", "q"}, nil)
	if !strings.Contains(r.stdout, `"total": 2`) {
		t.Fatalf("json = %q", r.stdout)
	}
}

func TestKBUpdate(t *testing.T) {
	const node = `{"id":"n1","kind":"page","name":"Renamed","managed":true,"tags":[],"children":[]}`
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Method != "PATCH" || call.Path != "/v1/default/banks/b1/knowledge-base/nodes/n1" {
			t.Errorf("request = %s %s", call.Method, call.Path)
		}
		return 200, node
	})

	// No flags is a CLI error.
	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "update", "b1", "n1"}, nil)
	if r.err == nil || !strings.Contains(r.err.Error(), "at least one") {
		t.Fatalf("err = %v", r.err)
	}

	r = runKB(t, srv.URL, FormatPretty, []string{
		"knowledge-base", "update", "b1", "n1", "--name", "Renamed", "--max-tokens", "9",
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if calls()[0].Body != `{"name":"Renamed","max_tokens":9}` {
		t.Fatalf("body = %q", calls()[0].Body)
	}
	wantContains(t, r.stdout, "Node 'n1' updated successfully")
	wantContains(t, r.stdout, "  n1 Renamed")

	// Unsupplied fields stay out of the body.
	r = runKB(t, srv.URL, FormatPretty, []string{
		"knowledge-base", "update", "b1", "n1", "--parent-id", "f2", "--tags", "x", "--source-query", "sq",
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if calls()[1].Body != `{"parent_id":"f2","source_query":"sq","tags":["x"]}` {
		t.Fatalf("body = %q", calls()[1].Body)
	}
}

func TestKBDelete(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Method != "DELETE" || call.Path != "/v1/default/banks/b1/knowledge-base/nodes/n1" {
			t.Errorf("request = %s %s", call.Method, call.Path)
		}
		return 200, `{"status":"deleted"}`
	})

	// Declining the prompt sends nothing.
	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "delete", "b1", "n1"},
		strings.NewReader("n\n"))
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "Are you sure you want to delete 'n1' and everything under it? This cannot be undone.")
	wantContains(t, r.stdout, "Operation cancelled")
	if len(calls()) != 0 {
		t.Fatalf("decline reached the API")
	}

	// Accepting deletes and reports.
	r = runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "delete", "b1", "n1"},
		strings.NewReader("y\n"))
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "Node 'n1' deleted successfully")

	// --yes skips the prompt.
	r = runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "delete", "b1", "n1", "-y"}, nil)
	if r.err != nil || strings.Contains(r.stdout, "Are you sure") {
		t.Fatalf("-y output = %q err=%v", r.stdout, r.err)
	}

	// Machine formats print the CLI's own literal, not the server body.
	r = runKB(t, srv.URL, FormatJSON, []string{"knowledge-base", "delete", "b1", "n1"}, nil)
	if r.err != nil || strings.TrimSpace(r.stdout) != `{"success": true}` {
		t.Fatalf("json output = %q err=%v", r.stdout, r.err)
	}
	r = runKB(t, srv.URL, FormatYAML, []string{"knowledge-base", "delete", "b1", "n1"}, nil)
	if strings.TrimSpace(r.stdout) != `{"success": true}` {
		t.Fatalf("yaml output = %q", r.stdout)
	}
}

func TestKBExport(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"files":[{"path":"index.md","content":"x"},{"path":"p1.md","content":"y"}]}`
	})
	r := runKB(t, srv.URL, FormatPretty, []string{"knowledge-base", "export", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "Knowledge Base Bundle: b1")
	wantContains(t, r.stdout, "  index.md")
	wantContains(t, r.stdout, "  p1.md")
	wantContains(t, r.stdout, "Use --output json to get the file contents, or 'wenyuan-admin fs mount' to mirror them to disk.")
	if calls()[0].Path != "/v1/default/banks/b1/knowledge-base/export" {
		t.Fatalf("path = %s", calls()[0].Path)
	}

	r = runKB(t, srv.URL, FormatJSON, []string{"knowledge-base", "export", "b1"}, nil)
	if !strings.Contains(r.stdout, `"files"`) {
		t.Fatalf("json = %q", r.stdout)
	}
}
