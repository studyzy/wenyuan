package cli

// Shared test scaffolding for the batch-C command tests, plus the memory
// list/get/clear/delete/history/clear-observations cases. Each action is
// exercised against a local stub API that records the request; the e2e cases
// live in memory_e2e_test.go.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// capturedRequest is one recorded stub call.
type capturedRequest struct {
	Method    string
	Path      string
	RawQuery  string
	Header    http.Header
	Body      string
	BodyCount int
}

// stubAPI runs a local server whose answers come from respond. Every request
// is recorded with its body restored for later inspection.
func stubAPI(t *testing.T, respond func(call capturedRequest) (int, string)) (*httptest.Server, func() []capturedRequest) {
	t.Helper()
	var mu sync.Mutex
	var calls []capturedRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = r.Body.Close()
		call := capturedRequest{
			Method:   r.Method,
			Path:     r.URL.Path,
			RawQuery: r.URL.RawQuery,
			Header:   r.Header.Clone(),
			Body:     string(body),
		}
		mu.Lock()
		calls = append(calls, call)
		mu.Unlock()
		status, resp := respond(call)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, func() []capturedRequest {
		mu.Lock()
		defer mu.Unlock()
		return append([]capturedRequest(nil), calls...)
	}
}

// commandResult carries one CLI invocation's streams and error.
type commandResult struct {
	stdout string
	stderr string
	err    error
}

// runCommand resolves and executes one invocation against a registry carrying
// every batch-C family, with the client pointed at baseURL.
func runCommand(t *testing.T, baseURL string, format Format, args []string, stdin io.Reader) commandResult {
	t.Helper()
	// Global flags are extracted by main before resolution; mirror that here.
	verbose := false
	filtered := make([]string, 0, len(args))
	for _, a := range args {
		if a == "-v" || a == "--verbose" {
			verbose = true
			continue
		}
		filtered = append(filtered, a)
	}
	registry := NewRegistry()
	RegisterMemoryCommands(registry)
	RegisterReadCommands(registry)
	cmd, rest, err := registry.Resolve(filtered)
	if err != nil {
		return commandResult{err: err}
	}
	positionals, err := cmd.ParseArgs(rest)
	if err != nil {
		return commandResult{err: err}
	}
	var stdout, stderr bytes.Buffer
	deps := &Deps{
		Ctx:     context.Background(),
		Args:    positionals,
		Flags:   cmd.Flags(),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Stdin:   stdin,
		Format:  format,
		Verbose: verbose,
		Config:  Config{APIURL: baseURL},
		Client:  NewClient(baseURL, ""),
		UI:      NewUI(&stdout, &stderr, ColorEnv{}),
	}
	err = cmd.Execute(deps.Ctx, deps)
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

func TestMemoryList(t *testing.T) {
	const page = `{"items":[{"id":"m1","text":"the deployment window opens at dawn and the release train leaves without waiting","fact_type":"world","document_id":"d1"},{"id":"m2","text":"short","fact_type":null}],"total":42,"limit":2,"offset":3}`
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, page
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{
		"memory", "list", "b1", "-t", "world", "-q", "find it", "-l", "7", "-s", "3",
	}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	call := calls()[0]
	if call.Method != "GET" || call.Path != "/v1/default/banks/b1/memories/list" {
		t.Fatalf("request = %s %s", call.Method, call.Path)
	}
	if call.RawQuery != "limit=7&offset=3&q=find+it&type=world" {
		t.Fatalf("query = %q", call.RawQuery)
	}
	for _, want := range []string{
		"Memories: b1 (showing 4-5)",
		"[WORLD]",
		"m1",
		"the deployment window opens at dawn and the release train leaves without waiting",
		"doc: d1",
		"Total: 42 total",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	if strings.Contains(r.stdout, "[NULL]") {
		t.Errorf("null fact type must not render NULL:\n%s", r.stdout)
	}
	// Long text keeps every character under the limit; the truncation is
	// pinned in output_test.go via Truncate.
}

func TestMemoryListEmptyPage(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"items":[],"total":0,"limit":100,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "list", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "Memories: b1 (showing 1-0)") {
		t.Errorf("header missing:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "No memories found.") {
		t.Errorf("empty marker missing:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "Total:") {
		t.Errorf("footer must not render on an empty page:\n%s", r.stdout)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}
}

func TestMemoryListMachineOutputIsRawResponse(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"items":[{"id":"m1","fact_type":"world"}],"total":1,"limit":100,"offset":0}`
	})
	r := runCommand(t, srv.URL, FormatJSON, []string{"memory", "list", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "\"fact_type\"") {
		t.Errorf("json output must carry the list item's fact_type key:\n%s", r.stdout)
	}
	if strings.Contains(r.stdout, "[WORLD]") {
		t.Errorf("pretty badges leak into json output:\n%s", r.stdout)
	}
}

func TestMemoryGet(t *testing.T) {
	const detail = `{"id":"m1","text":"full body text","context":"a context","type":"world","occurred_start":"2024-01-01T00:00:00Z","occurred_end":"2024-01-02T00:00:00Z","entities":["Alice","Bob Inc"],"document_id":"d1","tags":["one","two"],"metadata":{"x":1}}`
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, detail
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "get", "b1", "m1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0]; got.Method != "GET" || got.Path != "/v1/default/banks/b1/memories/m1" {
		t.Fatalf("request = %s %s", got.Method, got.Path)
	}
	for _, want := range []string{
		"Memory: m1",
		"  Type: [WORLD]",
		"  ID: m1",
		"  Document: d1",
		"  Context: a context",
		"─── Content ───",
		"full body text",
		"─── Temporal ───",
		"  Start: 2024-01-01T00:00:00Z",
		"  End: 2024-01-02T00:00:00Z",
		"─── Entities ───",
		"  • Alice",
		"  • Bob Inc",
		"─── Tags ───",
		"  one, two",
	} {
		if !strings.Contains(r.stdout, want) {
			t.Errorf("stdout missing %q:\n%s", want, r.stdout)
		}
	}
	// Metadata is never rendered in pretty mode.
	if strings.Contains(r.stdout, "metadata") {
		t.Errorf("metadata leaked:\n%s", r.stdout)
	}
}

func TestMemoryGetMachineOutputPassesDetailThrough(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"id":"m1","type":null,"entities":[]}`
	})
	r := runCommand(t, srv.URL, FormatYAML, []string{"memory", "get", "b1", "m1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "id: m1") {
		t.Errorf("yaml output:\n%s", r.stdout)
	}
}

func TestMemoryClearConfirmation(t *testing.T) {
	cases := []struct {
		name       string
		format     Format
		args       []string
		stdin      string
		wantMethod string
		wantQuery  string
		wantOut    string
		wantCalls  int
	}{
		{
			name:       "typed clear confirmed",
			args:       []string{"memory", "clear", "b1", "-t", "world"},
			stdin:      "y\n",
			wantMethod: "DELETE",
			wantQuery:  "type=world",
			wantOut:    "Memories cleared successfully",
			wantCalls:  1,
		},
		{
			name:       "full clear confirmed with yes",
			args:       []string{"memory", "clear", "b1", "-y"},
			wantMethod: "DELETE",
			wantQuery:  "",
			wantOut:    "All memories cleared successfully",
			wantCalls:  1,
		},
		{
			name:      "declined sends nothing",
			args:      []string{"memory", "clear", "b1"},
			stdin:     "n\n",
			wantOut:   "Operation cancelled",
			wantCalls: 0,
		},
		{
			name:      "eof declines",
			args:      []string{"memory", "clear", "b1"},
			wantOut:   "Operation cancelled",
			wantCalls: 0,
		},
		{
			name:       "machine format sends without prompting",
			format:     FormatJSON,
			args:       []string{"memory", "clear", "b1"},
			wantMethod: "DELETE",
			wantQuery:  "",
			wantOut:    "deleted_count",
			wantCalls:  1,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
				return 200, `{"success":true,"message":"Cleared 3 memory unit(s) from bank 'b1'","deleted_count":3}`
			})
			var stdin io.Reader
			if tc.stdin != "" {
				stdin = strings.NewReader(tc.stdin)
			}
			r := runCommand(t, srv.URL, tc.format, tc.args, stdin)
			if r.err != nil {
				t.Fatalf("err = %v", r.err)
			}
			got := calls()
			if len(got) != tc.wantCalls {
				t.Fatalf("calls = %d, want %d", len(got), tc.wantCalls)
			}
			if tc.wantCalls == 1 {
				if got[0].Method != tc.wantMethod || got[0].Path != "/v1/default/banks/b1/memories" {
					t.Fatalf("request = %s %s", got[0].Method, got[0].Path)
				}
				if got[0].RawQuery != tc.wantQuery {
					t.Fatalf("query = %q, want %q", got[0].RawQuery, tc.wantQuery)
				}
			}
			if tc.wantOut != "" && !strings.Contains(r.stdout, tc.wantOut) {
				t.Errorf("stdout missing %q:\n%s", tc.wantOut, r.stdout)
			}
		})
	}
}

func TestMemoryClearRejectsUnknownFactType(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "clear", "b1", "-t", "opinion"}, nil)
	if r.err == nil {
		t.Fatal("expected a usage error")
	}
	if !strings.Contains(r.err.Error(), "--fact-type must be one of world, experience, observation") {
		t.Errorf("err = %v", r.err)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("requests = %d, want none before any request", len(got))
	}
}

func TestMemoryClearFailureBody(t *testing.T) {
	srv, _ := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `{"success":false}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "clear", "b1", "-y"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stderr, "Failed to clear memories") {
		t.Errorf("stderr:\n%s", r.stderr)
	}
}

func TestMemoryDeleteRefusesClientSide(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		t.Error("memory delete must never send a request")
		return 200, `{}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "delete", "b1", "m1"}, nil)
	if r.err == nil {
		t.Fatal("expected the refusal error")
	}
	const want = "Individual memory deletion is no longer supported. Use 'memory clear' to clear all memories."
	if r.err.Error() != want {
		t.Errorf("err = %q, want %q", r.err.Error(), want)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("requests = %d, want 0", len(got))
	}
}

func TestMemoryHistoryRendersBareArray(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		return 200, `[{"action":"created","at":"2024-01-01T00:00:00Z"}]`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "history", "b1", "m1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if got := calls()[0]; got.Path != "/v1/default/banks/b1/memories/m1/history" {
		t.Fatalf("path = %s", got.Path)
	}
	if !strings.Contains(r.stdout, "\"action\": \"created\"") {
		t.Errorf("pretty output pretty-prints the array:\n%s", r.stdout)
	}
}

func TestMemoryClearObservations(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		if call.Method != "DELETE" || call.Path != "/v1/default/banks/b1/memories/m1/observations" {
			t.Errorf("unexpected request %s %s", call.Method, call.Path)
		}
		return 200, `{"cleared":true}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "clear-observations", "b1", "m1"}, strings.NewReader("yes\n"))
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "Cleared observations for memory 'm1'") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	if !strings.Contains(r.stdout, "\"cleared\": true") {
		t.Errorf("body not pretty-printed:\n%s", r.stdout)
	}
	if got := calls(); len(got) != 1 {
		t.Fatalf("calls = %d", len(got))
	}
}

func TestMemoryClearObservationsDeclined(t *testing.T) {
	srv, calls := stubAPI(t, func(call capturedRequest) (int, string) {
		t.Error("declined clear-observations must not send a request")
		return 200, `{}`
	})
	r := runCommand(t, srv.URL, FormatPretty, []string{"memory", "clear-observations", "b1", "m1"}, strings.NewReader("\n"))
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	if !strings.Contains(r.stdout, "Operation cancelled") {
		t.Errorf("stdout:\n%s", r.stdout)
	}
	if got := calls(); len(got) != 0 {
		t.Fatalf("calls = %d", len(got))
	}
}
