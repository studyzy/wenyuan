package httpx

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ts-0005: an unrecognized query parameter yields success plus the
// ignored-parameters header naming it.
func TestIgnoredParamsHeaderOnUnknownParameter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/default/banks/{bank_id}", QueryParams(func(w http.ResponseWriter, r *http.Request) {
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"id": r.PathValue("bank_id")})
	}, "limit"))
	handler := Chain(mux, IgnoredParamsMiddleware())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1?limt=10", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("unknown parameter must not be rejected, got %d", rec.Code)
	}
	if got := rec.Header().Get(HeaderIgnoredParams); got != "limt" {
		t.Fatalf("X-Ignored-Params = %q, want %q", got, "limt")
	}
}

func TestKnownParametersAreNotReported(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/default/banks/{bank_id}", QueryParams(func(w http.ResponseWriter, r *http.Request) {
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	}, "limit", "tags"))
	handler := Chain(mux, IgnoredParamsMiddleware())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1?limit=10&tags=x", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("accepted parameters must not be reported, got %q", got)
	}
}

// An accepted name is reported on a route that does not declare it: the report
// is per-route, so a parameter accepted elsewhere is still unknown here.
func TestParameterAcceptedElsewhereIsReportedHere(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /accepted", QueryParams(func(w http.ResponseWriter, r *http.Request) {
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	}, "tag"))
	mux.HandleFunc("GET /other", QueryParams(func(w http.ResponseWriter, r *http.Request) {
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	}))
	handler := Chain(mux, IgnoredParamsMiddleware())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/other?tag=x", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "tag" {
		t.Fatalf("a parameter accepted on another route must still be reported here, got %q", got)
	}
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/accepted?tag=x", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("the declaring route accepts the parameter, got %q", got)
	}
}

// A query parameter that merely repeats a path-parameter name is never
// reported, matching the reference which skips the endpoint's path parameters.
func TestPathParameterNamedQueryParameterIsNotReported(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/default/banks/{bank_id}", QueryParams(func(w http.ResponseWriter, r *http.Request) {
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"id": r.PathValue("bank_id")})
	}))
	handler := Chain(mux, IgnoredParamsMiddleware())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1?bank_id=b1", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("a path-parameter-named query parameter must not be reported, got %q", got)
	}
}

func TestIgnoredParamsPreserveEncounterOrder(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /x", QueryParams(func(w http.ResponseWriter, r *http.Request) {
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	}, "limit"))
	handler := Chain(mux, IgnoredParamsMiddleware())

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x?zebra=1&apple=2&limit=3", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "zebra, apple" {
		t.Fatalf("X-Ignored-Params = %q, want %q", got, "zebra, apple")
	}
	// A repeated name is reported once.
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x?apple=1&apple=2&zebra=3", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "apple, zebra" {
		t.Fatalf("duplicate handling = %q", got)
	}
}

// ts-0006: bodies above the floor are gzip-compressed; a negative floor
// disables compression entirely.
func TestGzipAboveFloor(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /big", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	})
	handler := Chain(mux, GzipMiddleware(512))

	req := httptest.NewRequest(http.MethodGet, "/big", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("Content-Encoding = %q, want gzip", got)
	}
	reader, err := gzip.NewReader(rec.Body)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if len(body) != 4096 {
		t.Fatalf("decompressed length = %d", len(body))
	}
}

func TestGzipBelowFloorIsNotCompressed(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /small", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("tiny"))
	})
	handler := Chain(mux, GzipMiddleware(4096))

	req := httptest.NewRequest(http.MethodGet, "/small", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("body below floor must not be compressed, Content-Encoding=%q", got)
	}
	if rec.Body.String() != "tiny" {
		t.Fatalf("body = %q", rec.Body.String())
	}
}

func TestNegativeFloorDisablesCompression(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 4096)))
	})
	handler := Chain(mux, GzipMiddleware(-1))

	req := httptest.NewRequest(http.MethodGet, "/big", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if got := rec.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("negative floor must disable compression, got %q", got)
	}
	if rec.Body.Len() != 4096 {
		t.Fatalf("body length = %d", rec.Body.Len())
	}
}

func TestMetricsMiddlewareCountsRequests(t *testing.T) {
	recorder := &MetricsRecorder{}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ping", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := Chain(mux, MetricsMiddleware(recorder, func(*http.Request) string { return "/ping" }))
	for i := 0; i < 3; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	}
	snap := recorder.Snapshot()
	if len(snap.Requests) != 1 || snap.Requests[0].Count != 3 {
		t.Fatalf("requests = %+v, want one series with count 3", snap.Requests)
	}
	if inProgress := snap.InProgress; len(inProgress) != 1 || inProgress[0].Count != 0 {
		t.Fatalf("in-flight should return to zero, got %+v", inProgress)
	}
}

// An unknown body field is reported on the response the same way an unknown
// query parameter is, and the request is still answered.
func TestUnknownBodyFieldsAreReported(t *testing.T) {
	allowed := map[string]struct{}{"items": {}, "async": {}}
	body := []byte(`{"items":[],"unknown_field":1,"another":2}`)
	got := UnknownBodyFields(body, allowed)
	if len(got) != 2 || got[0] != "unknown_field" || got[1] != "another" {
		t.Fatalf("unknown body fields = %v, want [unknown_field another]", got)
	}
	// A known body has nothing to report.
	if got := UnknownBodyFields([]byte(`{"items":[]}`), allowed); len(got) != 0 {
		t.Fatalf("known body reported fields: %v", got)
	}
	// A non-object body is left to the route's own decoder.
	if got := UnknownBodyFields([]byte(`[1,2]`), allowed); len(got) != 0 {
		t.Fatalf("array body reported fields: %v", got)
	}
}

// The accepted body fields are read from the request model's json tags, so the
// reported set stays in step with the model.
func TestAllowedBodyFieldsFromModel(t *testing.T) {
	type sample struct {
		Query    string `json:"query"`
		Limit    int    `json:"limit,omitempty"`
		Skipped  string `json:"-"`
		Untagged string
	}
	allowed := AllowedBodyFields(&sample{})
	if _, ok := allowed["query"]; !ok {
		t.Fatal("a tagged field must be accepted")
	}
	if _, ok := allowed["limit"]; !ok {
		t.Fatal("a tagged field with options must be accepted by its name")
	}
	if _, ok := allowed["-"]; ok {
		t.Fatal("a json:- field must not be accepted")
	}
	if len(allowed) != 2 {
		t.Fatalf("allowed = %v, want exactly the two tagged fields", allowed)
	}
}

// An open-row response keeps explicit null fields even when null-dropping is
// enabled for the request.
func TestOpenRowKeepsNulls(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /graph", func(w http.ResponseWriter, r *http.Request) {
		WriteJSONOpenRow(w, r, http.StatusOK, map[string]any{"date": nil, "id": "n1"})
	})
	handler := Chain(mux, NullDropMiddleware(true))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/graph", nil))
	if !strings.Contains(rec.Body.String(), `"date":null`) {
		t.Fatalf("an open-row response must keep null fields: %s", rec.Body.String())
	}
}

// A saturated admission lane sheds with 503 and Retry-After, and a request that
// goes away while waiting is reported as 499.
func TestAdmissionShedsWhenSaturated(t *testing.T) {
	release := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc("POST /memories", func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	})
	handler := Chain(mux, AdmissionMiddleware(AdmissionConfig{
		Rules:             []AdmissionRule{{Suffix: "/memories", Limit: 1}},
		MaxWait:           10 * time.Millisecond,
		RetryAfterSeconds: 3,
	}))

	// Occupy the only slot.
	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/memories", nil))
	}()
	time.Sleep(10 * time.Millisecond)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/memories", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("saturated lane = %d, want 503", rec.Code)
	}
	if rec.Header().Get(HeaderRetryAfter) == "" {
		t.Fatalf("a shed request must carry Retry-After")
	}
	close(release)
	<-firstDone
}
