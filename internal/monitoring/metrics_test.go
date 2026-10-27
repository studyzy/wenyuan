package monitoring

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
)

func serveMetrics(h *MetricsHandler) *httptest.ResponseRecorder {
	mux := http.NewServeMux()
	BindMetricsRoutes(h, func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathMetrics, nil))
	return rec
}

// ts-0004: the response carries the Prometheus text exposition content type
// and a parseable metrics body. The reference's registry declares a family only
// once it has a sample, so with no request served the HTTP trio is absent and
// only the always-present target_info metadata gauge remains.
func TestMetricsContentTypeAndBody(t *testing.T) {
	rec := serveMetrics(&MetricsHandler{})
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != PrometheusContentType {
		t.Fatalf("content type = %q, want %q", got, PrometheusContentType)
	}
	body := rec.Body.String()
	for _, want := range []string{
		"# HELP target_info Target metadata",
		"# TYPE target_info gauge",
		`service_name="hindsight-api"`,
		`service_version="1.0.0"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in body:\n%s", want, body)
		}
	}
	for _, unwanted := range []string{
		"wenyuan_http_requests_total",
		"wenyuan_http_requests_in_progress_requests",
		"wenyuan_http_duration_seconds",
	} {
		if strings.Contains(body, unwanted) {
			t.Fatalf("family %q emitted with no sample:\n%s", unwanted, body)
		}
	}
}

// Every series carries the reference's OpenTelemetry instrumentation-scope
// labels, and the in-progress family takes the unit suffix its instrument name
// gains from the declared unit "requests". Driving a real request through the
// router leaves the in-progress depth back at zero, which is the series the
// reference exposes for every endpoint it has seen.
func TestMetricsScopeLabelsAndInProgressName(t *testing.T) {
	recorder := &httpx.MetricsRecorder{}
	router := httpx.NewRouter(httpx.RouterOptions{Metrics: recorder})
	router.Handle(http.MethodGet, "/v1/x", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	handler := &MetricsHandler{Recorder: recorder}
	BindMetricsRoutes(handler, router.Handle)
	service := router.Service()
	service.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/v1/x", nil))
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathMetrics, nil))
	body := rec.Body.String()
	want := `wenyuan_http_requests_in_progress_requests{endpoint="/v1/x",method="GET",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version=""} 0.0`
	if !strings.Contains(body, want) {
		t.Fatalf("in-progress series missing:\n%s", body)
	}
}

// The labelled request total, in-progress depth and duration histogram are
// rendered from the recorder's series, with the reference's label set.
func TestMetricsRendersRequestSeries(t *testing.T) {
	recorder := &httpx.MetricsRecorder{}
	recorder.Observe(http.MethodGet, "/v1/default/banks/{bank_id}", http.StatusOK, 0)
	rec := serveMetrics(&MetricsHandler{Recorder: recorder})
	body := rec.Body.String()
	want := `wenyuan_http_requests_total{endpoint="/v1/default/banks/{bank_id}",method="GET",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version="",status_class="2xx",status_code="200"} 1.0`
	if !strings.Contains(body, want) {
		t.Fatalf("request total series missing:\n%s", body)
	}
	if !strings.Contains(body, "wenyuan_http_duration_seconds_count{") {
		t.Fatalf("duration histogram count missing:\n%s", body)
	}
	if !strings.Contains(body, "le=\"+Inf\"") {
		t.Fatalf("duration histogram lacks the +Inf bucket:\n%s", body)
	}
}

// Drive the whole edge — router, metrics middleware and the exposition
// handler — so the label the scraped body carries is the one a real request
// produces: the route template for the matched path, the same template on a
// method mismatch (405), and the fallback for a path no route serves.
func TestMetricsEndpointLabelEndToEnd(t *testing.T) {
	recorder := &httpx.MetricsRecorder{}
	router := httpx.NewRouter(httpx.RouterOptions{Metrics: recorder})
	router.Handle(http.MethodGet, "/v1/default/banks/{bank_id}/config", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := &MetricsHandler{Recorder: recorder}
	BindMetricsRoutes(handler, router.Handle)
	service := router.Service()

	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/v1/default/banks/user-1680/config", nil),
		httptest.NewRequest(http.MethodPost, "/v1/default/banks/user-1680/config", nil),
		httptest.NewRequest(http.MethodGet, "/no-such-route", nil),
	} {
		service.ServeHTTP(httptest.NewRecorder(), req)
	}
	rec := httptest.NewRecorder()
	service.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, httpx.PathMetrics, nil))
	body := rec.Body.String()

	for _, want := range []string{
		`wenyuan_http_requests_total{endpoint="/v1/default/banks/{bank_id}/config",method="GET",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version="",status_class="2xx",status_code="200"} 1.0`,
		`wenyuan_http_requests_total{endpoint="/v1/default/banks/{bank_id}/config",method="POST",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version="",status_class="4xx",status_code="405"} 1.0`,
		`wenyuan_http_requests_total{endpoint="unmatched",method="GET",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version="",status_class="4xx",status_code="404"} 1.0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing series %q in body:\n%s", want, body)
		}
	}
	if strings.Contains(body, "user-1680") {
		t.Fatalf("endpoint label leaked the concrete path:\n%s", body)
	}
}

func TestMetricsExtraSamplesAreSorted(t *testing.T) {
	h := &MetricsHandler{Extra: func() []Metric {
		return []Metric{{Name: "wenyuan_z_gauge", Value: 1}, {Name: "wenyuan_a_gauge", Value: 2}}
	}}
	rec := serveMetrics(h)
	body := rec.Body.String()
	if strings.Index(body, "wenyuan_a_gauge") > strings.Index(body, "wenyuan_z_gauge") {
		t.Fatalf("extra metrics must be emitted in deterministic sorted order: %s", body)
	}
}

// A deployment scoped to a tenant labels every rendered series with it, so the
// HTTP instruments and the extra samples carry the same tenant value.
func TestMetricsTenantLabel(t *testing.T) {
	recorder := &httpx.MetricsRecorder{}
	recorder.Observe(http.MethodGet, "/v1/x", http.StatusOK, 0)
	h := &MetricsHandler{
		Recorder: recorder,
		Tenant:   "tenant_a",
		Extra: func() []Metric {
			return []Metric{{Name: "wenyuan_gauge", Value: 1}}
		},
	}
	body := serveMetrics(h).Body.String()
	for _, want := range []string{
		`wenyuan_http_requests_total{endpoint="/v1/x",method="GET",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version="",status_class="2xx",status_code="200",tenant="tenant_a"} 1.0`,
		`wenyuan_gauge{otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version="",tenant="tenant_a"} 1.0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in body:\n%s", want, body)
		}
	}
}

// Without a tenant configured no tenant label appears, so an unset key leaves
// the exposition unchanged.
func TestMetricsWithoutTenantHasNoTenantLabel(t *testing.T) {
	recorder := &httpx.MetricsRecorder{}
	recorder.Observe(http.MethodGet, "/v1/x", http.StatusOK, 0)
	body := serveMetrics(&MetricsHandler{Recorder: recorder}).Body.String()
	if strings.Contains(body, "tenant=") {
		t.Fatalf("unexpected tenant label:\n%s", body)
	}
}

// Per-bank samples share a name and are separated by their bank_id label, with
// the HELP/TYPE header emitted once for the name.
func TestMetricsLabelledSamples(t *testing.T) {
	h := &MetricsHandler{Extra: func() []Metric {
		return []Metric{
			{Name: "wenyuan_consolidation_backlog", Help: "queued", Value: 2, Labels: map[string]string{"bank_id": "b1"}},
			{Name: "wenyuan_consolidation_backlog", Help: "queued", Value: 5, Labels: map[string]string{"bank_id": "b2"}},
		}
	}}
	body := serveMetrics(h).Body.String()
	for _, want := range []string{
		`wenyuan_consolidation_backlog{bank_id="b1",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version=""} 2`,
		`wenyuan_consolidation_backlog{bank_id="b2",otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version=""} 5`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in body:\n%s", want, body)
		}
	}
	if got := strings.Count(body, "# HELP wenyuan_consolidation_backlog"); got != 1 {
		t.Fatalf("HELP emitted %d times, want 1:\n%s", got, body)
	}
}

// A histogram family renders its buckets (including +Inf), then its count and
// its sum, each series carrying the scope labels. Bucket bounds and counts are
// rendered as the reference's floats do — a whole value keeps its ".0", so a
// bound is the same label value ("1.0") on both sides.
func TestMetricsHistogramFamily(t *testing.T) {
	h := &MetricsHandler{Histograms: func() []Histogram {
		return []Histogram{{
			Name:    "wenyuan_event_loop_lag_seconds",
			Buckets: []float64{0.001, 1.0},
			Counts:  []uint64{1, 2},
			Sum:     0.01,
			Count:   2,
		}}
	}}
	body := serveMetrics(h).Body.String()
	const scope = `otel_scope_name="wenyuan_api.metrics",otel_scope_schema_url="",otel_scope_version=""`
	for _, want := range []string{
		"# TYPE wenyuan_event_loop_lag_seconds histogram",
		`wenyuan_event_loop_lag_seconds_bucket{le="0.001",` + scope + `} 1.0`,
		`wenyuan_event_loop_lag_seconds_bucket{le="1.0",` + scope + `} 2.0`,
		`wenyuan_event_loop_lag_seconds_bucket{le="+Inf",` + scope + `} 2.0`,
		`wenyuan_event_loop_lag_seconds_count{` + scope + `} 2.0`,
		`wenyuan_event_loop_lag_seconds_sum{` + scope + `} 0.01`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in body:\n%s", want, body)
		}
	}
	if strings.Index(body, "_count{") > strings.Index(body, "_sum{") {
		t.Fatalf("count must precede sum, as the reference's exposition orders them:\n%s", body)
	}
}

// The exposition's numbers are the reference's text, not Go's: a whole value
// keeps the ".0" CPython's repr writes, and a large positive magnitude is
// re-spelled in exponent form the way the reference exporter does.
func TestExpositionFloatFormatting(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0.0"},
		{1, "1.0"},
		{23, "23.0"},
		{-3, "-3.0"},
		{0.005, "0.005"},
		{0.036437034606933594, "0.036437034606933594"},
		{33665024, "3.3665024e+07"},
		{6038089728, "6.038089728e+09"},
		{100000, "100000.0"},
		{1000000, "1e+06"},
		{1e16, "1e+16"},
		{1e-5, "1e-05"},
	} {
		if got := formatValue(tc.in); got != tc.want {
			t.Errorf("formatValue(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The exposition is answered as one complete body with its length declared.
// The reference's framework receives a finished bytes object from the metrics
// handler, so its response carries Content-Length; streaming the exposition
// straight to the ResponseWriter instead makes Go answer chunked with no
// length header, which is a different response for the same bytes.
func TestMetricsResponseDeclaresItsLength(t *testing.T) {
	for _, tc := range []struct {
		name string
		h    *MetricsHandler
	}{
		{"plain", &MetricsHandler{}},
		{"worker", &MetricsHandler{Workers: &WorkerMetrics{render: func() []byte { return []byte("worker_family 1.0\n") }}}},
	} {
		rec := serveMetrics(tc.h)
		body := rec.Body.String()
		if body == "" {
			t.Fatalf("%s: empty exposition", tc.name)
		}
		if got := rec.Header().Get("Content-Length"); got != strconv.Itoa(len(body)) {
			t.Fatalf("%s: Content-Length = %q, want %d", tc.name, got, len(body))
		}
	}
}

// A histogram bound is a label value, so its text must match the reference's
// byte for byte: a whole bound keeps its ".0", which is what keeps "1.0" and
// "1" from being two different series.
func TestHistogramBoundaryFormatting(t *testing.T) {
	for _, tc := range []struct {
		in   float64
		want string
	}{
		{0, "0.0"},
		{1, "1.0"},
		{10, "10.0"},
		{30, "30.0"},
		{10000, "10000.0"},
		{2.5, "2.5"},
		{0.025, "0.025"},
	} {
		if got := formatBoundary(tc.in); got != tc.want {
			t.Errorf("formatBoundary(%v) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Histogram families render in sorted name order however the caller collected
// them, the way the reference's registry walks its sorted instrument names.
// Insertion order is not stable across scrapes, so an unsorted body makes the
// exposition's layout depend on map iteration.
func TestMetricsHistogramFamiliesRenderInNameOrder(t *testing.T) {
	h := &MetricsHandler{Histograms: func() []Histogram {
		return []Histogram{
			{Name: "zeta_seconds", Buckets: []float64{1}, Counts: []uint64{1}, Count: 1},
			{Name: "alpha_seconds", Buckets: []float64{1}, Counts: []uint64{1}, Count: 1},
			{Name: "mid_seconds", Buckets: []float64{1}, Counts: []uint64{1}, Count: 1},
		}
	}}
	body := serveMetrics(h).Body.String()
	alpha := strings.Index(body, "# HELP alpha_seconds")
	mid := strings.Index(body, "# HELP mid_seconds")
	zeta := strings.Index(body, "# HELP zeta_seconds")
	if alpha < 0 || mid < 0 || zeta < 0 {
		t.Fatalf("a family is missing (alpha=%d mid=%d zeta=%d):\n%s", alpha, mid, zeta, body)
	}
	if alpha >= mid || mid >= zeta {
		t.Fatalf("histogram families not in name order (alpha=%d mid=%d zeta=%d):\n%s", alpha, mid, zeta, body)
	}
}
