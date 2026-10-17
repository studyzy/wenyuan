package monitoring

import (
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// Float is a JSON number the reference emits as a Python float. Encoding/json
// writes an integral float64 as "812"; Python's json.dumps writes its float as
// "812.0". Both parse to the same number, but the probe payload's text is the
// contract, so this keeps the trailing ".0" a whole value carries and leaves a
// fractional one untouched.
type Float float64

// MarshalJSON renders the shortest round-tripping decimal and, when that is a
// whole number, appends the ".0" the reference writes.
func (f Float) MarshalJSON() ([]byte, error) {
	text := strconv.FormatFloat(float64(f), 'f', -1, 64)
	if !strings.ContainsAny(text, ".nN") {
		text += ".0"
	}
	return []byte(text), nil
}

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
	Status      string `json:"status"`
	Database    string `json:"database,omitempty"`
	DBAcquireMS *Float `json:"db_acquire_ms,omitempty"`
	Error       string `json:"error,omitempty"`
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
	Status        string `json:"status"`
	Version       string `json:"version"`
	UptimeSeconds Float  `json:"uptime_seconds"`
}

// Ready serves both /health and /health/ready with identical semantics.
func (h *HealthHandler) Ready(w http.ResponseWriter, r *http.Request) {
	body, code := h.readyBody(r)
	httpx.WriteJSON(w, code, body)
}

// readyBody probes the dependency and returns the readiness payload with the
// status code to answer with.
func (h *HealthHandler) readyBody(r *http.Request) (HealthBody, int) {
	if h.DB == nil {
		// Nothing has been wired to probe yet, which the reference reports as
		// not-initialized rather than as a database error.
		return HealthBody{Status: "unhealthy", Reason: "not_initialized"}, http.StatusServiceUnavailable
	}
	start := time.Now()
	err := h.DB.Ping(r)
	waited := time.Since(start)
	acquireMS := Float(round1(float64(waited.Microseconds()) / 1000.0))
	if err != nil {
		return HealthBody{
			Status:   "unhealthy",
			Database: "error",
			Error:    errorText(err),
		}, http.StatusServiceUnavailable
	}
	// The probe took a pooled connection to answer, so its wait is the pool's
	// acquire wait: an exhausted pool shows up here as a rising latency before
	// it shows up as a request failure. The reference records the same duration
	// from its pool instrumentation, which this service has no analogue for.
	Workload.ObserveDBAcquireWait(waited.Seconds())
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
	return body, http.StatusOK
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
	httpx.WriteJSON(w, http.StatusOK, LivenessBody{
		Status:        "alive",
		Version:       h.Version,
		UptimeSeconds: Float(round1(time.Since(h.startedAt()).Seconds())),
	})
}

func (h *HealthHandler) startedAt() time.Time {
	if h.Started.IsZero() {
		return time.Now()
	}
	return h.Started
}

// round1 rounds to one decimal place, matching the reference's probe payloads.
// The reference rounds with Python's round(), which breaks ties to even rather
// than away from zero.
func round1(value float64) float64 {
	return math.RoundToEven(value*10) / 10
}

// WorkerLivenessBody is the worker liveness payload, which adds the worker's
// identity and poll freshness to the API's liveness fields.
type WorkerLivenessBody struct {
	Status               string `json:"status"`
	Version              string `json:"version"`
	UptimeSeconds        Float  `json:"uptime_seconds"`
	WorkerID             string `json:"worker_id"`
	IsShutdown           bool   `json:"is_shutdown"`
	SecondsSinceLastPoll *Float `json:"seconds_since_last_poll"`
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

// WorkerReadyBody is the worker readiness payload: the API's readiness body
// plus the worker identity and shutdown state, which the reference injects into
// both /health and /health/ready.
type WorkerReadyBody struct {
	HealthBody
	WorkerID   string `json:"worker_id"`
	IsShutdown bool   `json:"is_shutdown"`
}

// WorkerInfoBody is the worker process's root info payload.
type WorkerInfoBody struct {
	Service    string `json:"service"`
	WorkerID   string `json:"worker_id"`
	IsShutdown bool   `json:"is_shutdown"`
}

// Ready serves the worker's /health and /health/ready. It is the API's
// readiness body with the worker's identity and shutdown state injected, so an
// operator reads one payload whichever probe they point at.
func (h *WorkerHandler) Ready(w http.ResponseWriter, r *http.Request) {
	body, code := h.readyBody(r)
	out := WorkerReadyBody{HealthBody: body, WorkerID: h.WorkerID}
	if h.IsShutdown != nil {
		out.IsShutdown = h.IsShutdown()
	}
	httpx.WriteJSON(w, code, out)
}

// Root serves the worker's root info endpoint.
func (h *WorkerHandler) Root(w http.ResponseWriter, _ *http.Request) {
	info := WorkerInfoBody{Service: "hindsight-worker", WorkerID: h.WorkerID}
	if h.IsShutdown != nil {
		info.IsShutdown = h.IsShutdown()
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}

// Live serves the worker's liveness probe with its extra fields.
func (h *WorkerHandler) Live(w http.ResponseWriter, r *http.Request) {
	body := WorkerLivenessBody{
		Status:        "alive",
		Version:       h.Version,
		UptimeSeconds: Float(round1(time.Since(h.startedAt()).Seconds())),
		WorkerID:      h.WorkerID,
	}
	if h.IsShutdown != nil {
		body.IsShutdown = h.IsShutdown()
	}
	if h.LastPoll != nil {
		if last := h.LastPoll(); last != nil {
			age := Float(round1(time.Since(*last).Seconds()))
			body.SecondsSinceLastPoll = &age
		}
	}
	httpx.WriteJSON(w, http.StatusOK, body)
}

// workerRootPattern matches only the root path: the worker's info endpoint is
// registered under this exact pattern so every other path still 404s the way
// the reference's single-route app does.
const workerRootPattern = "/{$}"

// BindWorkerRoutes registers the worker's health surface.
func BindWorkerRoutes(h *WorkerHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathHealth, h.Ready)
	handle(http.MethodGet, httpx.PathReady, h.Ready)
	handle(http.MethodGet, httpx.PathHealthLive, h.Live)
	handle(http.MethodGet, workerRootPattern, h.Root)
}

// BindHealthRoutes registers the monitoring health routes on the root mux
// outside the versioned bank-scoped prefix.
func BindHealthRoutes(h *HealthHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathHealth, h.Ready)
	handle(http.MethodGet, httpx.PathReady, h.Ready)
	handle(http.MethodGet, httpx.PathHealthLive, h.Live)
}
