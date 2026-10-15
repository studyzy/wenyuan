package monitoring

import (
	"net/http"
	"net/http/httptest"
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
// and a parseable metrics body.
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
		"# TYPE hindsight_http_requests_total counter",
		"# TYPE hindsight_http_requests_in_progress gauge",
		"# TYPE hindsight_http_duration_seconds histogram",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing %q in body:\n%s", want, body)
		}
	}
}

// The labelled request total, in-progress depth and duration histogram are
// rendered from the recorder's series, with the reference's label set.
func TestMetricsRendersRequestSeries(t *testing.T) {
	recorder := &httpx.MetricsRecorder{}
	recorder.Observe(http.MethodGet, "/v1/default/banks/{bank_id}", http.StatusOK, 0)
	rec := serveMetrics(&MetricsHandler{Recorder: recorder})
	body := rec.Body.String()
	want := `hindsight_http_requests_total{endpoint="/v1/default/banks/{bank_id}",method="GET",status_class="2xx",status_code="200"} 1`
	if !strings.Contains(body, want) {
		t.Fatalf("request total series missing:\n%s", body)
	}
	if !strings.Contains(body, "hindsight_http_duration_seconds_count{") {
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
		`hindsight_http_requests_total{endpoint="/v1/default/banks/{bank_id}/config",method="GET",status_class="2xx",status_code="200"} 1`,
		`hindsight_http_requests_total{endpoint="/v1/default/banks/{bank_id}/config",method="POST",status_class="4xx",status_code="405"} 1`,
		`hindsight_http_requests_total{endpoint="unmatched",method="GET",status_class="4xx",status_code="404"} 1`,
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
	h := &MetricsHandler{Extra: func() map[string]float64 {
		return map[string]float64{"wenyuan_z_gauge": 1, "wenyuan_a_gauge": 2}
	}}
	rec := serveMetrics(h)
	body := rec.Body.String()
	if strings.Index(body, "wenyuan_a_gauge") > strings.Index(body, "wenyuan_z_gauge") {
		t.Fatalf("extra metrics must be emitted in deterministic sorted order: %s", body)
	}
}
