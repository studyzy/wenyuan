package cli

// Bank template command tests: export, import (including the file-error
// paths and verbose URL printing) and the shared schema.

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBankTemplateExportStdout(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"version":"1","bank":{"llm_model":"m"}}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "export-template", "b1")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodGet || req.Path != "/v1/default/banks/b1/export" {
		t.Fatalf("request = %+v", req)
	}
	if !strings.Contains(out, "\"version\": \"1\"") || !strings.Contains(out, "\"llm_model\": \"m\"") {
		t.Fatalf("output = %s", out)
	}
}

func TestBankTemplateExportToFile(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"version":"1"}`})
	path := filepath.Join(t.TempDir(), "manifest.json")
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "export-template", "b1", "--out", path)
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Template written to "+path)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\n  \"version\": \"1\"\n}\n" {
		t.Fatalf("file = %q", string(data))
	}

	// A write failure surfaces with the exact message.
	bad := filepath.Join(t.TempDir(), "missing-dir", "m.json")
	_, _, err = runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "export-template", "b1", "--out", bad)
	if err == nil || !strings.HasPrefix(err.Error(), "failed to write "+bad+": ") {
		t.Fatalf("err = %v", err)
	}
}

func TestBankTemplateExportShortOutFlag(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"version":"1"}`})
	path := filepath.Join(t.TempDir(), "manifest.json")
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "export-template", "b1", "-o", path)
	if err != nil {
		t.Fatal(err)
	}
	wantContains(t, out, "Template written to "+path)
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}
}

func TestBankTemplateImportFileErrors(t *testing.T) {
	stub := newBankStub(t)
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "import-template", "b1", filepath.Join(t.TempDir(), "absent.json"))
	if err == nil || !strings.HasPrefix(err.Error(), "failed to read ") {
		t.Fatalf("err = %v", err)
	}
	bad := filepath.Join(t.TempDir(), "bad.json")
	if werr := os.WriteFile(bad, []byte("not json"), 0o644); werr != nil {
		t.Fatal(werr)
	}
	_, _, err = runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "import-template", "b1", bad)
	if err == nil || !strings.HasPrefix(err.Error(), "invalid JSON in "+bad+": ") {
		t.Fatalf("err = %v", err)
	}
	wantNoRequests(t, stub)
}

func TestBankTemplateImportDryRun(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1","config_applied":true,"mental_models_created":["mm1"],"mental_models_updated":[],"directives_created":["d1"],"directives_updated":["d2"],"operation_ids":[],"dry_run":true}`})
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{"version":"1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false,
		"bank", "import-template", "b1", path, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodPost || req.Path != "/v1/default/banks/b1/import" || req.Query != "dry_run=true" {
		t.Fatalf("request = %+v", req)
	}
	if req.Body != `{"version":"1"}` {
		t.Fatalf("body = %s", req.Body)
	}
	wantContains(t, out, "Template for bank 'b1' validated")
	wantContains(t, out, `  directives created: ["d1"]`)
	wantContains(t, out, `  directives updated: ["d2"]`)
	wantContains(t, out, `  mental models created: ["mm1"]`)
	wantContains(t, out, "  mental models updated: []")
	wantContains(t, out, "  config applied: true")
}

func TestBankTemplateImportFailure(t *testing.T) {
	stub := newBankStub(t, bankResponse{status: http.StatusConflict, body: "conflict"})
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "import-template", "b1", path)
	if err == nil || err.Error() != "import failed (409 Conflict): conflict" {
		t.Fatalf("err = %v", err)
	}
}

func TestBankTemplateImportVerboseURL(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"bank_id":"b1"}`})
	path := filepath.Join(t.TempDir(), "manifest.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, stderr, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", true,
		"bank", "import-template", "b1", path, "--dry-run")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr, "POST "+stub.Server.URL+"/v1/default/banks/b1/import?dry_run=true") {
		t.Fatalf("stderr = %s", stderr)
	}
}

func TestBankTemplateSchema(t *testing.T) {
	stub := newBankStub(t, bankResponse{body: `{"type":"object"}`})
	out, _, err := runBankCommand(t, stub.Server.URL, FormatPretty, "", false, "bank", "template-schema")
	if err != nil {
		t.Fatal(err)
	}
	req := stub.requests()[0]
	if req.Method != http.MethodGet || req.Path != "/v1/bank-template-schema" {
		t.Fatalf("request = %+v", req)
	}
	if !strings.Contains(out, "\"type\": \"object\"") {
		t.Fatalf("output = %s", out)
	}
	out, _, err = runBankCommand(t, stub.Server.URL, FormatJSON, "", false, "bank", "template-schema")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "\"type\": \"object\"") {
		t.Fatalf("machine output = %s", out)
	}
}
