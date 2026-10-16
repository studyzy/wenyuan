package monitoring

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

type fakePinger struct {
	err   error
	calls int
}

func (f *fakePinger) Ping(r *http.Request) error {
	f.calls++
	return f.err
}

func bindHealth(h *HealthHandler) *http.ServeMux {
	mux := http.NewServeMux()
	BindHealthRoutes(h, func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}

// ts-0001 / inv-0002: readiness and its alias agree for the same database
// state and return 200 reachable / 503 unreachable with a JSON status body.
func TestReadinessReachableAndUnreachable(t *testing.T) {
	reachable := bindHealth(&HealthHandler{DB: &fakePinger{}})
	for _, path := range []string{httpx.PathHealth, httpx.PathReady} {
		rec := httptest.NewRecorder()
		reachable.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s reachable status = %d", path, rec.Code)
		}
		assertStatusBody(t, rec, "healthy")
	}

	unreachable := bindHealth(&HealthHandler{DB: &fakePinger{err: errors.New("down")}})
	for _, path := range []string{httpx.PathHealth, httpx.PathReady} {
		rec := httptest.NewRecorder()
		unreachable.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s unreachable status = %d", path, rec.Code)
		}
		assertStatusBody(t, rec, "unhealthy")
	}
}

func TestReadinessFailsClosedWithoutDependency(t *testing.T) {
	mux := bindHealth(&HealthHandler{DB: nil})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil database dependency must fail readiness, got %d", rec.Code)
	}
}

// ts-0002 / neg-0001 / inv-0003: liveness returns 200 while the database is
// unreachable and issues no database call.
func TestLivenessIgnoresDatabase(t *testing.T) {
	pinger := &fakePinger{err: errors.New("down")}
	mux := bindHealth(&HealthHandler{DB: pinger})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathHealthLive, nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("liveness status = %d, want 200", rec.Code)
	}
	if pinger.calls != 0 {
		t.Fatalf("liveness must not consult the database, made %d call(s)", pinger.calls)
	}
	assertStatusBody(t, rec, "alive")
}

func assertStatusBody(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a JSON status object: %v (%s)", err, rec.Body.String())
	}
	if body["status"] != want {
		t.Fatalf("status body = %v, want %q", body["status"], want)
	}
}

// The liveness payload carries the version and the process uptime, and the
// readiness payload names the database state and the acquire latency.
func TestProbePayloadFields(t *testing.T) {
	mux := bindHealth(&HealthHandler{DB: &fakePinger{}, Version: "1.2.3"})
	live := httptest.NewRecorder()
	mux.ServeHTTP(live, httptest.NewRequest(http.MethodGet, httpx.PathHealthLive, nil))
	var liveBody map[string]any
	if err := json.Unmarshal(live.Body.Bytes(), &liveBody); err != nil {
		t.Fatal(err)
	}
	if liveBody["version"] != "1.2.3" {
		t.Fatalf("liveness version = %v", liveBody["version"])
	}
	if _, ok := liveBody["uptime_seconds"]; !ok {
		t.Fatalf("liveness must report uptime_seconds: %v", liveBody)
	}

	ready := httptest.NewRecorder()
	mux.ServeHTTP(ready, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	var readyBody map[string]any
	if err := json.Unmarshal(ready.Body.Bytes(), &readyBody); err != nil {
		t.Fatal(err)
	}
	if readyBody["database"] != "connected" {
		t.Fatalf("readiness database = %v", readyBody["database"])
	}
	if _, ok := readyBody["db_acquire_ms"]; !ok {
		t.Fatalf("readiness must report db_acquire_ms: %v", readyBody)
	}
}

// poolPinger reports pool utilization, which the readiness body surfaces on
// success when the dependency can.
type poolPinger struct{ *fakePinger }

func (p poolPinger) PoolStats() (PoolStats, bool) {
	return PoolStats{InUse: 3, Max: 10, Idle: 7}, true
}

// Before anything is wired to probe, readiness reports not-initialized rather
// than a database error, as the reference does.
func TestReadinessNotInitialized(t *testing.T) {
	mux := bindHealth(&HealthHandler{})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "unhealthy" || body["reason"] != "not_initialized" {
		t.Fatalf("not-initialized body = %v", body)
	}
	if _, ok := body["database"]; ok {
		t.Fatalf("not-initialized body must not name a database error: %v", body)
	}
}

// A probe failure names the error, and a probe with no message still names its
// type, so the payload is never an empty string.
func TestReadinessFailureNamesError(t *testing.T) {
	mux := bindHealth(&HealthHandler{DB: &fakePinger{err: errors.New("boom")}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "unhealthy" || body["database"] != "error" || body["error"] != "boom" {
		t.Fatalf("failure body = %v", body)
	}

	empty := bindHealth(&HealthHandler{DB: &fakePinger{err: errNoMessage{}}})
	rec = httptest.NewRecorder()
	empty.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	body = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if message, _ := body["error"].(string); message == "" {
		t.Fatalf("a message-less failure must still name its type: %v", body)
	}
}

type errNoMessage struct{}

func (errNoMessage) Error() string { return "" }

// On success, a dependency that can report pool utilization contributes the
// pool keys, and one that cannot omits them.
func TestReadinessPoolStats(t *testing.T) {
	mux := bindHealth(&HealthHandler{DB: poolPinger{fakePinger: &fakePinger{}}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["db_pool_in_use"].(float64) != 3 || body["db_pool_max"].(float64) != 10 || body["db_pool_idle"].(float64) != 7 {
		t.Fatalf("pool stats = %v", body)
	}

	plain := bindHealth(&HealthHandler{DB: &fakePinger{}})
	rec = httptest.NewRecorder()
	plain.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	body = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["db_pool_in_use"]; ok {
		t.Fatalf("a dependency with no pool must omit the pool keys: %v", body)
	}
}

// The worker liveness payload adds the worker identity and poll freshness.
func TestWorkerLivenessFields(t *testing.T) {
	handler := &WorkerHandler{
		HealthHandler: HealthHandler{DB: &fakePinger{}, Version: "1.0.0"},
		WorkerID:      "w-1",
	}
	mux := http.NewServeMux()
	BindWorkerRoutes(handler, func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, h)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathHealthLive, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["worker_id"] != "w-1" || body["is_shutdown"] != false {
		t.Fatalf("worker liveness = %v", body)
	}
	if _, ok := body["seconds_since_last_poll"]; !ok {
		t.Fatalf("worker liveness must carry seconds_since_last_poll: %v", body)
	}
}

// The metrics endpoint includes deployment-provided gauges.
func TestMetricsExtraSeries(t *testing.T) {
	handler := &MetricsHandler{Extra: func() []Metric {
		return []Metric{{Name: "wenyuan_async_operations_pending", Value: 3}}
	}}
	mux := http.NewServeMux()
	BindMetricsRoutes(handler, func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, h)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathMetrics, nil))
	if !strings.Contains(rec.Body.String(), "wenyuan_async_operations_pending 3") {
		t.Fatalf("metrics body = %q", rec.Body.String())
	}
}
