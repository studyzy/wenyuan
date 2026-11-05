package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// bankObservation is what a served request reached: the URL the handler saw
// and the bank id the mux captured from it.
type bankObservation struct {
	path, rawPath, bank string
}

// recordingRouter serves the bank member route, the config sub-route, the
// collection and the single-bank MCP mount for the tenant "acme", recording
// each request. Every route writes to its own variable: the served router
// memoizes its chain, so sharing one set would report an earlier request.
func recordingRouter(observations map[string]*bankObservation) *Router {
	extension := &stubTenantExtension{contexts: map[string]tenant.Context{
		"good": {ID: "acme", Schema: "acme"},
		// A resolution with no tenant id — the single-tenant extension — has
		// nothing to put in the prefix's place.
		"bare": {Schema: "acme"},
	}}
	router := NewRouter(RouterOptions{TenantExtension: extension})
	record := func(route string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			observations[route] = &bankObservation{
				path:    r.URL.Path,
				rawPath: r.URL.RawPath,
				bank:    r.PathValue("bank_id"),
			}
			w.WriteHeader(http.StatusOK)
		}
	}
	router.Handle(http.MethodGet, BankMemberPath(), record("member"))
	router.Handle(http.MethodGet, BankPath("/config"), record("config"))
	router.Handle(http.MethodGet, BanksCollectionPath(), record("collection"))
	router.Handle(http.MethodGet, NamespaceMCP+"/{bank_id}", record("mcp"))
	return router
}

// The re-homing through the served router: a client knows its credential and
// nothing else, so the bank id it sends moves under the tenant the credential
// resolved to — the harness spelling "coding-agent::<project>" and a bare
// "xyz" alike. An id that already names a tenant is served as sent.
func TestAgentBankRehomingThroughServedRouter(t *testing.T) {
	observations := map[string]*bankObservation{}
	router := recordingRouter(observations)
	service := router.Service()

	cases := []struct {
		name, credential, target, route string
		want                            bankObservation
	}{
		{
			name: "literal separator", credential: "good",
			target: "/v1/default/banks/coding-agent::wenyuan", route: "member",
			want: bankObservation{path: "/v1/default/banks/acme::wenyuan", bank: "acme::wenyuan"},
		},
		{
			// encodeURIComponent, which the coding-agent client uses, escapes
			// the two colons; the rewrite writes the canonical id back
			// decoded and drops the escaped spelling.
			name: "escaped separator", credential: "good",
			target: "/v1/default/banks/coding-agent%3A%3Aworkspace/config", route: "config",
			want: bankObservation{
				path: "/v1/default/banks/acme::workspace/config",
				bank: "acme::workspace",
			},
		},
		{
			name: "own tenant prefix", credential: "good",
			target: "/v1/default/banks/acme::wenyuan", route: "member",
			want: bankObservation{path: "/v1/default/banks/acme::wenyuan", bank: "acme::wenyuan"},
		},
		{
			name: "bare name", credential: "good",
			target: "/v1/default/banks/wenyuan", route: "member",
			want: bankObservation{path: "/v1/default/banks/acme::wenyuan", bank: "acme::wenyuan"},
		},
		{
			// The client's word for its own account, sent without a project;
			// "<tenant>::" would name no bank, so it is served as sent.
			name: "harness name without a project", credential: "good",
			target: "/v1/default/banks/coding-agent", route: "member",
			want: bankObservation{path: "/v1/default/banks/coding-agent", bank: "coding-agent"},
		},
		{
			name: "empty name after the separator", credential: "good",
			target: "/v1/default/banks/coding-agent::", route: "member",
			want: bankObservation{path: "/v1/default/banks/coding-agent::", bank: "coding-agent::"},
		},
		{
			name: "collection is not bank-scoped", credential: "good",
			target: "/v1/default/banks", route: "collection",
			want: bankObservation{path: "/v1/default/banks"},
		},
		{
			name: "single-bank MCP mount", credential: "good",
			target: "/mcp/coding-agent::wenyuan", route: "mcp",
			want: bankObservation{path: "/mcp/acme::wenyuan", bank: "acme::wenyuan"},
		},
		{
			// A single-tenant extension answers no tenant id, so there is
			// nothing to re-home an id to.
			name: "no resolved tenant id", credential: "bare",
			target: "/v1/default/banks/coding-agent::wenyuan", route: "member",
			want: bankObservation{path: "/v1/default/banks/coding-agent::wenyuan", bank: "coding-agent::wenyuan"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, tc.target, nil)
			request.Header.Set("Authorization", "Bearer "+tc.credential)
			recorder := httptest.NewRecorder()
			service.ServeHTTP(recorder, request)
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", recorder.Code, recorder.Body.String())
			}
			got := observations[tc.route]
			if got == nil {
				t.Fatalf("the %s route was not reached", tc.route)
			}
			if *got != tc.want {
				t.Fatalf("reached %+v, want %+v", *got, tc.want)
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

// A prefix this service cannot decide is refused rather than served under the
// caller's schema: "bob::wenyuan" may be another tenant's bank, and the prefix
// alone cannot tell. A bare id, which every client sends, is re-homed and
// reaches the handler.
func TestAgentBankPrefixRefusesForeignBankIDs(t *testing.T) {
	router := testAgentBankRouter()
	cases := []struct {
		name, target string
		want         int
	}{
		{"own tenant prefix", "/v1/default/banks/acme::wenyuan", http.StatusOK},
		{"harness prefix", "/v1/default/banks/coding-agent::wenyuan", http.StatusOK},
		{"bare name", "/v1/default/banks/wenyuan", http.StatusOK},
		{"another tenant's bank", "/v1/default/banks/bob::wenyuan", http.StatusNotFound},
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
