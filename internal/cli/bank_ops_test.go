package cli

// Bank async-operation tests: consolidate polling, clear-observations and
// consolidation-recover.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBankConsolidate(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"operation_id":"op-1","deduplicated":false}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "consolidate", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPost || req.Path != "/v1/default/banks/b1/consolidate" {
		t.Fatalf("request = %+v", req)
	}
	if req.Body != `{"observation_scopes":null}` {
		t.Fatalf("body = %s", req.Body)
	}
	wantContains(t, out, "Consolidation triggered")
	wantContains(t, out, "  Operation ID: op-1")
	wantContains(t, out, "Use --wait to poll for completion, or 'wenyuan-admin operation get' to check status.")
	if strings.Contains(out, "Reusing") {
		t.Fatalf("deduplicated note printed without deduplicated=true: %s", out)
	}
}

func TestBankConsolidateDeduplicated(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"operation_id":"op-1","deduplicated":true}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "consolidate", "b1")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "  Note: Reusing existing pending consolidation task")
}

func opListBody(id, status string) string {
	return `{"bank_id":"b1","total":1,"limit":50,"offset":0,"operations":[{"id":"` + id + `","task_type":"consolidation","status":"` + status + `","error_message":null}]}`
}

func TestBankConsolidateWaitPolls(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"operation_id":"op-1","deduplicated":false}`},
		bankResponse{body: opListBody("op-1", "processing")},
		bankResponse{body: opListBody("op-1", "completed")},
	)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "consolidate", "b1", "--wait", "--poll-interval", "0")
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.requests()
	if len(reqs) != 3 || reqs[1].Method != http.MethodGet || reqs[1].Path != "/v1/default/banks/b1/operations" {
		t.Fatalf("requests = %+v", reqs)
	}
	wantContains(t, out, "Polling every 0s for completion...")
	wantContains(t, out, "  ⏳ processing (0s elapsed)")
	wantContains(t, out, "Consolidation completed (0s)")
}

func TestBankConsolidateWaitFailed(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"operation_id":"op-1","deduplicated":false}`},
		bankResponse{body: `{"bank_id":"b1","operations":[{"id":"op-1","status":"failed","error_message":"boom"}]}`},
	)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "consolidate", "b1", "--wait", "--poll-interval", "0")
	if err == nil || err.Error() != "consolidation failed: boom" {
		t.Fatalf("err = %v, want consolidation failed: boom", err)
	}
}

func TestBankConsolidateWaitFailedUnknownError(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"operation_id":"op-1","deduplicated":false}`},
		bankResponse{body: `{"bank_id":"b1","operations":[{"id":"op-1","status":"failed"}]}`},
	)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "consolidate", "b1", "--wait", "--poll-interval", "0")
	if err == nil || err.Error() != "consolidation failed: Unknown error" {
		t.Fatalf("err = %v", err)
	}
}

func TestBankConsolidateWaitMissingOperation(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"operation_id":"op-1","deduplicated":false}`},
		bankResponse{body: `{"bank_id":"b1","operations":[]}`},
	)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "consolidate", "b1", "--wait", "--poll-interval", "0")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "warning: Operation op-1 not found in list")
}

func TestBankConsolidateWaitMachineIsSilent(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"operation_id":"op-1","deduplicated":false}`},
		bankResponse{body: opListBody("op-1", "processing")},
		bankResponse{body: opListBody("op-1", "completed")},
	)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatJSON, "", false,
		"bank", "consolidate", "b1", "--wait", "--poll-interval", "0")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "Polling") || strings.Contains(out, "⏳") || strings.Contains(out, "completed (") {
		t.Fatalf("machine mode printed polling progress: %s", out)
	}
	if !strings.Contains(out, "operation_id") {
		t.Fatalf("machine output lost the trigger response: %s", out)
	}
}

func TestBankClearObservations(t *testing.T) {
	stub := newBankStub(t)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "n\n", false, "bank", "clear-observations", "b1")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Operation cancelled")
	wantNoRequests(t, stub)

	stub2 := newBankStub(t, bankResponse{body: `{"success":true,"deleted_count":5}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatPretty, "y\n", false, "bank", "clear-observations", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub2.requests()[0]
	if req.Method != http.MethodDelete || req.Path != "/v1/default/banks/b1/observations" {
		t.Fatalf("request = %+v", req)
	}
	wantContains(t, out, "Observations cleared for bank 'b1'")
	wantContains(t, out, "  Observations deleted: 5")

	stub3 := newBankStub(t, bankResponse{body: `{"success":false}`})
	_, stderr, err := runBankCommand(t, stub3.Server.URL, FormatPretty, "y\n", false, "bank", "clear-observations", "b1")
	if err != nil {
		t.Fatalf("success=false must keep the exit code 0, got %v", err)
	}
	wantContains(t, stderr, "error: Failed to clear observations")
}

func TestBankConsolidationRecover(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"retried_count":2}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "consolidation-recover", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPost || req.Path != "/v1/default/banks/b1/consolidation/recover" || req.Body != "" {
		t.Fatalf("request = %+v", req)
	}
	wantContains(t, out, "Consolidation recovered for bank 'b1'")
	wantContains(t, out, "  {\n    \"retried_count\": 2\n  }")

	stub2 := newBankStub(t, bankResponse{body: `{"retried_count":2}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatJSON, "", false, "bank", "consolidation-recover", "b1")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, out)
	}
	if v["retried_count"] != float64(2) {
		t.Fatalf("retried_count = %v", v["retried_count"])
	}
}
