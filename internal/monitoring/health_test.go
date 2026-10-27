package monitoring

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

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

// The probe payloads' float fields render the way Python's json.dumps renders a
// float: an integral value keeps its trailing ".0" and a fractional one is
// unchanged. The reference builds these payloads as Python floats, so the wire
// text — not only the number it parses to — is part of the contract.
func TestProbeFloatRendering(t *testing.T) {
	live, err := json.Marshal(LivenessBody{Status: "alive", Version: "1.0.0", UptimeSeconds: Float(812)})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"status":"alive","version":"1.0.0","uptime_seconds":812.0}`; string(live) != want {
		t.Errorf("liveness body = %s, want %s", live, want)
	}

	// The served route renders the same text: started 812s ago, the probe
	// payload's uptime is a whole number and keeps its trailing ".0".
	served := bindHealth(&HealthHandler{Version: "1.0.0", Started: time.Now().Add(-812 * time.Second)})
	rec := httptest.NewRecorder()
	served.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathHealthLive, nil))
	if want := `{"status":"alive","version":"1.0.0","uptime_seconds":812.0}`; rec.Body.String() != want {
		t.Errorf("served liveness body = %s, want %s", rec.Body.String(), want)
	}

	acquire := Float(1)
	ready, err := json.Marshal(HealthBody{Status: "healthy", Database: "connected", DBAcquireMS: &acquire})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"status":"healthy","database":"connected","db_acquire_ms":1.0}`; string(ready) != want {
		t.Errorf("readiness body = %s, want %s", ready, want)
	}

	age := Float(0.4)
	worker, err := json.Marshal(WorkerLivenessBody{
		Status: "alive", Version: "1.0.0", UptimeSeconds: Float(100), WorkerID: "w", SecondsSinceLastPoll: &age,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"status":"alive","version":"1.0.0","uptime_seconds":100.0,"worker_id":"w","is_shutdown":false,"seconds_since_last_poll":0.4}`; string(worker) != want {
		t.Errorf("worker liveness body = %s, want %s", worker, want)
	}

	if frac, err := json.Marshal(Float(812.4)); err != nil || string(frac) != "812.4" {
		t.Errorf("fractional float = %s (err %v), want 812.4", frac, err)
	}
	if zero, err := json.Marshal(Float(0)); err != nil || string(zero) != "0.0" {
		t.Errorf("zero float = %s (err %v), want 0.0", zero, err)
	}
}

// waitingPinger is a dependency backed by a counted pool: it reports both the
// pooled-acquire waiter count and the utilization snapshot, which the one
// pool reporter exposes together.
type waitingPinger struct {
	*fakePinger
	waiting int
	present bool
	stats   PoolStats
}

func (p waitingPinger) PoolWaiting() (int, bool)     { return p.waiting, p.present }
func (p waitingPinger) PoolStats() (PoolStats, bool) { return p.stats, p.present }

// poolStatsPinger reports utilization only; it has no waiter counter, so it is
// not a pool reporter at all (see PoolReporter).
type poolStatsPinger struct{ *fakePinger }

func (p poolStatsPinger) PoolStats() (PoolStats, bool) {
	return PoolStats{InUse: 1, Max: 10, Idle: 9}, true
}

// The readiness payload carries db_pool_waiting whenever the dependency can
// count its waiters — written as 0 when nothing is waiting, not omitted. The
// reference writes the key unconditionally from its pool instrumentation
// (engine/memory_engine.py:6218, engine/db/pool_instrumentation.py:29), so the
// key's presence is part of the contract even at zero.
func TestHealthReportsPoolWaiting(t *testing.T) {
	cases := []struct {
		name  string
		db    Pinger
		want  bool
		value float64
	}{
		{name: "no waiter counter omits the key", db: &fakePinger{}, want: false},
		{name: "utilization without a counter reports no pool at all", db: poolStatsPinger{&fakePinger{}}, want: false},
		{name: "zero waiters writes 0", db: waitingPinger{fakePinger: &fakePinger{}, present: true}, want: true, value: 0},
		{name: "waiters are reported", db: waitingPinger{fakePinger: &fakePinger{}, waiting: 4, present: true}, want: true, value: 4},
		{name: "an absent count omits the key", db: waitingPinger{fakePinger: &fakePinger{}, present: false}, want: false},
	}
	for _, path := range []string{httpx.PathHealth, httpx.PathReady} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				mux := bindHealth(&HealthHandler{DB: tc.db})
				rec := httptest.NewRecorder()
				mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
				if rec.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200", rec.Code)
				}
				var body map[string]any
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				got, ok := body["db_pool_waiting"]
				if ok != tc.want {
					t.Fatalf("%s must %s db_pool_waiting: %v", path, map[bool]string{true: "carry", false: "omit"}[tc.want], body)
				}
				if ok && got.(float64) != tc.value {
					t.Fatalf("db_pool_waiting = %v, want %v", got, tc.value)
				}
			})
		}
	}
}

// A pool reporting its waiters reports the count in the payload, and a failing
// probe never reports pool keys — the reference returns its error payload before
// gathering them (engine/memory_engine.py:6210-6215).
func TestHealthPoolWaitingForwardingAndFailure(t *testing.T) {
	forwarding := &forwardingPinger{fakePinger: &fakePinger{}, waiting: 2}
	mux := bindHealth(&HealthHandler{DB: forwarding})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathHealth, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["db_pool_waiting"].(float64) != 2 {
		t.Fatalf("forwarded db_pool_waiting = %v, want 2", body["db_pool_waiting"])
	}

	failing := &forwardingPinger{fakePinger: &fakePinger{err: errors.New("down")}, waiting: 2}
	mux = bindHealth(&HealthHandler{DB: failing})
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathHealth, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	body = map[string]any{}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if _, ok := body["db_pool_waiting"]; ok {
		t.Fatalf("a failed probe must not report pool keys: %v", body)
	}
}

// forwardingPinger is a counted pool: it reports its waiter count and its
// utilization together, as the one pool reporter does.
type forwardingPinger struct {
	*fakePinger
	waiting int
}

func (p *forwardingPinger) PoolWaiting() (int, bool) { return p.waiting, true }
func (p *forwardingPinger) PoolStats() (PoolStats, bool) {
	return PoolStats{InUse: 0, Max: 100, Idle: 2}, true
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

// On success, a dependency that can report its pool contributes the pool keys,
// and one that cannot omits them.
func TestReadinessPoolStats(t *testing.T) {
	mux := bindHealth(&HealthHandler{DB: &forwardingPinger{fakePinger: &fakePinger{}, waiting: 0}})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathReady, nil))
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["db_pool_in_use"].(float64) != 0 || body["db_pool_max"].(float64) != 100 || body["db_pool_idle"].(float64) != 2 {
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

// The worker readiness body carries the worker identity and shutdown state on
// both /health and /health/ready, matching the reference's shared body.
func TestWorkerReadinessInjectsWorkerFields(t *testing.T) {
	handler := &WorkerHandler{
		HealthHandler: HealthHandler{DB: &fakePinger{}, Version: "1.0.0"},
		WorkerID:      "w-test",
		IsShutdown:    func() bool { return false },
	}
	mux := http.NewServeMux()
	BindWorkerRoutes(handler, func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, h)
	})
	for _, path := range []string{httpx.PathHealth, httpx.PathReady} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d", path, rec.Code)
		}
		var body map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body["status"] != "healthy" {
			t.Fatalf("%s status body = %v", path, body)
		}
		if body["worker_id"] != "w-test" || body["is_shutdown"] != false {
			t.Fatalf("%s must carry worker_id and is_shutdown: %v", path, body)
		}
	}
}

// A failing readiness probe keeps the worker fields and the 503 the reference
// returns, so an operator sees both the failure and which worker reported it.
func TestWorkerReadinessFailureKeepsWorkerFields(t *testing.T) {
	handler := &WorkerHandler{
		HealthHandler: HealthHandler{DB: &fakePinger{err: errors.New("down")}},
		WorkerID:      "w-test",
		IsShutdown:    func() bool { return true },
	}
	mux := http.NewServeMux()
	BindWorkerRoutes(handler, func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, h)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathHealth, nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "unhealthy" || body["worker_id"] != "w-test" || body["is_shutdown"] != true {
		t.Fatalf("worker readiness failure body = %v", body)
	}
}

// The worker root endpoint returns the info payload, and every other path still
// 404s rather than falling through to it.
func TestWorkerRootInfoEndpoint(t *testing.T) {
	handler := &WorkerHandler{WorkerID: "w-test", IsShutdown: func() bool { return false }}
	mux := http.NewServeMux()
	BindWorkerRoutes(handler, func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, h)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("root status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body["service"] != "wenyuan-worker" || body["worker_id"] != "w-test" || body["is_shutdown"] != false {
		t.Fatalf("root body = %v", body)
	}

	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/unknown", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown path status = %d, want 404", rec.Code)
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
	if !strings.Contains(rec.Body.String(), "wenyuan_async_operations_pending{otel_scope_name=\"wenyuan_api.metrics\",otel_scope_schema_url=\"\",otel_scope_version=\"\"} 3") {
		t.Fatalf("metrics body = %q", rec.Body.String())
	}
}

// combinedPinger reports both the waiter count and the pool utilization, so
// the readiness body carries every pool key.
type combinedPinger struct {
	*fakePinger
	waiting int
	stats   PoolStats
}

func (p combinedPinger) PoolWaiting() (int, bool) { return p.waiting, true }

func (p combinedPinger) PoolStats() (PoolStats, bool) { return p.stats, true }

// The readiness payload's key order is the reference's, because the response
// bytes are the contract. The reference builds the body with
// `health.update(self._pool_health_stats(backend))` (engine/memory_engine.py:6208),
// whose dict inserts db_pool_waiting before db_pool_in_use/max/idle
// (engine/memory_engine.py:6220-6235), so a scrape sees waiting first.
func TestHealthPoolKeyOrder(t *testing.T) {
	db := combinedPinger{fakePinger: &fakePinger{}, waiting: 0,
		stats: PoolStats{InUse: 1, Max: 100, Idle: 2}}
	mux := bindHealth(&HealthHandler{DB: db})
	for _, path := range []string{httpx.PathHealth, httpx.PathReady} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		body := rec.Body.String()
		// db_acquire_ms is a measurement, so it is matched by position here.
		prefix := `{"status":"healthy","database":"connected","db_acquire_ms":`
		if !strings.HasPrefix(body, prefix) {
			t.Fatalf("%s body = %s, want the reference's leading keys", path, body)
		}
		want := `,"db_pool_waiting":0,"db_pool_in_use":1,"db_pool_max":100,"db_pool_idle":2}`
		if !strings.HasSuffix(body, want) {
			t.Fatalf("%s body = %s, want suffix %s", path, body, want)
		}
	}
}

// The readiness payload's db_pool_waiting must agree with the gauge /metrics
// exposes for the same counter: one caller is waiting, so both report it. A
// dependency handed to the readiness probe is an adapter around a counted pool
// — the reference gathers the count from its pool instrumentation and receives
// its utilization snapshot from the backend (engine/memory_engine.py:6218-6236),
// so the two halves of the same health surface never disagree about a pool that
// reported its utilization. This pins the agreement as a property of the
// exported type: an adapter that forwards one must forward the other.
func TestHealthPoolWaitCountAgreesWithMetricsGauge(t *testing.T) {
	for _, tc := range []struct {
		name string
		db   Pinger
		want int
	}{
		{name: "waiter is queued", db: combinedPinger{fakePinger: &fakePinger{}, waiting: 1}, want: 1},
		{name: "no waiter is queued", db: combinedPinger{fakePinger: &fakePinger{}, waiting: 0}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux := bindHealth(&HealthHandler{DB: tc.db})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathHealth, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rec.Code)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if got, ok := body["db_pool_waiting"]; !ok || got.(float64) != float64(tc.want) {
				t.Fatalf("db_pool_waiting = %v (present %v), want %d: %s", got, ok, tc.want, rec.Body.String())
			}
		})
	}

	// guard: the helpers the assertion above relies on must keep being
	// recognized as a pool reporter, or this test would pass vacuously.
	var db Pinger = combinedPinger{fakePinger: &fakePinger{}}
	if _, ok := db.(PoolStatsReporter); !ok {
		t.Fatal("combinedPinger must report pool stats")
	}
}
