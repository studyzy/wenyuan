package bankapi

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// RetiredHandler answers the retired bank routes with the terminal gone status.
// It consults no authenticator: the reference's retired handlers raise before
// any authenticated engine call, so the retirement status is revealed without a
// credential, and the router's tenant authentication (when a deployment enables
// it) exempts these paths.
//
// A retired WRITE route still declares its request body, and the framework
// validates a declared body before the handler runs — so an unparseable body is
// answered 422 even though the route is gone. ServeRoute reproduces that order:
// body gate first, retirement status second.
type RetiredHandler struct{}

// Serve returns the gone handler for one retirement message.
func (h *RetiredHandler) Serve(detail string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindGone, detail))
	}
}

// RetiredRoute is one retired route with its method, path pattern and the
// message the reference answers it with.
type RetiredRoute struct {
	Method  string
	Pattern string
	Detail  string
}

// RetiredRoutes lists the retired bank routes. They must remain routable so
// existing clients receive the terminal status rather than a not-found. The two
// profile routes share one message; the background route has its own.
var RetiredRoutes = []RetiredRoute{
	{Method: http.MethodGet, Pattern: httpx.BankPath(httpx.SuffixProfile), Detail: httpx.DetailRetiredProfile},
	{Method: http.MethodPut, Pattern: httpx.BankPath(httpx.SuffixProfile), Detail: httpx.DetailRetiredProfile},
	{Method: http.MethodPost, Pattern: httpx.BankPath(httpx.SuffixBackground), Detail: httpx.DetailRetiredBackground},
}

// ServeRoute returns the handler for one retired route: its declared body is
// validated first, then the route answers its retirement status.
func (h *RetiredHandler) ServeRoute(route RetiredRoute) http.HandlerFunc {
	body, declaresBody := retiredRouteBodies[retiredRouteKey(route.Method, route.Pattern)]
	return func(w http.ResponseWriter, r *http.Request) {
		if declaresBody {
			if failure := RetiredBodyGate(r, body.model()); failure != nil {
				httpx.WriteValidationFailure(w, failure)
				return
			}
		}
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindGone, route.Detail))
	}
}

// BindRetiredRoutes registers every retired route with its own handler.
func (h *RetiredHandler) BindRetiredRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	for _, route := range RetiredRoutes {
		handle(route.Method, route.Pattern, h.ServeRoute(route))
	}
}
