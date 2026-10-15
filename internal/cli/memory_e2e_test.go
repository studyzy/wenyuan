package cli

// End-to-end round trip: the CLI commands against the served application
// handler composed over the in-memory store, the same way a real deployment
// answers. This covers retain -> list -> recall -> clear without any stub.

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// servedTestApp composes the served application the way the app package's own
// harness does and wraps it in a live HTTP server. The listen address is an
// ephemeral one so the startup probe cannot collide with a real server.
func servedTestApp(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	cfg.Host = "127.0.0.1"
	cfg.Port = 0
	cfg.BindGrace = 0
	application, err := app.New(context.Background(), cfg, app.Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(application.Close)
	srv := httptest.NewServer(application.Handler)
	t.Cleanup(srv.Close)
	return srv
}

func TestMemoryEndToEndRetainListRecallClear(t *testing.T) {
	srv := servedTestApp(t)
	const bank = "e2e-bank"

	retain := runCommand(t, srv.URL, FormatPretty, []string{
		"memory", "retain", bank, "the deployment window opens at dawn", "--doc-id", "e2e-doc", "-c", "ops context",
	}, nil)
	if retain.err != nil {
		t.Fatalf("retain: %v", retain.err)
	}
	for _, want := range []string{
		"Memory retained successfully (document: e2e-doc)",
		"  Stored count: 1",
	} {
		if !strings.Contains(retain.stdout, want) {
			t.Errorf("retain stdout missing %q:\n%s", want, retain.stdout)
		}
	}

	list := runCommand(t, srv.URL, FormatJSON, []string{"memory", "list", bank}, nil)
	if list.err != nil {
		t.Fatalf("list: %v", list.err)
	}
	if !strings.Contains(list.stdout, "\"fact_type\"") {
		t.Errorf("list json must carry fact_type:\n%s", list.stdout)
	}
	var page struct {
		Items []struct {
			ID       string  `json:"id"`
			Text     string  `json:"text"`
			FactType *string `json:"fact_type"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(list.stdout), &page); err != nil || len(page.Items) != 1 {
		t.Fatalf("list json = %s (%v)", list.stdout, err)
	}
	if page.Items[0].Text != "the deployment window opens at dawn" {
		t.Errorf("stored text = %q", page.Items[0].Text)
	}

	get := runCommand(t, srv.URL, FormatPretty, []string{"memory", "get", bank, page.Items[0].ID}, nil)
	if get.err != nil {
		t.Fatalf("get: %v", get.err)
	}
	// The detail endpoint's type key is `type`, not fact_type.
	if !strings.Contains(get.stdout, "─── Content ───") || !strings.Contains(get.stdout, "  Context: ops context") {
		t.Errorf("get stdout:\n%s", get.stdout)
	}

	recall := runCommand(t, srv.URL, FormatPretty, []string{"memory", "recall", bank, "deployment window"}, nil)
	if recall.err != nil {
		t.Fatalf("recall: %v", recall.err)
	}
	if !strings.Contains(recall.stdout, "Search Results (1)") {
		t.Errorf("recall stdout:\n%s", recall.stdout)
	}
	if !strings.Contains(recall.stdout, "the deployment window opens at dawn") {
		t.Errorf("recall stdout missing the result text:\n%s", recall.stdout)
	}

	clear := runCommand(t, srv.URL, FormatPretty, []string{"memory", "clear", bank, "-y"}, nil)
	if clear.err != nil {
		t.Fatalf("clear: %v", clear.err)
	}
	if !strings.Contains(clear.stdout, "All memories cleared successfully") {
		t.Errorf("clear stdout:\n%s", clear.stdout)
	}

	empty := runCommand(t, srv.URL, FormatPretty, []string{"memory", "list", bank}, nil)
	if empty.err != nil {
		t.Fatalf("list after clear: %v", empty.err)
	}
	if !strings.Contains(empty.stdout, "No memories found.") {
		t.Errorf("list after clear:\n%s", empty.stdout)
	}
}

func TestMemoryEndToEndRetainJSONModes(t *testing.T) {
	srv := servedTestApp(t)
	retain := runCommand(t, srv.URL, FormatJSON, []string{
		"memory", "retain", "e2e-json", "a retained line", "--doc-id", "json-doc",
	}, nil)
	if retain.err != nil {
		t.Fatalf("retain: %v", retain.err)
	}
	for _, want := range []string{
		"\"success\": true",
		"\"items_count\": 1",
		"\"async\": false",
		"\"bank_id\": \"e2e-json\"",
	} {
		if !strings.Contains(retain.stdout, want) {
			t.Errorf("retain json missing %q:\n%s", want, retain.stdout)
		}
	}
	list := runCommand(t, srv.URL, FormatJSON, []string{"document", "get", "e2e-json", "json-doc"}, nil)
	if list.err != nil {
		t.Fatalf("document get: %v", list.err)
	}
	if !strings.Contains(list.stdout, "\"id\": \"json-doc\"") || !strings.Contains(list.stdout, "\"memory_unit_count\": 1") {
		t.Errorf("document get json:\n%s", list.stdout)
	}
}

func TestMemoryEndToEndRetainAsyncQueue(t *testing.T) {
	srv := servedTestApp(t)
	retain := runCommand(t, srv.URL, FormatPretty, []string{
		"memory", "retain", "e2e-async", "queued text", "--async",
	}, nil)
	if retain.err != nil {
		t.Fatalf("retain: %v", retain.err)
	}
	if !strings.Contains(retain.stdout, "  Status: queued for background processing") ||
		!strings.Contains(retain.stdout, "  Items: 1") {
		t.Errorf("stdout:\n%s", retain.stdout)
	}
}
