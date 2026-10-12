package monitoring

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// Pinger reports whether a dependency is reachable. The liveness route never
// consults it.
type Pinger interface {
	Ping(r *http.Request) error
}

// HealthHandler serves the readiness alias, readiness probe, and liveness
// probe.
type HealthHandler struct {
	// DB is the database readiness dependency. A nil value is treated as
	// unreachable so readiness fails closed.
	DB Pinger
}

// StatusBody is the JSON status object returned by the health routes.
type StatusBody struct {
	Status string `json:"status"`
}

// Ready serves both /health and /health/ready with identical semantics.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil || h.DB.Ping(r) != nil {
		httpx.WriteJSONFor(w, r, http.StatusServiceUnavailable, StatusBody{Status: "not_healthy"})
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, StatusBody{Status: "healthy"})
}

// Live serves /health/live. It never touches the database.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSONFor(w, r, http.StatusOK, StatusBody{Status: "alive"})
}

// BindHealthRoutes registers the monitoring health routes on the root mux
// outside the versioned bank-scoped prefix.
func BindHealthRoutes(h *HealthHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathHealth, h.Ready)
	handle(http.MethodGet, httpx.PathReady, h.Ready)
	handle(http.MethodGet, httpx.PathHealthLive, h.Live)
}
