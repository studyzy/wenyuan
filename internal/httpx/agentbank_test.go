package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The coding-agent rewrite through the served router: a client that addresses
// "coding-agent::<project>" is served the bank of the tenant its credential
// resolved to, and any other bank id reaches the handler exactly as sent.
func TestAgentBankPrefixThroughServedRouter(t *testing.T) {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{
		"good": {ID: "acme", Schema: "acme"},
		// A resolution with no tenant id — the single-tenant extension — has
		// nothing to put in the prefix's place.
		"bare": {Schema: "acme"},
	}}
	router := NewRouter(RouterOptions{TenantExtension: extension})

	var seenPath, seenRawPath, seenBank string
	record := func(w http.ResponseWriter, r *http.Request) {
		seenPath, seenRawPath, seenBank = r.URL.Path, r.URL.RawPath, r.PathValue("bank_id")
		w.WriteHeader(http.StatusOK)
	}
	router.Handle(http.MethodGet, BankMemberPath(), record)
	router.Handle(http.MethodGet, BankPath("/config"), record)
	router.Handle(http.MethodGet, BanksCollectionPath(), record)
	router.Handle(http.MethodGet, NamespaceMCP+"/{bank_id}", record)

	cases := []struct {
		name, credential, target        string
		wantPath, wantRawPath, wantBank string
	}{
		{
			name: "literal separator", credential: "good",
			target:   "/v1/default/banks/coding-agent::wenyuan",
			wantPath: "/v1/default/banks/acme::wenyuan", wantBank: "acme::wenyuan",
		},
		{
			// encodeURIComponent, which the coding-agent client uses, escapes
			// the two colons; the raw path is what the mux matches on.
			name: "escaped separator", credential: "good",
			target:      "/v1/default/banks/coding-agent%3A%3Aworkspace/config",
			wantPath:    "/v1/default/banks/acme::workspace/config",
			wantRawPath: "/v1/default/banks/acme%3A%3Aworkspace/config",
			wantBank:    "acme::workspace",
		},
		{
			name: "another tenant prefix", credential: "good",
			target:   "/v1/default/banks/acme::wenyuan",
			wantPath: "/v1/default/banks/acme::wenyuan", wantBank: "acme::wenyuan",
		},
		{
			name: "harness name without a project", credential: "good",
			target:   "/v1/default/banks/coding-agent",
			wantPath: "/v1/default/banks/acme::", wantBank: "acme::",
		},
		{
			name: "collection is not bank-scoped", credential: "good",
			target:   "/v1/default/banks",
			wantPath: "/v1/default/banks",
		},
		{
			name: "single-bank MCP mount", credential: "good",
			target:   "/mcp/coding-agent::wenyuan",
			wantPath: "/mcp/acme::wenyuan", wantBank: "acme::wenyuan",
		},
		{
			name: "no resolved tenant id", credential: "bare",
			target:   "/v1/default/banks/coding-agent::wenyuan",
			wantPath: "/v1/default/banks/coding-agent::wenyuan", wantBank: "coding-agent::wenyuan",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			seenPath, seenRawPath, seenBank = "", "", ""
			request := httptest.NewRequest(http.MethodGet, tc.target, nil)
			request.Header.Set("Authorization", "Bearer "+tc.credential)
			recorder := httptest.NewRecorder()
			router.Service().ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			if seenPath != tc.wantPath {
				t.Fatalf("path = %q, want %q", seenPath, tc.wantPath)
			}
			if seenRawPath != tc.wantRawPath {
				t.Fatalf("raw path = %q, want %q", seenRawPath, tc.wantRawPath)
			}
			if seenBank != tc.wantBank {
				t.Fatalf("bank = %q, want %q", seenBank, tc.wantBank)
			}
		})
	}
}

// testAgentBankRouter serves the bank routes for the tenant "acme": the member
// route, the config sub-route and the collection, all answering 200 when the
// request gets through.
func testAgentBankRouter() *Router {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{
		"good": {ID: "acme", Schema: "acme"},
	}}
	router := NewRouter(RouterOptions{TenantExtension: extension})
	ok := func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }
	router.Handle(http.MethodGet, BankMemberPath(), ok)
	router.Handle(http.MethodGet, BankPath("/config"), ok)
	router.Handle(http.MethodGet, BanksCollectionPath(), ok)
	return router
}

// A bank-scoped route is where the rewrite has to fire: the served route is
// /v1/default/banks/{bank_id}/..., so a prefix that leaves out the version
// segment matches no path and the harness id reaches the store unchanged.
// The unit test above registers its handler on BankMemberPath(), which is what
// makes it pin the route the rewrite is written for; this one pins the constant
// those routes are built from.
func TestBankRoutesPrefixIsTheServedBankRoute(t *testing.T) {
	if BankRoutesPrefix != PrefixBanksV1+"/" {
		t.Fatalf("BankRoutesPrefix = %q, want %q", BankRoutesPrefix, PrefixBanksV1+"/")
	}
	if !strings.HasPrefix(BankMemberPath(), BankRoutesPrefix) {
		t.Fatalf("the bank member route %q is not under %q", BankMemberPath(), BankRoutesPrefix)
	}
}

// The harness id is re-homed for every bank the caller's tenant owns and for
// none it does not: a prefix naming another tenant, or no `::` at all, is a
// bank the caller cannot reach, and the answer is the missing-bank 404 the
// contract gives for a bank that is not there.
func TestAgentBankPrefixRefusesForeignBankIDs(t *testing.T) {
	router := testAgentBankRouter()
	cases := []struct {
		name, target string
		want         int
	}{
		{"own tenant prefix", "/v1/default/banks/acme::wenyuan", http.StatusOK},
		{"harness prefix", "/v1/default/banks/coding-agent::wenyuan", http.StatusOK},
		{"another tenant's bank", "/v1/default/banks/bob::wenyuan", http.StatusNotFound},
		{"no tenant prefix", "/v1/default/banks/wenyuan", http.StatusNotFound},
		{"collection", "/v1/default/banks", http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.target, nil)
			request.Header.Set("Authorization", "Bearer good")
			recorder := httptest.NewRecorder()
			router.Service().ServeHTTP(recorder, request)
			if recorder.Code != tc.want {
				t.Fatalf("%s answered %d, want %d: %s", tc.target, recorder.Code, tc.want, recorder.Body.String())
			}
			if tc.want == http.StatusNotFound &&
				!strings.Contains(recorder.Body.String(), "Bank '") {
				t.Fatalf("refusal does not name the bank: %s", recorder.Body.String())
			}
		})
	}
}

// A rejected credential rewrites nothing: the request never reaches a tenant,
// so the 401 is the only answer and no path is served under a guessed tenant.
func TestAgentBankPrefixNeedsAResolvedTenant(t *testing.T) {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{
		"good": {ID: "acme", Schema: "acme"},
	}}
	router := NewRouter(RouterOptions{TenantExtension: extension})
	called := false
	router.Handle(http.MethodGet, BankMemberPath(), func(http.ResponseWriter, *http.Request) {
		called = true
	})

	request := httptest.NewRequest(http.MethodGet, "/v1/default/banks/coding-agent::wenyuan", nil)
	request.Header.Set("Authorization", "Bearer unknown")
	recorder := httptest.NewRecorder()
	router.Service().ServeHTTP(recorder, request)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if called {
		t.Fatal("handler ran for an unauthenticated request")
	}
}
