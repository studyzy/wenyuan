package monitoring

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// PrometheusContentType is the text exposition content type scrapers expect.
const PrometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// MetricsHandler serves the Prometheus text exposition endpoint.
type MetricsHandler struct {
	// Requests counts handled requests; it may be nil.
	Requests *atomic.Int64
	// InFlight reports the current in-flight request count; it may be nil.
	InFlight *atomic.Int64
	// Extra supplies additional gauge-style samples by name.
	Extra func() map[string]float64
}

// Serve writes the metrics body in text exposition format.
func (h *MetricsHandler) Serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", PrometheusContentType)
	w.WriteHeader(http.StatusOK)

	var b strings.Builder
	if h.Requests != nil {
		writeCounter(&b, "wenyuan_http_requests_total", "Total number of HTTP requests handled.", float64(h.Requests.Load()))
	}
	if h.InFlight != nil {
		writeGauge(&b, "wenyuan_http_requests_in_flight", "Number of HTTP requests currently in flight.", float64(h.InFlight.Load()))
	}
	if h.Extra != nil {
		extra := h.Extra()
		names := make([]string, 0, len(extra))
		for name := range extra {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			writeGauge(&b, name, "Deployment-provided metric.", extra[name])
		}
	}
	_, _ = w.Write([]byte(b.String()))
}

func writeCounter(b *strings.Builder, name, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s counter\n", name)
	fmt.Fprintf(b, "%s %g\n", name, value)
}

func writeGauge(b *strings.Builder, name, help string, value float64) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s gauge\n", name)
	fmt.Fprintf(b, "%s %g\n", name, value)
}

// BindMetricsRoutes registers the metrics route.
func BindMetricsRoutes(h *MetricsHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathMetrics, h.Serve)
}
