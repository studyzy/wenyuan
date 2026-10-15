package cli

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// dRequest captures one request a dStub server received.
type dRequest struct {
	Method string
	Path   string
	Query  string
	Body   string
}

// dStub is a canned-response HTTP server that records every request.
type dStub struct {
	Server *httptest.Server
	mu     sync.Mutex
	got    []dRequest
}

// newDstub starts a stub server answering every request with the given status
// and body; a zero status means 200.
func newDstub(t *testing.T, status int, body string) *dStub {
	t.Helper()
	if status == 0 {
		status = 200
	}
	s := &dStub{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.got = append(s.got, dRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(data)})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(s.Server.Close)
	return s
}

// requests returns the captured requests in arrival order.
func (s *dStub) requests() []dRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dRequest(nil), s.got...)
}

// runDCommandVerbose resolves args against a registry holding all batch D
// families and executes the resolved command against the stub.
func runDCommandVerbose(t *testing.T, stub *dStub, format Format, stdin string, verbose bool, args ...string) (string, string, error) {
	t.Helper()
	registry := NewRegistry()
	RegisterOperationFamily(registry)
	RegisterMentalModelFamily(registry)
	RegisterDirectiveFamily(registry)
	RegisterWebhookFamily(registry)
	RegisterAuditFamily(registry)
	cmd, rest, err := registry.Resolve(args)
	if err != nil {
		return "", "", err
	}
	positionals, err := cmd.ParseArgs(rest)
	if err != nil {
		return "", "", err
	}
	var out, errBuf strings.Builder
	deps := &Deps{
		Ctx:     context.Background(),
		Args:    positionals,
		Flags:   cmd.Flags(),
		Stdout:  &out,
		Stderr:  &errBuf,
		Stdin:   strings.NewReader(stdin),
		Format:  format,
		Verbose: verbose,
		UI:      NewUI(&out, &errBuf, ColorEnv{Term: "dumb"}),
	}
	if stub != nil {
		deps.Client = &Client{BaseURL: stub.Server.URL, HTTP: stub.Server.Client()}
	}
	err = cmd.Execute(deps.Ctx, deps)
	return out.String(), errBuf.String(), err
}

// runDCommand runs a command in non-verbose mode.
func runDCommand(t *testing.T, stub *dStub, format Format, stdin string, args ...string) (string, string, error) {
	t.Helper()
	return runDCommandVerbose(t, stub, format, stdin, false, args...)
}

// wantUsage reports whether err is a usage error carrying want text.
func wantUsage(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a usage error, got nil")
	}
	var usage *UsageError
	if !errors.As(err, &usage) {
		t.Fatalf("expected a usage error, got %T: %v", err, err)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("usage error %q does not contain %q", err.Error(), want)
	}
}

// wantCommand reports whether err is a command-body error carrying want text.
// These exit 1 where a usage error exits 2.
func wantCommand(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected a command error, got nil")
	}
	var command *CommandError
	if !errors.As(err, &command) {
		t.Fatalf("expected a command error, got %T: %v", err, err)
	}
	if want != "" && !strings.Contains(err.Error(), want) {
		t.Fatalf("command error %q does not contain %q", err.Error(), want)
	}
}

// TestOperationCommands walks the operation surface: methods, paths, bodies
// and pretty rendering for every action.
func TestOperationCommands(t *testing.T) {
	tests := []struct {
		name      string
		args      []string
		stdin     string
		format    Format
		status    int
		body      string
		wantCalls int
		want      dRequest
		out       []string
		errOut    []string
		wantUsage bool
		usageText string
	}{
		{
			name: "list renders one block per operation",
			args: []string{"operation", "list", "b1"},
			body: `{"bank_id":"b1","total":2,"limit":50,"offset":0,"operations":[` +
				`{"id":"op-1","task_type":"consolidation","operation_id":null,"operation_type":null,"items_count":3,` +
				`"document_id":"d-1","filename":"f.pdf","mental_model_id":null,"details":null,` +
				`"created_at":"2026-01-01T00:00:00Z","updated_at":null,"status":"completed","error_message":null,` +
				`"retry_count":0,"next_retry_at":null,"progress":null},` +
				`{"id":"op-2","task_type":"reflect","operation_id":null,"operation_type":null,"items_count":0,` +
				`"document_id":null,"filename":null,"mental_model_id":null,"details":null,` +
				`"created_at":"2026-01-02T00:00:00Z","updated_at":null,"status":"pending","error_message":null,` +
				`"retry_count":0,"next_retry_at":null,"progress":null}]}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/operations"},
			out: []string{
				"Found 2 operation(s)",
				"  Operation ID: op-1",
				"    Type: consolidation",
				"    Status: completed",
				"    Items: 3",
				"    Filename: f.pdf",
				"    Document ID: d-1",
				"  Operation ID: op-2",
				"    Status: pending",
			},
		},
		{
			name:      "list sends no query parameters",
			args:      []string{"operation", "list", "b1"},
			body:      `{"bank_id":"b1","total":0,"limit":50,"offset":0,"operations":[]}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/operations", Query: ""},
			out:       []string{"No operations found"},
		},
		{
			name:      "get renders status and error",
			args:      []string{"operation", "get", "b1", "op-1"},
			body:      `{"operation_id":"op-1","status":"failed","error_message":"boom"}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/operations/op-1"},
			out:       []string{"Operation: op-1", "  Status: failed", "  Error: boom"},
		},
		{
			name:      "get unknown id renders not_found, not an error",
			args:      []string{"operation", "get", "b1", "op-9"},
			body:      `{"operation_id":"op-9","status":"not_found"}`,
			wantCalls: 1,
			want:      dRequest{Method: "GET", Path: "/v1/default/banks/b1/operations/op-9"},
			out:       []string{"Operation: op-9", "  Status: not_found"},
		},
		{
			name:      "cancel is a DELETE on the member without confirmation",
			args:      []string{"operation", "cancel", "b1", "op-1"},
			body:      `{"success":true,"message":"operation cancelled","operation_id":"op-1"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/operations/op-1"},
			out:       []string{"Operation cancelled successfully"},
		},
		{
			name:      "retry posts to the retry route with no body",
			args:      []string{"operation", "retry", "b1", "op-1"},
			body:      `{"success":true,"message":"operation requeued","operation_id":"op-1"}`,
			wantCalls: 1,
			want:      dRequest{Method: "POST", Path: "/v1/default/banks/b1/operations/op-1/retry", Body: ""},
			out:       []string{"Operation 'op-1' retried", "operation requeued"},
		},
		{
			name:      "delete confirms then hits the /delete route",
			args:      []string{"operation", "delete", "b1", "op-1"},
			stdin:     "y\n",
			body:      `{"success":true,"message":"Operation op-1 deleted","operation_id":"op-1"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/operations/op-1/delete"},
			out: []string{
				"Are you sure you want to permanently delete operation 'op-1'? This cannot be undone.",
				"Operation op-1 deleted",
			},
		},
		{
			name:      "delete declined makes no HTTP call",
			args:      []string{"operation", "delete", "b1", "op-1"},
			stdin:     "n\n",
			wantCalls: 0,
			out:       []string{"Operation cancelled"},
		},
		{
			name:      "delete skips the prompt with --yes",
			args:      []string{"operation", "delete", "b1", "op-1", "--yes"},
			body:      `{"success":true,"message":"Operation op-1 deleted","operation_id":"op-1"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/operations/op-1/delete"},
			out:       []string{"Operation op-1 deleted"},
		},
		{
			name:      "delete in json mode skips the prompt",
			args:      []string{"operation", "delete", "b1", "op-1"},
			format:    FormatJSON,
			body:      `{"success":true,"message":"Operation op-1 deleted","operation_id":"op-1"}`,
			wantCalls: 1,
			want:      dRequest{Method: "DELETE", Path: "/v1/default/banks/b1/operations/op-1/delete"},
			out:       []string{`"success": true`},
		},
		{
			name:      "get requires two positionals",
			args:      []string{"operation", "get", "b1"},
			wantUsage: true,
			usageText: "operation get requires",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := newDstub(t, tt.status, tt.body)
			stdout, stderr, err := runDCommand(t, stub, tt.format, tt.stdin, tt.args...)
			if tt.wantUsage {
				wantUsage(t, err, tt.usageText)
				return
			}
			if err != nil {
				t.Fatalf("execute: %v", err)
			}
			got := stub.requests()
			if len(got) != tt.wantCalls {
				t.Fatalf("requests = %d, want %d: %+v", len(got), tt.wantCalls, got)
			}
			if tt.wantCalls == 1 {
				r := got[0]
				if r.Method != tt.want.Method || r.Path != tt.want.Path || r.Query != tt.want.Query || r.Body != tt.want.Body {
					t.Fatalf("request = %+v, want %+v", r, tt.want)
				}
			}
			for _, sub := range tt.out {
				if !strings.Contains(stdout, sub) {
					t.Errorf("stdout missing %q\ngot:\n%s", sub, stdout)
				}
			}
			for _, sub := range tt.errOut {
				if !strings.Contains(stderr, sub) {
					t.Errorf("stderr missing %q\ngot:\n%s", sub, stderr)
				}
			}
		})
	}
}

// TestOperationListJSON pins the machine-mode envelope.
func TestOperationListJSON(t *testing.T) {
	body := `{"bank_id":"b1","total":1,"limit":50,"offset":0,"operations":[` +
		`{"id":"op-1","task_type":"consolidation","operation_id":null,"operation_type":null,"items_count":2,` +
		`"document_id":null,"filename":null,"mental_model_id":null,"details":null,` +
		`"created_at":"2026-01-01T00:00:00Z","updated_at":null,"status":"completed","error_message":null,` +
		`"retry_count":0,"next_retry_at":null,"progress":null}]}`
	stub := newDstub(t, 200, body)
	stdout, _, err := runDCommand(t, stub, FormatJSON, "", "operation", "list", "b1")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got struct {
		BankID     string           `json:"bank_id"`
		Total      int              `json:"total"`
		Operations []map[string]any `json:"operations"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if got.BankID != "b1" || got.Total != 1 || len(got.Operations) != 1 {
		t.Fatalf("unexpected envelope: %s", stdout)
	}
}

// TestOperationRetryJSON pins that retry in machine mode prints the response
// object verbatim.
func TestOperationRetryJSON(t *testing.T) {
	stub := newDstub(t, 200, `{"success":true,"message":"operation requeued","operation_id":"op-1"}`)
	stdout, _, err := runDCommand(t, stub, FormatJSON, "", "operation", "retry", "b1", "op-1")
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	var got struct {
		Success     bool   `json:"success"`
		Message     string `json:"message"`
		OperationID string `json:"operation_id"`
	}
	if err := json.Unmarshal([]byte(stdout), &got); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout)
	}
	if !got.Success || got.Message != "operation requeued" || got.OperationID != "op-1" {
		t.Fatalf("unexpected response rendering: %s", stdout)
	}
}
