package monitoring

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
		assertStatusBody(t, rec, "not_healthy")
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
	var body StatusBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not a JSON status object: %v (%s)", err, rec.Body.String())
	}
	if body.Status != want {
		t.Fatalf("status body = %q, want %q", body.Status, want)
	}
}
