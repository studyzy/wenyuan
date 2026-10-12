package bankapi

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// RetiredHandler returns the terminal gone status for retired bank routes
// after enforcing authentication.
type RetiredHandler struct {
	// Auth enforces authentication before the retirement status is revealed.
	Auth httpx.Authenticator
}

// Serve rejects unauthenticated callers and otherwise returns the gone status
// whose message names the configuration routes and the bank list route.
func (h *RetiredHandler) Serve(w http.ResponseWriter, r *http.Request) {
	if h.Auth == nil || !h.Auth.Authenticated(r) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(http.StatusUnauthorized, "authentication required"))
		return
	}
	httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindGone, httpx.DetailRetired))
}

// RetiredRoute is one retired route with its method and path pattern.
type RetiredRoute struct {
	Method  string
	Pattern string
}

// RetiredRoutes lists the retired bank routes. They must remain routable so
// existing clients receive the terminal status rather than a not-found.
var RetiredRoutes = []RetiredRoute{
	{Method: http.MethodGet, Pattern: httpx.BankPath(httpx.SuffixProfile)},
	{Method: http.MethodPut, Pattern: httpx.BankPath(httpx.SuffixProfile)},
	{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixBackground)},
}

// BindRetiredRoutes registers every retired route with the shared handler.
func (h *RetiredHandler) BindRetiredRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	for _, route := range RetiredRoutes {
		handle(route.Method, route.Pattern, h.Serve)
	}
}
