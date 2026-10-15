package cli

// Bank lifecycle tests: the stub harness shared by the bank test files, the
// list/create/update/name/mission/delete behaviors.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// bankResponse is one canned answer in a bankStub sequence; a zero status
// means 200.
type bankResponse struct {
	status int
	body   string
}

// bankStub is a canned-response HTTP server that answers requests in
// sequence — the last response repeats — and records every request.
type bankStub struct {
	Server *httptest.Server
	mu     sync.Mutex
	queue  []bankResponse
	got    []dRequest
}

// newBankStub starts a sequence stub.
func newBankStub(t *testing.T, responses ...bankResponse) *bankStub {
	t.Helper()
	s := &bankStub{queue: responses}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		n := len(s.got)
		s.got = append(s.got, dRequest{Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery, Body: string(data)})
		s.mu.Unlock()
		resp := bankResponse{status: http.StatusOK, body: "{}"}
		switch {
		case len(s.queue) == 0:
		case n < len(s.queue):
			resp = s.queue[n]
		default:
			resp = s.queue[len(s.queue)-1]
		}
		w.Header().Set("Content-Type", "application/json")
		if resp.status != 0 && resp.status != http.StatusOK {
			w.WriteHeader(resp.status)
		}
		_, _ = w.Write([]byte(resp.body))
	}))
	t.Cleanup(s.Server.Close)
	return s
}

// requests returns the captured requests in arrival order.
func (s *bankStub) requests() []dRequest {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]dRequest(nil), s.got...)
}

// runBankCommand resolves args against a registry holding the bank family
// and executes the resolved command against baseURL.
func runBankCommand(t *testing.T, baseURL string, format Format, stdin string, verbose bool, args ...string) (string, string, error) {
	t.Helper()
	registry := NewRegistry()
	RegisterBankCommands(registry)
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
	if baseURL != "" {
		deps.Client = NewClient(baseURL, "")
	}
	err = cmd.Execute(deps.Ctx, deps)
	return out.String(), errBuf.String(), err
}

func wantContains(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("output %q does not contain %q", haystack, needle)
	}
}

func wantNoRequests(t *testing.T, s *bankStub) {
	t.Helper()
	if got := len(s.requests()); got != 0 {
		t.Fatalf("made %d HTTP requests, want none", got)
	}
}

func TestBankListWalksPages(t *testing.T) {
	page1 := `{"banks":[{"bank_id":"b1","disposition":{"skepticism":3,"literalism":3,"empathy":3}},{"bank_id":"b2","disposition":{"skepticism":3,"literalism":3,"empathy":3}}],"total":3,"limit":100,"offset":0}`
	page2 := `{"banks":[{"bank_id":"b3","disposition":{"skepticism":3,"literalism":3,"empathy":3}}],"total":3,"limit":100,"offset":100}`
	stub := newBankStub(t, bankResponse{body: page1}, bankResponse{body: page2})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "list")
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.requests()
	if len(reqs) != 2 {
		t.Fatalf("made %d requests, want 2", len(reqs))
	}
	if reqs[0].Method != http.MethodGet || reqs[0].Path != "/v1/default/banks" || reqs[0].Query != "limit=100&offset=0" {
		t.Fatalf("request 1 = %+v", reqs[0])
	}
	if reqs[1].Query != "limit=100&offset=2" {
		t.Fatalf("request 2 = %+v", reqs[1])
	}
	wantContains(t, out, "Found 3 bank(s)")
	wantContains(t, out, "  - b1")
	wantContains(t, out, "  - b2")
	wantContains(t, out, "  - b3")
}

func TestBankListEmptyWarns(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"banks":[],"total":0,"limit":100,"offset":0}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "list")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "warning: No banks found")
}

func TestBankListJSONIsBareArray(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"banks":[{"bank_id":"b1","name":"N","disposition":{"skepticism":2,"literalism":3,"empathy":4},"fact_count":2,"matched_aliases":[]}],"total":1,"limit":100,"offset":0}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatJSON, "", false, "bank", "list")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, `"total"`) || strings.Contains(out, `"offset"`) {
		t.Fatalf("machine output leaked the envelope: %s", out)
	}
	var items []map[string]any
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("output is not a JSON array: %v (%s)", err, out)
	}
	if len(items) != 1 || items[0]["bank_id"] != "b1" || items[0]["fact_count"] != float64(2) {
		t.Fatalf("items = %+v", items)
	}
}

func TestBankCreateSendsBodyAndRendersView(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","name":"N","disposition":{"skepticism":4,"literalism":3,"empathy":3},"mission":null}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "create", "b1", "--name", "N", "--skepticism", "4")
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.requests()
	if len(reqs) != 1 || reqs[0].Method != http.MethodPut || reqs[0].Path != "/v1/default/banks/b1" {
		t.Fatalf("requests = %+v", reqs)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, reqs[0].Body)
	}
	if body["name"] != "N" || body["mission"] != nil || body["background"] != nil {
		t.Fatalf("body = %s", reqs[0].Body)
	}
	disp, ok := body["disposition"].(map[string]any)
	if !ok || disp["skepticism"] != float64(4) || disp["literalism"] != float64(3) || disp["empathy"] != float64(3) {
		t.Fatalf("disposition = %v", body["disposition"])
	}
	wantContains(t, out, "Bank 'b1' created successfully")
	wantContains(t, out, "Name: N")
	wantContains(t, out, "4/5")
}

func TestBankCreateOmitsDispositionWithoutTraits(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "create", "b1")
	if err != nil {
		t.Fatal(err)
	}
	if body := stub.requests()[0].Body; strings.Contains(body, `"disposition"`) {
		t.Fatalf("body sent a disposition without trait flags: %s", body)
	}
}

func TestBankCreateTraitRangeValidated(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "create", "b1", "--skepticism", "6")
	wantUsage(t, err, "--skepticism must be between 1 and 5")
	wantNoRequests(t, stub)
}

func TestBankUpdateRequiresOneField(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "update", "b1")
	wantCommand(t, err, "At least one field must be provided (--name, --mission, --skepticism, --literalism, --empathy)")
	wantNoRequests(t, stub)
}

func TestBankUpdateSendsPatch(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "update", "b1", "--mission", "M", "--empathy", "2")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPatch || req.Path != "/v1/default/banks/b1" {
		t.Fatalf("request = %+v", req)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["mission"] != "M" || body["name"] != nil {
		t.Fatalf("body = %s", req.Body)
	}
	disp := body["disposition"].(map[string]any)
	if disp["empathy"] != float64(2) || disp["skepticism"] != float64(3) || disp["literalism"] != float64(3) {
		t.Fatalf("disposition = %v", disp)
	}
}

func TestBankNamePutsNulls(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","name":"Resp","disposition":{"skepticism":3,"literalism":3,"empathy":3}}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "name", "b1", "New Name")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPut {
		t.Fatalf("method = %s", req.Method)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body["name"] != "New Name" || body["mission"] != nil || body["background"] != nil || body["disposition"] != nil {
		t.Fatalf("body = %s", req.Body)
	}
	wantContains(t, out, "Bank name updated to 'Resp'")
}

func TestBankMissionPatches(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","mission":"From server","disposition":{"skepticism":3,"literalism":3,"empathy":3}}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "mission", "b1", "Do good")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPatch || req.Body != `{"mission":"Do good"}` {
		t.Fatalf("request = %+v", req)
	}
	wantContains(t, out, "Mission updated successfully")
	wantContains(t, out, "From server")
}

func TestBankDeleteConfirmFlow(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"success":true}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "n\n", false, "bank", "delete", "b1")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Operation cancelled")
	wantNoRequests(t, stub)

	stub2 := newBankStub(t, bankResponse{body: `{"success":true}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatPretty, "y\n", false, "bank", "delete", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub2.requests()[0]
	if req.Method != http.MethodDelete || req.Path != "/v1/default/banks/b1" {
		t.Fatalf("request = %+v", req)
	}
	wantContains(t, out, "Bank 'b1' deleted successfully")

	// Machine formats execute without any prompt.
	stub3 := newBankStub(t, bankResponse{body: `{"success":true}`})
	_, _, err = runBankCommand(t, stub3.Server.URL, FormatJSON, "", false, "bank", "delete", "b1")
	if err != nil {
		t.Fatal(err)
	}
	if got := len(stub3.requests()); got != 1 {
		t.Fatalf("machine delete made %d requests, want 1", got)
	}
}

func TestBankDeleteItemsDeletedAndFailure(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"success":true,"deleted_count":7}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "y\n", false, "bank", "delete", "b1")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "  Items deleted: 7")

	stub2 := newBankStub(t, bankResponse{body: `{"success":false}`})
	_, stderr, err := runBankCommand(t, stub2.Server.URL, FormatPretty, "y\n", false, "bank", "delete", "b1")
	if err != nil {
		t.Fatalf("success=false must keep the exit code 0, got %v", err)
	}
	wantContains(t, stderr, "error: Failed to delete bank")
}
