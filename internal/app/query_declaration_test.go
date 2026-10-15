package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// queryDeclarationApp builds the served application over an in-memory store
// with one bank, the way the declaration test drives every route.
func queryDeclarationApp(t *testing.T) *App {
	t.Helper()
	environ := func(key string) (string, bool) {
		switch key {
		case "HINDSIGHT_API_MCP_ENABLED", "HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH":
			return "true", true
		}
		return "", false
	}
	app, err := New(context.Background(), withTestAddress(config.FromEnv(environ)),
		Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.Store.CreateBank(context.Background(), storage.Bank{BankID: "decl-bank", Name: "decl-bank"}); err != nil {
		t.Fatal(err)
	}
	return app
}

// TestRouteHandlersReadOnlyDeclaredQueryParams fails when a handler reads a
// query parameter its route did not declare. The declaration and the read come
// from different places — the route's accepted set from QueryParams at
// registration, the read from httpx.Query in the handler — so nothing but this
// check keeps them in step: a handler that reads a name its route never
// declared is reported here instead of silently accepting a parameter another
// route owns.
//
// Reading a parameter records its name whether or not the request carried it,
// so a request with no query string is enough: every read a handler reaches is
// recorded. A route that fails early on a missing body records fewer reads;
// the check is over whatever reads happened.
func TestRouteHandlersReadOnlyDeclaredQueryParams(t *testing.T) {
	app := queryDeclarationApp(t)

	const base = "/v1/default/banks/decl-bank"
	routes := []struct {
		method string
		path   string
	}{
		{http.MethodGet, "/v1/default/banks"},
		{http.MethodGet, base},
		{http.MethodGet, base + "/graph"},
		{http.MethodGet, base + "/memories/list"},
		{http.MethodGet, base + "/memories/x"},
		{http.MethodGet, base + "/memories/x/history"},
		{http.MethodGet, base + "/stats"},
		{http.MethodGet, base + "/stats/memories-timeseries"},
		{http.MethodGet, base + "/audit-logs"},
		{http.MethodGet, base + "/audit-logs/stats"},
		{http.MethodGet, base + "/llm-requests"},
		{http.MethodGet, base + "/llm-requests/stats"},
		{http.MethodGet, base + "/operations"},
		{http.MethodGet, base + "/operations/x"},
		{http.MethodGet, base + "/documents"},
		{http.MethodGet, base + "/documents/x"},
		{http.MethodGet, base + "/documents/x/chunks"},
		{http.MethodGet, base + "/tags"},
		{http.MethodGet, base + "/entities"},
		{http.MethodGet, base + "/entities/graph"},
		{http.MethodGet, base + "/entities/x"},
		{http.MethodGet, base + "/mental-models"},
		{http.MethodGet, base + "/mental-models/x"},
		{http.MethodGet, base + "/directives"},
		{http.MethodGet, base + "/webhooks"},
		{http.MethodGet, base + "/webhooks/x/deliveries"},
		{http.MethodGet, base + "/knowledge-base/tree"},
		{http.MethodGet, base + "/knowledge-base/search"},
		{http.MethodGet, base + "/observations/scopes"},
		{http.MethodGet, base + "/config"},
		{http.MethodGet, "/v1/default/chunks/x"},
		{http.MethodGet, "/v1/default/files/download/x/y.zip"},
	}

	undeclared := make([]string, 0)
	for _, route := range routes {
		req := httptest.NewRequest(route.method, route.path, nil)
		req = httpx.IgnoredParamTrace(req)
		rec := httptest.NewRecorder()
		app.Handler.ServeHTTP(rec, req)
		if read, bad := httpx.QueryAudit(req); len(bad) > 0 {
			undeclared = append(undeclared, route.method+" "+route.path+" declares no "+strings.Join(bad, ", ")+
				" (reads: "+strings.Join(read, ", ")+")")
		}
	}
	if len(undeclared) > 0 {
		t.Fatalf("handlers read undeclared query parameters:\n  %s", strings.Join(undeclared, "\n  "))
	}
}
