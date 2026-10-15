package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runForTest(args []string, stdin string) (int, string, string) {
	var out, errB bytes.Buffer
	code := run(args, &out, &errB, strings.NewReader(stdin))
	return code, out.String(), errB.String()
}

func TestUnknownCommand(t *testing.T) {
	code, _, errOut := runForTest([]string{"bogus"}, "")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown command") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestNoArgumentsPrintsUsageToStderr(t *testing.T) {
	code, out, errOut := runForTest(nil, "")
	if code != 2 || out != "" {
		t.Fatalf("code = %d, stdout = %q", code, out)
	}
	if !strings.Contains(errOut, "Usage: wenyuan") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestInvalidOutputFormat(t *testing.T) {
	code, _, errOut := runForTest([]string{"-o", "xml", "health"}, "")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "invalid output format") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestAttachedOutputValue(t *testing.T) {
	// -o=json must parse the same as -o json; use an unknown command so the
	// exit code is deterministic.
	code, _, errOut := runForTest([]string{"-o=json", "bogus"}, "")
	if code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
}

func TestUnknownGroupSubcommand(t *testing.T) {
	code, _, errOut := runForTest([]string{"profile", "bogus"}, "")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
	if !strings.Contains(errOut, "unknown profile subcommand") {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestGroupRequiresSubcommand(t *testing.T) {
	code, _, errOut := runForTest([]string{"profile"}, "")
	if code != 2 || !strings.Contains(errOut, "requires a subcommand") {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
}

func TestHelpExitsZero(t *testing.T) {
	code, out, _ := runForTest([]string{"--help"}, "")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(out, "Current API URL:") || !strings.Contains(out, "Run 'wenyuan-admin configure' to change the API URL.") {
		t.Fatalf("help = %q", out)
	}
	if !strings.Contains(out, "profile") || !strings.Contains(out, "health") {
		t.Fatalf("help missing commands: %q", out)
	}
}

func TestProfileLifecycleThroughMain(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	code, out, errOut := runForTest([]string{"-o", "json", "profile", "create", "dev",
		"--api-url", "http://p:1", "--api-key", "sk-abcdefghijklmnop"}, "")
	if code != 0 {
		t.Fatalf("create code = %d, stderr = %q", code, errOut)
	}
	var created struct {
		Name      string `json:"name"`
		APIKeySet bool   `json:"api_key_set"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal([]byte(out), &created); err != nil {
		t.Fatalf("create json: %v", err)
	}
	if created.Name != "dev" || !created.APIKeySet {
		t.Fatalf("created = %+v", created)
	}
	info, err := os.Stat(created.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}

	code, out, _ = runForTest([]string{"profile", "show", "dev"}, "")
	if code != 0 || !strings.Contains(out, "API URL: http://p:1") || !strings.Contains(out, "API Key: sk-a...mnop") {
		t.Fatalf("show code = %d, out = %q", code, out)
	}

	code, out, _ = runForTest([]string{"-o", "yaml", "profile", "list"}, "")
	if code != 0 || !strings.Contains(out, "profiles:") || !strings.Contains(out, "- dev") {
		t.Fatalf("list code = %d, out = %q", code, out)
	}

	code, out, _ = runForTest([]string{"profile", "delete", "-y", "dev"}, "")
	if code != 0 || !strings.Contains(out, "Deleted profile 'dev'") {
		t.Fatalf("delete code = %d, out = %q", code, out)
	}

	code, out, _ = runForTest([]string{"-o", "json", "profile", "list"}, "")
	if code != 0 || !strings.Contains(out, `"profiles": []`) {
		t.Fatalf("empty list code = %d, out = %q", code, out)
	}
}

func TestProfileDeleteAbortsOnN(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if code, _, errOut := runForTest([]string{"profile", "create", "dev", "--api-url", "http://p:1"}, ""); code != 0 {
		t.Fatalf("create code = %d, stderr = %q", code, errOut)
	}
	code, out, _ := runForTest([]string{"profile", "delete", "dev"}, "n\n")
	if code != 0 {
		t.Fatalf("abort code = %d, want 0", code)
	}
	if !strings.Contains(out, "Aborted.") {
		t.Fatalf("out = %q", out)
	}
	if _, err := os.Stat(filepath.Join(home, ".wenyuan", "cli-profiles", "dev.toml")); err != nil {
		t.Fatalf("profile should survive the abort: %v", err)
	}
}

func TestProfileCreateRequiresAPIURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	code, _, errOut := runForTest([]string{"profile", "create", "dev"}, "")
	if code != 2 || !strings.Contains(errOut, "requires --api-url") {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
}

func TestConfigureSavesAndHelpReflectsIt(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	code, out, errOut := runForTest([]string{"configure", "--api-url", "http://x:1", "--api-key", "secretkey"}, "")
	if code != 0 {
		t.Fatalf("configure code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "Configuration saved to ") || !strings.Contains(out, "Source: default") {
		t.Fatalf("configure out = %q", out)
	}
	data, err := os.ReadFile(filepath.Join(home, ".wenyuan", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "api_url = \"http://x:1\"\napi_key = \"secretkey\"\n"; got != want {
		t.Fatalf("config = %q, want %q", got, want)
	}
	code, out, _ = runForTest([]string{"--help"}, "")
	if code != 0 || !strings.Contains(out, "Current API URL: http://x:1 (from config file)") {
		t.Fatalf("help code = %d, out = %q", code, out)
	}
}

func TestConfigureRejectsInvalidURL(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	code, _, errOut := runForTest([]string{"configure", "--api-url", "nope"}, "")
	if code != 1 || !strings.Contains(errOut, "Invalid API URL: nope") {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
}

func TestHealthEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"healthy","database":"connected"}`))
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	code, out, errOut := runForTest([]string{"-o", "json", "health"}, "")
	if code != 0 {
		t.Fatalf("health code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, `"status": "healthy"`) {
		t.Fatalf("health out = %q", out)
	}
}

func TestHealthConnectionRefusedIsClassified(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", url)

	code, _, errOut := runForTest([]string{"health"}, "")
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errOut, "Cannot connect to Wenyuan API") || !strings.Contains(errOut, "API URL: "+url) {
		t.Fatalf("stderr = %q", errOut)
	}
}

func TestVerboseDumpsRequests(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"api_version":"v1","features":{}}`))
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	code, _, errOut := runForTest([]string{"-v", "version"}, "")
	if code != 0 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(errOut, "> GET") || !strings.Contains(errOut, "< 200 OK") {
		t.Fatalf("verbose stderr = %q", errOut)
	}
}

func TestMetricsPassesTextThrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("# HELP go_goroutines x\ngo_goroutines 1\n"))
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	code, out, _ := runForTest([]string{"metrics"}, "")
	if code != 0 || !strings.Contains(out, "go_goroutines 1") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

func TestVersionFlagPrintsCLIVersion(t *testing.T) {
	code, out, errOut := runForTest([]string{"-V"}, "")
	if code != 0 || errOut != "" {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "wenyuan-admin ") || strings.Contains(out, "API version") {
		t.Fatalf("out = %q", out)
	}
	// The long spelling is the same flag.
	codeLong, outLong, _ := runForTest([]string{"--version"}, "")
	if codeLong != 0 || outLong != out {
		t.Fatalf("--version = %d/%q, want %d/%q", codeLong, outLong, code, out)
	}
}

// TestHealthPrettyMatchesReferenceLayout pins the section header and the
// status/database/version lines the reference prints.
func TestHealthPrettyMatchesReferenceLayout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"healthy","database":"connected","version":"0.10.3"}`))
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	code, out, errOut := runForTest([]string{"health"}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"Health Check", "Status: healthy", "Database: connected", "Version: 0.10.3"} {
		if !strings.Contains(out, want) {
			t.Errorf("health output missing %q:\n%s", want, out)
		}
	}
}

// TestVersionPrettyShowsFeatures pins the ordered feature block.
func TestVersionPrettyShowsFeatures(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"api_version":"0.10.3","features":{"mcp":true,"observations":false,"worker":true}}`))
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	code, out, errOut := runForTest([]string{"version"}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"API Version", "Version: 0.10.3", "Features:", "MCP Server: enabled", "Observations: disabled", "Background Worker: enabled"} {
		if !strings.Contains(out, want) {
			t.Errorf("version output missing %q:\n%s", want, out)
		}
	}
}

// TestMetricsMachineWrapsText pins the {"metrics": ...} envelope of the
// machine formats, where pretty prints the text under a header.
func TestMetricsMachineWrapsText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("go_goroutines 1\n"))
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	code, out, _ := runForTest([]string{"metrics"}, "")
	if code != 0 || !strings.Contains(out, "Prometheus Metrics") {
		t.Fatalf("pretty code = %d, out = %q", code, out)
	}

	code, out, _ = runForTest([]string{"-o", "json", "metrics"}, "")
	if code != 0 {
		t.Fatalf("json code = %d", code)
	}
	var wrapped struct {
		Metrics string `json:"metrics"`
	}
	if err := json.Unmarshal([]byte(out), &wrapped); err != nil {
		t.Fatalf("json output is not the metrics envelope: %v\n%s", err, out)
	}
	if wrapped.Metrics != "go_goroutines 1\n" {
		t.Fatalf("metrics = %q", wrapped.Metrics)
	}
}

// TestCommandBodyErrorExitsOne pins the exit-code split: a validation error
// raised after parsing exits 1, while an argument error exits 2.
func TestCommandBodyErrorExitsOne(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", "http://127.0.0.1:1")

	code, _, errOut := runForTest([]string{"bank", "update", "b1"}, "")
	if code != 1 {
		t.Fatalf("code = %d, want 1 (stderr %q)", code, errOut)
	}
	if !strings.Contains(errOut, "At least one field must be provided") || strings.Contains(errOut, "error: ") {
		t.Fatalf("stderr = %q", errOut)
	}

	// A missing positional is an argument error and still exits 2.
	code, _, _ = runForTest([]string{"bank", "update"}, "")
	if code != 2 {
		t.Fatalf("code = %d, want 2", code)
	}
}

// TestExploreAcceptsTuiAlias resolves the aliased spelling without launching
// the alternate screen.
func TestExploreAcceptsTuiAlias(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	code, out, errOut := runForTest([]string{"tui", "--help"}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "open the interactive explorer") {
		t.Fatalf("out = %q", out)
	}
}

// TestExportTemplateShortOutBeatsTheGlobalOutput pins the shadowing rule: the
// command's own -o wins after its path, while the global -o still applies
// before it, exactly as the reference's global arguments behave.
func TestExportTemplateShortOutBeatsTheGlobalOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"version":"1"}`))
	}))
	defer srv.Close()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("HINDSIGHT_API_URL", srv.URL)

	path := filepath.Join(t.TempDir(), "manifest.json")
	code, out, errOut := runForTest([]string{"bank", "export-template", "b1", "-o", path}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	if !strings.Contains(out, "Template written to "+path) {
		t.Fatalf("out = %q", out)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("manifest not written: %v", err)
	}

	// The global spelling before the command still selects the format.
	code, out, _ = runForTest([]string{"-o", "json", "bank", "export-template", "b1"}, "")
	if code != 0 || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("code = %d, out = %q", code, out)
	}
}

// TestGroupHelpListsSubcommands covers `--help` at a group level.
func TestGroupHelpListsSubcommands(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	code, out, errOut := runForTest([]string{"bank", "--help"}, "")
	if code != 0 {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
	for _, want := range []string{"Usage: wenyuan-admin bank", "Subcommands:", "list", "set-config"} {
		if !strings.Contains(out, want) {
			t.Errorf("group help missing %q:\n%s", want, out)
		}
	}

	code, _, errOut = runForTest([]string{"bank", "bogus", "--help"}, "")
	if code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Fatalf("code = %d, stderr = %q", code, errOut)
	}
}
