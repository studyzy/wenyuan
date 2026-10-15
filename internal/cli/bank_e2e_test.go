package cli

// End-to-end bank round trip through the served application handler: the
// CLI's requests hit the real routes over HTTP, backed by the in-memory
// store.

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// bankE2EServer composes the served application over an in-memory store and
// serves it over real HTTP.
func bankE2EServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	cfg.Host = "127.0.0.1"
	cfg.Port = 0
	cfg.BindGrace = 0
	cfg.EnableBankConfigAPI = true
	a, err := app.New(context.Background(), cfg, app.Dependencies{
		Store:               storage.NewMemoryStore("public"),
		AddressAlreadyBound: true,
	})
	if err != nil {
		t.Fatalf("compose app: %v", err)
	}
	srv := httptest.NewServer(a.Handler)
	t.Cleanup(srv.Close)
	return srv
}

// TestBankEndToEndLifecycle walks bank create → list → alias → config →
// delete → empty list through the served handler.
func TestBankEndToEndLifecycle(t *testing.T) {
	base := bankE2EServer(t).URL

	out, _, err := runBankCommand(t, base, FormatPretty, "", false,
		"bank", "create", "b1", "--name", "Friendly", "--mission", "Help users")
	if err != nil {
		t.Fatalf("create: %v\n%s", err, out)
	}
	wantContains(t, out, "Bank 'b1' created successfully")
	wantContains(t, out, "Disposition: b1")

	out, _, err = runBankCommand(t, base, FormatPretty, "", false, "bank", "list")
	if err != nil {
		t.Fatalf("list: %v\n%s", err, out)
	}
	wantContains(t, out, "Found 1 bank(s)")
	wantContains(t, out, "  - b1")

	out, _, err = runBankCommand(t, base, FormatPretty, "", false, "bank", "alias", "add", "b1", "alt")
	if err != nil {
		t.Fatalf("alias add: %v\n%s", err, out)
	}
	wantContains(t, out, "'alt' now reaches bank 'b1'")

	out, _, err = runBankCommand(t, base, FormatPretty, "", false, "bank", "alias", "list", "b1")
	if err != nil {
		t.Fatalf("alias list: %v\n%s", err, out)
	}
	wantContains(t, out, "Aliases that also reach 'b1':")
	wantContains(t, out, "  alt")

	out, _, err = runBankCommand(t, base, FormatJSON, "", false, "bank", "config", "b1", "--overrides-only")
	if err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	if !strings.HasPrefix(out, "{") {
		t.Fatalf("config overrides output = %q", out)
	}

	out, _, err = runBankCommand(t, base, FormatPretty, "", false, "bank", "delete", "b1", "-y")
	if err != nil {
		t.Fatalf("delete: %v\n%s", err, out)
	}
	wantContains(t, out, "Bank 'b1' deleted successfully")

	out, _, err = runBankCommand(t, base, FormatPretty, "", false, "bank", "list")
	if err != nil {
		t.Fatalf("list after delete: %v\n%s", err, out)
	}
	wantContains(t, out, "warning: No banks found")
}
