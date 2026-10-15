package monitoring

import (
	"fmt"
	"math"
	"net/http"
	"time"

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
	// Version is the version string reported by the liveness probe.
	Version string
	// Started is the process start time the liveness probe reports uptime
	// against. A zero value is treated as "now".
	Started time.Time
}

// PoolStats is the database pool utilization the readiness payload reports on
// success. The reference gathers it best-effort, so a dependency that cannot
// report it simply omits the keys.
type PoolStats struct {
	InUse int
	Max   int
	Idle  int
}

// PoolStatsReporter reports database pool utilization for the readiness
// payload. A dependency that does not implement it reports no pool stats, which
// is what the reference does when the source is unavailable.
type PoolStatsReporter interface {
	PoolStats() (PoolStats, bool)
}

// HealthBody is the JSON status object returned by the readiness routes. It
// names the database state and the connection-acquire latency, so a failing or
// slow probe is self-diagnosing.
type HealthBody struct {
	Status      string   `json:"status"`
	Database    string   `json:"database,omitempty"`
	DBAcquireMS *float64 `json:"db_acquire_ms,omitempty"`
	Error       string   `json:"error,omitempty"`
	// Reason names why the service is not ready when there is no dependency to
	// probe at all, which the reference reports as "not_initialized".
	Reason string `json:"reason,omitempty"`
	// PoolInUse, PoolMax and PoolIdle describe the connection pool on success,
	// and are present only when the dependency reports them.
	PoolInUse *int `json:"db_pool_in_use,omitempty"`
	PoolMax   *int `json:"db_pool_max,omitempty"`
	PoolIdle  *int `json:"db_pool_idle,omitempty"`
}

// LivenessBody is the JSON status object returned by the liveness probe.
type LivenessBody struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

// Ready serves both /health and /health/ready with identical semantics.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	if h.DB == nil {
		// Nothing has been wired to probe yet, which the reference reports as
		// not-initialized rather than as a database error.
		httpx.WriteJSONFor(w, r, http.StatusServiceUnavailable, HealthBody{Status: "unhealthy", Reason: "not_initialized"})
		return
	}
	start := time.Now()
	err := h.DB.Ping(r)
	acquireMS := round1(float64(time.Since(start).Microseconds()) / 1000.0)
	if err != nil {
		httpx.WriteJSONFor(w, r, http.StatusServiceUnavailable, HealthBody{
			Status:   "unhealthy",
			Database: "error",
			Error:    errorText(err),
		})
		return
	}
	body := HealthBody{
		Status:      "healthy",
		Database:    "connected",
		DBAcquireMS: &acquireMS,
	}
	if reporter, ok := h.DB.(PoolStatsReporter); ok {
		if stats, ok := reporter.PoolStats(); ok {
			body.PoolInUse = &stats.InUse
			body.PoolMax = &stats.Max
			body.PoolIdle = &stats.Idle
		}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, body)
}

// errorText renders a probe failure the way the reference does: the message
// when there is one, and otherwise the error's type, so a pool error with no
// args still names itself in the payload.
func errorText(err error) string {
	if message := err.Error(); message != "" {
		return message
	}
	return fmt.Sprintf("%T", err)
}

// Live serves /health/live. It never touches the database: a liveness probe
// that consults a dependency turns its degradation into an outage.
func (h *HealthHandler) Live(w http.ResponseWriter, r *http.Request) {
	httpx.WriteJSONFor(w, r, http.StatusOK, LivenessBody{
		Status:        "alive",
		Version:       h.Version,
		UptimeSeconds: round1(time.Since(h.startedAt()).Seconds()),
	})
}

func (h *HealthHandler) startedAt() time.Time {
	if h.Started.IsZero() {
		return time.Now()
	}
	return h.Started
}

// round1 rounds to one decimal place, matching the reference's probe payloads.
func round1(value float64) float64 {
	return math.Round(value*10) / 10
}

// WorkerLivenessBody is the worker liveness payload, which adds the worker's
// identity and poll freshness to the API's liveness fields.
type WorkerLivenessBody struct {
	Status               string   `json:"status"`
	Version              string   `json:"version"`
	UptimeSeconds        float64  `json:"uptime_seconds"`
	WorkerID             string   `json:"worker_id"`
	IsShutdown           bool     `json:"is_shutdown"`
	SecondsSinceLastPoll *float64 `json:"seconds_since_last_poll"`
}

// WorkerHandler serves the worker process's health surface: readiness consults
// the database, liveness never does.
type WorkerHandler struct {
	HealthHandler
	// WorkerID identifies this worker process.
	WorkerID string
	// IsShutdown reports whether graceful shutdown has been signalled.
	IsShutdown func() bool
	// LastPoll is the time of the last completed poll cycle; nil before the
	// first one.
	LastPoll func() *time.Time
}

// Live serves the worker's liveness probe with its extra fields.
func (h *WorkerHandler) Live(w http.ResponseWriter, r *http.Request) {
	body := WorkerLivenessBody{
		Status:        "alive",
		Version:       h.Version,
		UptimeSeconds: round1(time.Since(h.startedAt()).Seconds()),
		WorkerID:      h.WorkerID,
	}
	if h.IsShutdown != nil {
		body.IsShutdown = h.IsShutdown()
	}
	if h.LastPoll != nil {
		if last := h.LastPoll(); last != nil {
			age := round1(time.Since(*last).Seconds())
			body.SecondsSinceLastPoll = &age
		}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, body)
}

// BindWorkerRoutes registers the worker's health surface.
func BindWorkerRoutes(h *WorkerHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathHealth, h.Ready)
	handle(http.MethodGet, httpx.PathReady, h.Ready)
	handle(http.MethodGet, httpx.PathHealthLive, h.Live)
}

// BindHealthRoutes registers the monitoring health routes on the root mux
// outside the versioned bank-scoped prefix.
func BindHealthRoutes(h *HealthHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathHealth, h.Ready)
	handle(http.MethodGet, httpx.PathReady, h.Ready)
	handle(http.MethodGet, httpx.PathHealthLive, h.Live)
}
