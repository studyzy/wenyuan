package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The metrics middleware labels each request with the route template it hit,
// and drives that label from the served mux: a routed request uses the
// template, an unmatched path uses the fallback, and a method mismatch on a
// matching path uses that path's template (the reference's 405 behaviour).
func TestMetricsLabelsFromServedMux(t *testing.T) {
	recorder := &MetricsRecorder{}
	router := NewRouter(RouterOptions{Metrics: recorder})
	router.Handle(http.MethodGet, "/widgets/{widget_id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/widgets/w-1", nil))
	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/no-such-route", nil))
	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/widgets/w-1", nil))

	snap := recorder.Snapshot()
	got := map[string]HTTPRequestSeries{}
	for _, series := range snap.Requests {
		got[series.Method+" "+series.Endpoint+" "+series.StatusCode] = series
	}

	routed, ok := got["GET /widgets/{widget_id} 200"]
	if !ok {
		t.Fatalf("missing routed series; got %v", keysOf(got))
	}
	if routed.StatusClass != "2xx" || routed.Count != 1 {
		t.Fatalf("routed series = %+v", routed)
	}
	if routed.DurationCount != 1 {
		t.Fatalf("routed duration count = %d, want 1", routed.DurationCount)
	}

	if _, ok := got["GET "+EndpointUnmatched+" 404"]; !ok {
		t.Fatalf("unmatched path must use the %q label; got %v", EndpointUnmatched, keysOf(got))
	}

	// A POST to a path that only has a GET route is a 405, labelled with the
	// path template rather than the fallback.
	if _, ok := got["POST /widgets/{widget_id} 405"]; !ok {
		t.Fatalf("405 must carry the partial route template; got %v", keysOf(got))
	}
}

// The in-progress counter settles back to zero once the request completes.
func TestMetricsInProgressSettles(t *testing.T) {
	recorder := &MetricsRecorder{}
	router := NewRouter(RouterOptions{Metrics: recorder})
	router.Handle(http.MethodGet, "/ping", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()
	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/ping", nil))
	for _, series := range recorder.Snapshot().InProgress {
		if series.Method == http.MethodGet && series.Endpoint == "/ping" && series.Count != 0 {
			t.Fatalf("in-progress did not settle: %+v", series)
		}
	}
}

// The endpoint label names the route template, and a route whose path parameter
// spans segments is labelled with the converter stripped: Go spells such a
// parameter "{name...}" where the reference declares "{name:path}", and the
// reference's label carries only "{name}". The concrete path never appears.
func TestMetricsEndpointIsTheRouteTemplate(t *testing.T) {
	recorder := &MetricsRecorder{}
	router := NewRouter(RouterOptions{Metrics: recorder})
	router.Handle(http.MethodGet, "/v1/default/chunks/{chunk_id...}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	router.Handle(http.MethodGet, "/v1/default/banks/{bank_id}/config", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	service := router.Service()

	for _, path := range []string{
		"/v1/default/chunks/chk_0123456789",
		"/v1/default/chunks/a/b/c",
		"/v1/default/banks/user-1680/config",
	} {
		service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
	}

	got := map[string]bool{}
	for _, series := range recorder.Snapshot().Requests {
		got[series.Method+" "+series.Endpoint] = true
	}
	for _, want := range []string{
		"GET /v1/default/chunks/{chunk_id}",
		"GET /v1/default/banks/{bank_id}/config",
	} {
		if !got[want] {
			t.Fatalf("missing series %q; got %v", want, got)
		}
	}
	for label := range got {
		if strings.Contains(label, "chk_") || strings.Contains(label, "user-1680") {
			t.Fatalf("endpoint label leaked the concrete path: %q", label)
		}
	}
}

func keysOf(series map[string]HTTPRequestSeries) []string {
	out := make([]string, 0, len(series))
	for key := range series {
		out = append(out, key)
	}
	return out
}
