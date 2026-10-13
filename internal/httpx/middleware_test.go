package httpx

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// ts-0005: an unrecognized query parameter yields success plus the
// ignored-parameters header naming it.
func TestIgnoredParamsHeaderOnUnknownParameter(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/default/banks/{bank_id}", func(w http.ResponseWriter, r *http.Request) {
		recorded := RecordIgnoredParamsFor(r, DefaultQueryFilter())
		WriteJSONFor(w, recorded, http.StatusOK, map[string]any{"id": r.PathValue("bank_id")})
	})
	handler := Chain(mux, IgnoredParamsMiddleware(DefaultQueryFilter()))

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
	mux.HandleFunc("GET /v1/default/banks/{bank_id}", func(w http.ResponseWriter, r *http.Request) {
		recorded := RecordIgnoredParamsFor(r, DefaultQueryFilter())
		WriteJSONFor(w, recorded, http.StatusOK, map[string]any{"ok": true})
	})
	handler := Chain(mux, IgnoredParamsMiddleware(DefaultQueryFilter()))

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1?limit=10&tags=x", nil))
	if got := rec.Header().Get(HeaderIgnoredParams); got != "" {
		t.Fatalf("accepted parameters must not be reported, got %q", got)
	}
}

func TestIgnoredParamsAreSortedAndJoined(t *testing.T) {
	filter := NewQueryFilter("limit")
	req := httptest.NewRequest(http.MethodGet, "/x?zebra=1&apple=2&limit=3", nil)
	unknown := filter.Unknown(req)
	if strings.Join(unknown, ",") != "apple,zebra" {
		t.Fatalf("unknown = %v", unknown)
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
	handler := Chain(mux, MetricsMiddleware(recorder))
	for i := 0; i < 3; i++ {
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	}
	if recorder.Requests.Load() != 3 {
		t.Fatalf("requests = %d, want 3", recorder.Requests.Load())
	}
	if recorder.InFlight.Load() != 0 {
		t.Fatalf("in-flight should return to zero, got %d", recorder.InFlight.Load())
	}
}

// An unknown body field is reported on the response the same way an unknown
// query parameter is, and the request is still answered.
func TestUnknownBodyFieldsAreReported(t *testing.T) {
	allowed := map[string]struct{}{"items": {}, "async": {}}
	body := []byte(`{"items":[],"unknown_field":1,"another":2}`)
	got := UnknownBodyFields(body, allowed)
	if len(got) != 2 || got[0] != "another" || got[1] != "unknown_field" {
		t.Fatalf("unknown body fields = %v, want [another unknown_field]", got)
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
