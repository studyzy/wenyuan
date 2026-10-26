package reporting

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// A malformed date parameter outranks a missing bank.
//
// The reference binds `start_date`/`end_date` through `_parse_iso_datetime` in
// the handler's *signature* (api/http.py:10583-10586, 10659-10662), which FastAPI
// resolves before the body runs -- so a bad date is answered 400 before the bank
// is ever looked up, and an unknown bank with a bad date is a 400, not a 404.
// Both list routes must agree on that order.
func TestMalformedDateOutranksMissingBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := serveRoutes(t, store)

	cases := []struct{ path, wantDetail string }{
		{"/v1/default/banks/no-such-bank/audit-logs?start_date=notadate",
			"Invalid start_date: 'notadate' is not an ISO-8601 datetime."},
		{"/v1/default/banks/no-such-bank/audit-logs?end_date=bogus",
			"Invalid end_date: 'bogus' is not an ISO-8601 datetime."},
		{"/v1/default/banks/no-such-bank/llm-requests?start_date=notadate",
			"Invalid start_date: 'notadate' is not an ISO-8601 datetime."},
		{"/v1/default/banks/no-such-bank/llm-requests?end_date=bogus",
			"Invalid end_date: 'bogus' is not an ISO-8601 datetime."},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(http.MethodGet, tc.path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (body %s)", tc.path, rec.Code, rec.Body.String())
			continue
		}
		if body := rec.Body.String(); !strings.Contains(body, tc.wantDetail) {
			t.Errorf("%s: body = %s, want detail %q", tc.path, body, tc.wantDetail)
		}
	}
}

// The happy path still reports an unknown bank, so the reordering above does not
// swallow the 404 when the dates are well formed (or absent).
func TestWellFormedDatesStillReportMissingBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := serveRoutes(t, store)
	for _, path := range []string{
		"/v1/default/banks/no-such-bank/audit-logs",
		"/v1/default/banks/no-such-bank/audit-logs?start_date=2026-01-01T00:00:00Z",
		"/v1/default/banks/no-such-bank/llm-requests",
	} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404 (body %s)", path, rec.Code, rec.Body.String())
		}
	}
}

// serveRoutes mounts the reporting routes on a mux, the shared setup for the
// ordering tests above.
func serveRoutes(t *testing.T, store storage.Store) *http.ServeMux {
	t.Helper()
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	return mux
}

// A deployment with the bank-LLM health check off answers "disabled" for any
// bank, known or not: the reference tests the process setting before it looks
// the bank up (api/http.py:6695-6699), so the 404 detail is the feature's, never
// the bank's.
func TestDisabledLLMHealthOutranksMissingBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(store)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	const want = "Bank LLM health check is disabled. Set HINDSIGHT_API_ENABLE_BANK_LLM_HEALTH=true to enable."
	for _, path := range []string{
		"/v1/default/banks/no-such-bank/health/llm",
		"/v1/default/banks/whatever/health/llm",
	} {
		req := httptest.NewRequest(http.MethodPost, path, nil)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", path, rec.Code)
			continue
		}
		if body := rec.Body.String(); !strings.Contains(body, want) {
			t.Errorf("%s: body = %s, want the disabled detail", path, body)
		}
	}
}

// An enabled LLM health check answers for a bank that does not exist.
//
// The reference's check_bank_llm (engine/memory_engine.py:16346-16392) probes
// the three deployment-level LLM configurations and never calls
// _require_bank_exists — the configs are process settings, not bank rows — so
// an unknown bank gets the same 200 body a known one does. Answering 404 there
// (which this route used to do) contradicts the reference; only the empty bank
// id is still the shared missing-bank 404.
func TestEnabledLLMHealthAnswersForAnUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(store)
	h.BankLLMHealthEnabled = true
	h.Prober = stubProber{}
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})

	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/no-such-bank/health/llm", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown bank: status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"bank_id":"no-such-bank"`) {
		t.Fatalf("body = %s, want the requested bank id echoed", body)
	}
	for _, operation := range []string{"retain", "consolidation", "reflect"} {
		if !strings.Contains(body, `"operation":"`+operation+`"`) {
			t.Fatalf("body = %s, want operation %q", body, operation)
		}
	}
	// Probing must not create the bank as a side effect.
	if _, err := store.GetBank(context.Background(), "no-such-bank"); err == nil {
		t.Fatal("the health probe created the bank")
	}
}

// stubProber reports a healthy model path without calling a provider.
type stubProber struct{}

func (stubProber) Probe(context.Context) error { return nil }
