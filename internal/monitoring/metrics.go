package monitoring

import (
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// PrometheusContentType is the text exposition content type scrapers expect.
const PrometheusContentType = "text/plain; version=0.0.4; charset=utf-8"

// MetricsHandler serves the Prometheus text exposition endpoint.
type MetricsHandler struct {
	// Recorder supplies the labelled HTTP request series; it may be nil.
	Recorder *httpx.MetricsRecorder
	// Extra supplies additional gauge-style samples by name.
	Extra func() map[string]float64
}

// Serve writes the metrics body in text exposition format.
func (h *MetricsHandler) Serve(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", PrometheusContentType)
	w.WriteHeader(http.StatusOK)

	var b strings.Builder
	var snap httpx.MetricsSnapshot
	if h.Recorder != nil {
		snap = h.Recorder.Snapshot()
	}
	// The HTTP instruments are declared at start-up, so their HELP/TYPE lines
	// are always present even before the first request lands.
	writeHTTPRequests(&b, snap)
	if h.Extra != nil {
		extra := h.Extra()
		names := make([]string, 0, len(extra))
		for name := range extra {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			writeGauge(&b, name, "Deployment-provided metric.", extra[name], nil)
		}
	}
	_, _ = w.Write([]byte(b.String()))
}

// BindMetricsRoutes registers the metrics route.
func BindMetricsRoutes(h *MetricsHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathMetrics, h.Serve)
}

// writeHTTPRequests renders the request total, in-progress and duration series
// under the reference's instrument names.
func writeHTTPRequests(b *strings.Builder, snap httpx.MetricsSnapshot) {
	writeHelpType(b, "hindsight_http_requests_total", "Total number of HTTP requests", "counter")
	for _, series := range snap.Requests {
		labels := requestLabels(series, true)
		fmt.Fprintf(b, "hindsight_http_requests_total%s %s\n", labels, formatValue(series.Count))
	}

	writeHelpType(b, "hindsight_http_requests_in_progress", "Number of HTTP requests in progress", "gauge")
	for _, series := range snap.InProgress {
		labels := methodEndpointLabels(series.Method, series.Endpoint)
		fmt.Fprintf(b, "hindsight_http_requests_in_progress%s %d\n", labels, series.Count)
	}

	writeHelpType(b, "hindsight_http_duration_seconds", "Duration of HTTP requests in seconds", "histogram")
	for _, series := range snap.Requests {
		labels := requestLabels(series, true)
		for i, boundary := range httpx.HTTPDurationBuckets {
			count := float64(0)
			if i < len(series.Buckets) {
				count = series.Buckets[i]
			}
			fmt.Fprintf(b, "hindsight_http_duration_seconds_bucket%s %s\n",
				requestLabelsWithLe(series, formatBoundary(boundary)), formatValue(count))
		}
		fmt.Fprintf(b, "hindsight_http_duration_seconds_bucket%s %s\n",
			requestLabelsWithLe(series, "+Inf"), formatValue(float64(series.DurationCount)))
		fmt.Fprintf(b, "hindsight_http_duration_seconds_sum%s %s\n", labels, formatValue(series.DurationSum))
		fmt.Fprintf(b, "hindsight_http_duration_seconds_count%s %s\n", labels, formatValue(float64(series.DurationCount)))
	}
}

// requestLabels renders the four request labels, optionally including the
// status code and class.
func requestLabels(series httpx.HTTPRequestSeries, withStatus bool) string {
	return requestLabelString(series.Method, series.Endpoint, series.StatusCode, series.StatusClass, "", withStatus)
}

// requestLabelsWithLe renders the request labels plus the histogram's le bound.
func requestLabelsWithLe(series httpx.HTTPRequestSeries, le string) string {
	return requestLabelString(series.Method, series.Endpoint, series.StatusCode, series.StatusClass, le, true)
}

// methodEndpointLabels renders the two labels the in-progress counter carries.
func methodEndpointLabels(method, endpoint string) string {
	return requestLabelString(method, endpoint, "", "", "", false)
}

func requestLabelString(method, endpoint, statusCode, statusClass, le string, withStatus bool) string {
	labels := map[string]string{
		"method":   method,
		"endpoint": endpoint,
	}
	if withStatus {
		labels["status_code"] = statusCode
		labels["status_class"] = statusClass
	}
	if le != "" {
		labels["le"] = le
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(labels[name]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// escapeLabelValue escapes a Prometheus label value.
func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

// formatValue renders a sample value without an exponent for the magnitudes
// counters and durations reach.
func formatValue(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

// formatBoundary renders a histogram boundary the way the exposition format
// spells it.
func formatBoundary(boundary float64) string {
	return strconv.FormatFloat(boundary, 'g', -1, 64)
}

func writeHelpType(b *strings.Builder, name, help, kind string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, kind)
}

func writeGauge(b *strings.Builder, name, help string, value float64, extraLabels map[string]string) {
	writeHelpType(b, name, help, "gauge")
	fmt.Fprintf(b, "%s%s %s\n", name, requestLabelStringMap(extraLabels), formatValue(value))
}

// requestLabelStringMap renders a label set, or nothing when empty.
func requestLabelStringMap(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}
	names := make([]string, 0, len(labels))
	for name := range labels {
		names = append(names, name)
	}
	sort.Strings(names)
	var b strings.Builder
	b.WriteByte('{')
	for i, name := range names {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(name)
		b.WriteString(`="`)
		b.WriteString(escapeLabelValue(labels[name]))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}
