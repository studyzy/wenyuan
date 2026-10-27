package app

import (
	"context"
	"net/http"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// TestEveryGAPSRouteIsMounted asserts that each route the backlog records is
// actually answerable through the served handler.
//
// The check is by status code rather than by string comparison, because a route
// that is registered but unhandled is indistinguishable from a missing one to a
// client. A route that answers anything other than 404 is mounted; a 404 for a
// route the manifest lists is a regression.
func TestEveryGAPSRouteIsMounted(t *testing.T) {
	// The MCP namespace is a feature-gated surface, so the probe enables it the
	// way a deployment would rather than assuming the default.
	environ := func(key string) (string, bool) {
		switch key {
		case "WENYUAN_API_MCP_ENABLED", "WENYUAN_API_ENABLE_BANK_LLM_HEALTH":
			return "true", true
		}
		return "", false
	}
	app, err := New(context.Background(), withTestAddress(config.FromEnv(environ)),
		Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{BankID: "cov-bank", Name: "cov-bank"}); err != nil {
		t.Fatal(err)
	}

	const base = "/v1/default/banks/cov-bank"
	routes := []struct {
		method string
		path   string
	}{
		// The compatibility surface the project started with.
		{http.MethodGet, base},
		{http.MethodPut, base},
		{http.MethodPatch, base},
		{http.MethodDelete, base},
		{http.MethodGet, base + "/aliases"},
		{http.MethodPost, base + "/aliases"},
		{http.MethodPatch, base + "/aliases/x"},
		{http.MethodDelete, base + "/aliases/x"},
		{http.MethodGet, base + "/config"},
		{http.MethodPatch, base + "/config"},
		{http.MethodDelete, base + "/config"},
		// The MCP namespace answers JSON-RPC over POST.
		{http.MethodPost, "/mcp"},
		{http.MethodPost, base + "/memories"},
		{http.MethodDelete, base + "/memories"},
		{http.MethodPost, base + "/files/retain"},
		{http.MethodPost, base + "/memories/recall"},
		{http.MethodPost, base + "/reflect"},
		{http.MethodGet, base + "/operations"},
		{http.MethodPost, base + "/operations/x/retry"},
		{http.MethodDelete, base + "/operations/x"},
		{http.MethodDelete, base + "/operations/x/delete"},
		// Transfer family.
		{http.MethodPost, base + "/import"},
		{http.MethodGet, base + "/export"},
		{http.MethodPost, base + "/document-transfer"},
		{http.MethodPost, base + "/document-transfer/export"},
		{http.MethodPost, base + "/transfer/export"},
		{http.MethodPost, base + "/transfer/import"},
		{http.MethodPost, base + "/clone"},
		{http.MethodGet, base + "/attachments/x"},
		{http.MethodGet, "/v1/default/files/download/x/y.zip"},
		// Mental models.
		{http.MethodGet, base + "/mental-models"},
		{http.MethodPost, base + "/mental-models"},
		{http.MethodGet, base + "/mental-models/x"},
		{http.MethodPatch, base + "/mental-models/x"},
		{http.MethodDelete, base + "/mental-models/x"},
		{http.MethodGet, base + "/mental-models/x/history"},
		{http.MethodPost, base + "/mental-models/x/refresh"},
		{http.MethodPost, base + "/mental-models/x/dry-run-refresh"},
		{http.MethodPost, base + "/mental-models/x/clear"},
		// Directives.
		{http.MethodGet, base + "/directives"},
		{http.MethodPost, base + "/directives"},
		{http.MethodGet, base + "/directives/x"},
		{http.MethodPatch, base + "/directives/x"},
		{http.MethodDelete, base + "/directives/x"},
		// Read surface.
		{http.MethodGet, base + "/documents"},
		{http.MethodGet, base + "/documents/x"},
		{http.MethodPatch, base + "/documents/x"},
		{http.MethodDelete, base + "/documents/x"},
		{http.MethodGet, base + "/documents/x/chunks"},
		{http.MethodPost, base + "/documents/x/reprocess"},
		{http.MethodGet, base + "/tags"},
		{http.MethodGet, base + "/graph"},
		{http.MethodGet, base + "/entities"},
		{http.MethodGet, base + "/entities/graph"},
		{http.MethodGet, base + "/entities/x"},
		{http.MethodPost, base + "/entities/x/regenerate"},
		{http.MethodGet, "/v1/default/chunks/x"},
		// Reporting and consolidation.
		{http.MethodGet, base + "/stats"},
		{http.MethodGet, base + "/stats/memories-timeseries"},
		{http.MethodPost, base + "/health/llm"},
		{http.MethodGet, base + "/audit-logs"},
		{http.MethodGet, base + "/audit-logs/stats"},
		{http.MethodGet, base + "/llm-requests"},
		{http.MethodGet, base + "/llm-requests/stats"},
		{http.MethodDelete, base + "/observations"},
		{http.MethodGet, base + "/observations/scopes"},
		{http.MethodPost, base + "/consolidation-strategies/preview"},
		{http.MethodPost, base + "/consolidation/recover"},
		{http.MethodPost, base + "/consolidate"},
		{http.MethodGet, base + "/memories/list"},
		{http.MethodGet, base + "/memories/x"},
		{http.MethodPatch, base + "/memories/x"},
		{http.MethodGet, base + "/memories/x/history"},
		{http.MethodDelete, base + "/memories/x/observations"},
		{http.MethodPost, base + "/memories/dry-run-extract"},
		{http.MethodPost, base + "/prompts/preview"},
		// Knowledge base.
		{http.MethodGet, base + "/knowledge-base/tree"},
		{http.MethodPost, base + "/knowledge-base/folders"},
		{http.MethodPost, base + "/knowledge-base/pages"},
		{http.MethodGet, base + "/knowledge-base/export"},
		{http.MethodGet, base + "/knowledge-base/search"},
		{http.MethodGet, base + "/knowledge-base/pages/x"},
		{http.MethodPatch, base + "/knowledge-base/nodes/x"},
		{http.MethodDelete, base + "/knowledge-base/nodes/x"},
		// Webhooks.
		{http.MethodGet, base + "/webhooks"},
		{http.MethodPost, base + "/webhooks"},
		{http.MethodGet, base + "/webhooks/x"},
		{http.MethodPatch, base + "/webhooks/x"},
		{http.MethodDelete, base + "/webhooks/x"},
		{http.MethodGet, base + "/webhooks/x/deliveries"},
		// Contract remainder.
		{http.MethodGet, "/v1/bank-template-schema"},
		{http.MethodGet, "/v1/version"},
		{http.MethodGet, "/version"},
		{http.MethodGet, "/health"},
		{http.MethodGet, "/health/live"},
		{http.MethodGet, "/health/ready"},
		{http.MethodGet, "/metrics"},
	}

	missing := make([]string, 0)
	for _, route := range routes {
		rec := send(app, route.method, route.path, "")
		// A mounted route answers something specific: a not-found for a member
		// that does not exist, a caller error for a body it needs, or a
		// success. Only the standard library's own not-found, which carries no
		// detail envelope, means "no such route".
		if rec.Code == http.StatusNotFound && rec.Body.String() == "404 page not found\n" {
			missing = append(missing, route.method+" "+route.path)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d route(s) are not mounted:\n", len(missing))
		for _, m := range missing {
			t.Errorf("  %s", m)
		}
	}
}
