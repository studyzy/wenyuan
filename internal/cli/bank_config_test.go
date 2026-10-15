package cli

// Bank configuration command tests.

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func TestBankConfigGetResolved(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","config":{"llm_model":"m","retain_chunk_size":2},"overrides":{"llm_model":"m"}}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "config", "b1")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Configuration for bank 'b1'")
	wantContains(t, out, "Resolved configuration (with all overrides applied):")
	wantContains(t, out, `  llm_model: "m"`)
	wantContains(t, out, "  retain_chunk_size: 2")
	if strings.Contains(out, "Bank-specific overrides:") {
		t.Fatalf("default view must not print the overrides heading: %s", out)
	}
}

func TestBankConfigGetOverridesOnly(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","config":{"llm_model":"m"},"overrides":{}}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "config", "b1", "--overrides-only")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Bank-specific overrides:")
	wantContains(t, out, "  (none - using defaults)")

	stub2 := newBankStub(t, bankResponse{body: `{"bank_id":"b1","config":{},"overrides":{"llm_model":"m"}}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatPretty, "", false, "bank", "config", "b1", "--overrides-only")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, `  llm_model: "m"`)
}

func TestBankConfigGetMachineOverridesOnly(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","config":{},"overrides":{"llm_model":"m"}}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatJSON, "", false, "bank", "config", "b1", "--overrides-only")
	if err != nil {
		t.Fatal(err)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, out)
	}
	if _, ok := v["bank_id"]; ok {
		t.Fatalf("overrides-only output must not carry the envelope: %s", out)
	}
	if v["llm_model"] != "m" {
		t.Fatalf("llm_model = %v", v["llm_model"])
	}
}

func TestBankSetConfigNoFlags(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "set-config", "b1")
	wantCommand(t, err, "No config updates provided. Use --llm-provider, --llm-model, --retain-mission, --retain-chunk-size, --observations-mission, or other flags")
	wantNoRequests(t, stub)
}

func TestBankSetConfigBody(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","config":{},"overrides":{"llm_model":"m"}}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "set-config", "b1", "--llm-model", "m", "--retain-chunk-size", "2", "--disposition-empathy", "5")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPatch || req.Path != "/v1/default/banks/b1/config" {
		t.Fatalf("request = %+v", req)
	}
	var body struct {
		Updates map[string]any `json:"updates"`
	}
	if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Updates["llm_model"] != "m" || body.Updates["retain_chunk_size"] != float64(2) || body.Updates["disposition_empathy"] != float64(5) {
		t.Fatalf("updates = %v", body.Updates)
	}
	if len(body.Updates) != 3 {
		t.Fatalf("updates = %v", body.Updates)
	}
	wantContains(t, out, "Configuration updated for bank 'b1'")
	wantContains(t, out, "Updated overrides:")
	wantContains(t, out, `  llm_model: "m"`)
}

func TestBankSetConfigChunkSizeValidated(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "set-config", "b1", "--retain-chunk-size", "0")
	wantUsage(t, err, "--retain-chunk-size must be at least 1")
	wantNoRequests(t, stub)
}

func TestBankResetConfigConfirmFlow(t *testing.T) {
	stub := newBankStub(t)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "n\n", false, "bank", "reset-config", "b1")
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Operation cancelled")
	wantNoRequests(t, stub)

	stub2 := newBankStub(t, bankResponse{body: `{"bank_id":"b1","config":{},"overrides":{}}`})
	out, _, err = runBankCommand(t, stub2.Server.URL, FormatPretty, "y\n", false, "bank", "reset-config", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub2.requests()[0]
	if req.Method != http.MethodDelete || req.Path != "/v1/default/banks/b1/config" {
		t.Fatalf("request = %+v", req)
	}
	wantContains(t, out, "Configuration reset to defaults for bank 'b1'")
}

func TestBankSetDisposition(t *testing.T) {
	stub := newBankStub(t,
		bankResponse{body: `{"bank_id":"b1","config":{},"overrides":{}}`},
		bankResponse{body: `{"bank_id":"b1","config":{"disposition_skepticism":4,"disposition_literalism":2,"disposition_empathy":5},"overrides":{}}`},
		bankResponse{body: `{"banks":[{"bank_id":"b1","name":"Friendly","disposition":{"skepticism":3,"literalism":3,"empathy":3}}],"total":1,"limit":100,"offset":0}`},
	)
	out, _, err := runBankCommand(t, stub.Server.URL, FormatJSON, "", false,
		"bank", "set-disposition", "b1", "--skepticism", "4", "--literalism", "2", "--empathy", "5")
	if err != nil {
		t.Fatal(err)
	}
	reqs := stub.requests()
	if len(reqs) != 3 {
		t.Fatalf("made %d requests, want 3 (patch, config, name lookup)", len(reqs))
	}
	if reqs[0].Method != http.MethodPatch || reqs[0].Path != "/v1/default/banks/b1/config" {
		t.Fatalf("request 1 = %+v", reqs[0])
	}
	var body struct {
		Updates map[string]any `json:"updates"`
	}
	if err := json.Unmarshal([]byte(reqs[0].Body), &body); err != nil {
		t.Fatal(err)
	}
	if body.Updates["disposition_skepticism"] != float64(4) || body.Updates["disposition_literalism"] != float64(2) || body.Updates["disposition_empathy"] != float64(5) {
		t.Fatalf("updates = %v", body.Updates)
	}
	// Machine output is the re-read disposition view.
	var v map[string]any
	if err := json.Unmarshal([]byte(out), &v); err != nil {
		t.Fatalf("output is not JSON: %v (%s)", err, out)
	}
	if v["skepticism"] != float64(4) || v["literalism"] != float64(2) || v["empathy"] != float64(5) || v["name"] != "Friendly" {
		t.Fatalf("view = %v", v)
	}
}

func TestBankSetDispositionRequiresAllTraits(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "set-disposition", "b1", "--skepticism", "4")
	wantUsage(t, err, "bank set-disposition requires --skepticism, --literalism, and --empathy")
	wantNoRequests(t, stub)
}
