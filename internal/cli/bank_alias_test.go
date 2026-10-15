package cli

// Bank alias sub-group tests.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBankAliasList(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"resp-1","aliases":[]}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "alias", "list", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodGet || req.Path != "/v1/default/banks/b1/aliases" {
		t.Fatalf("request = %+v", req)
	}
	// Empty case uses the bank_id from the response, not the argument.
	wantContains(t, out, "Bank 'resp-1' has no aliases; it is reachable only by its own id")

	stub2 := newBankStub(t, bankResponse{body: `{"bank_id":"resp-1","aliases":[{"alias":"resp-1","primary":true},{"alias":"alt","primary":false}]}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatPretty, "", false, "bank", "alias", "list", "b1")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Aliases that also reach 'resp-1':")
	wantContains(t, out, "  resp-1  (shown)")
	wantContains(t, out, "  alt")
	if strings.Count(out, "(shown)") != 1 {
		t.Fatalf("(shown) appears more than once: %s", out)
	}
}

func TestBankAliasAdd(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","aliases":[{"alias":"b1","primary":true},{"alias":"alt","primary":false}]}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "alias", "add", "b1", "alt")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPost || req.Path != "/v1/default/banks/b1/aliases" {
		t.Fatalf("request = %+v", req)
	}
	if req.Body != `{"alias":"alt","primary":false}` {
		t.Fatalf("body = %s", req.Body)
	}
	wantContains(t, out, "'alt' now reaches bank 'b1'")
}

func TestBankAliasPrimary(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","aliases":[]}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "alias", "primary", "b1", "alt")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPatch || req.Path != "/v1/default/banks/b1/aliases/alt" {
		t.Fatalf("request = %+v", req)
	}
	if req.Body != `{"primary":true}` {
		t.Fatalf("body = %s", req.Body)
	}
	wantContains(t, out, "Bank 'b1' is now shown as 'alt' (its id is still 'b1')")

	stub2 := newBankStub(t, bankResponse{body: `{"bank_id":"b1","aliases":[]}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatPretty, "", false, "bank", "alias", "primary", "b1", "alt", "--clear")
	if err != nil {
		t.Fatal(err)
	}
	if body := stub2.requests()[0].Body; body != `{"primary":false}` {
		t.Fatalf("body = %s", body)
	}
	wantContains(t, out, "Bank 'b1' is shown under its own id again")
}

func TestBankAliasRemoveConfirmFlow(t *testing.T) {
	stub := newBankStub(t)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "n\n", false, "bank", "alias", "remove", "b1", "alt")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Operation cancelled")
	wantNoRequests(t, stub)

	stub2 := newBankStub(t, bankResponse{body: `{"bank_id":"b1","aliases":[]}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatPretty, "y\n", false, "bank", "alias", "remove", "b1", "alt")
	if err != nil {
		t.Fatal(err)
	}
	req := stub2.requests()[0]
	if req.Method != http.MethodDelete || req.Path != "/v1/default/banks/b1/aliases/alt" {
		t.Fatalf("request = %+v", req)
	}
	wantContains(t, out, "'alt' no longer reaches bank 'b1'")
}

func TestBankAliasMachineOutput(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","aliases":[{"alias":"b1","primary":true}]}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatJSON, "", false, "bank", "alias", "list", "b1")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, out)
	}
	if v["bank_id"] != "b1" {
		t.Fatalf("bank_id = %v", v["bank_id"])
	}
}
