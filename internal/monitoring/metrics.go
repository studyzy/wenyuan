package monitoring

import (
	"crypto/rand"
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
)

// PrometheusContentType is the text exposition content type scrapers expect. It
// is the reference's OpenTelemetry Prometheus exporter content type, the 1.0.0
// text exposition, not the older 0.0.4 one.
const PrometheusContentType = "text/plain; version=1.0.0; charset=utf-8"

// MetricsScopeName is the OpenTelemetry instrumentation-scope name every
// wenyuan_* series carries as its otel_scope_name label, matching the
// reference's meter name.
const MetricsScopeName = "wenyuan_api.metrics"

// ServiceName and ServiceVersion identify this process in target_info, the
// metadata gauge the reference's exporter emits from its resource attributes.
const (
	ServiceName    = "hindsight-api"
	ServiceVersion = "1.0.0"
)

// Metric is one additional gauge or counter sample beyond the HTTP instruments.
// It is not necessarily unique by name: per-bank series share a name and differ
// in their Labels.
type Metric struct {
	// Name is the exposition metric name.
	Name string
	// Help is the HELP text; a blank value falls back to a generic one.
	Help string
	// Type is the exposition type, "gauge" or "counter"; blank means gauge.
	Type string
	// Value is the sample value.
	Value float64
	// Labels is the sample's label set; nil means none.
	Labels map[string]string
}

// Histogram is one additional histogram family: the bucket boundaries with their
// cumulative counts, plus the sum and count of every observation.
type Histogram struct {
	// Name is the exposition metric name. The buckets, sum and count render as
	// Name_bucket, Name_sum and Name_count.
	Name string
	// Help is the HELP text; a blank value falls back to a generic one.
	Help string
	// Buckets are the upper bounds in seconds, ascending.
	Buckets []float64
	// Counts are the cumulative counts, one per boundary. The +Inf bucket is
	// not listed here; it renders from Count.
	Counts []uint64
	// Sum is the sum of the observed values.
	Sum float64
	// Count is the number of observations.
	Count uint64
	// Labels is the family's label set; nil means none.
	Labels map[string]string
}

// MetricsHandler serves the Prometheus text exposition endpoint.
type MetricsHandler struct {
	// Recorder supplies the labelled HTTP request series; it may be nil.
	Recorder *httpx.MetricsRecorder
	// Extra supplies additional gauge/counter samples; it may be nil.
	Extra func() []Metric
	// Histograms supplies additional histogram families; it may be nil.
	Histograms func() []Histogram
	// Tenant, when non-empty, is added to every rendered instrument series
	// (but not the target_info metadata gauge) as a `tenant` label. A
	// deployment sets it to the database schema it is scoped to.
	Tenant string
	// Workers, when non-nil, makes one scrape cover every worker of a
	// multi-process server: the response is this process's own series read live
	// plus every live peer's latest snapshot, each series labelled with the
	// worker it came from. A nil value is the plain single-process exposition.
	Workers *WorkerMetrics
}

// Serve writes the metrics body in text exposition format.
//
// The body is rendered into one buffer and written with a length: the
// reference hands its framework a complete bytes object, so the response
// declares Content-Length. Writing the slices straight to the ResponseWriter
// streams them instead, and Go then answers with chunked transfer encoding and
// no length header — a scraping client that reads the length (or a proxy that
// buffers on it) sees a different response for the same bytes.
func (h *MetricsHandler) Serve(w http.ResponseWriter, r *http.Request) {
	body := h.Exposition()
	if h.Workers != nil {
		body = h.Workers.Render()
	}
	w.Header().Set("Content-Type", PrometheusContentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// Exposition renders this process's own metrics body in text exposition
// format. It is the registry a multi-process deployment publishes as this
// worker's snapshot, and the whole body when no worker label is configured.
func (h *MetricsHandler) Exposition() []byte {
	var b strings.Builder
	writeTargetInfo(&b)
	var snap httpx.MetricsSnapshot
	if h.Recorder != nil {
		snap = h.Recorder.Snapshot()
	}
	// The HTTP families render only from series the recorder has observed, as
	// the reference's registry does; the metadata gauge leads the body.
	writeHTTPRequests(&b, snap, h.Tenant)
	if h.Extra != nil {
		writeSamples(&b, sortMetrics(h.Extra()), h.Tenant)
	}
	if h.Histograms != nil {
		writeHistogramFamilies(&b, h.Histograms(), h.Tenant)
	}
	// The per-workload families the process has recorded — operations, LLM
	// calls, retain documents and so on — render after the deployment's own
	// extras. Like every other family they are absent until an observation
	// exists, so a scrape carries only what the process actually did.
	writeSamples(&b, Workload.Samples(), h.Tenant)
	writeHistogramFamilies(&b, Workload.Histograms(), h.Tenant)
	return []byte(b.String())
}

// serviceInstanceID identifies this process in target_info. It is generated once
// per process, so a scrape tells a restarted server from a live one.
var serviceInstanceID = newServiceInstanceID()

// newServiceInstanceID returns a random identifier stable for the process's
// lifetime. A read failure falls back to the service name rather than failing
// the exposition.
func newServiceInstanceID() string {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ServiceName
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[0:4], id[4:6], id[6:8], id[8:10], id[10:16])
}

// writeTargetInfo renders the metadata gauge the reference's exporter emits from
// its resource attributes. It describes the process rather than carrying a
// measurement, so it is always present with value 1. The telemetry_sdk_* labels
// name the SDK whose exporter shape this exposition follows.
func writeTargetInfo(b *strings.Builder) {
	writeHelpType(b, "target_info", "Target metadata", "gauge")
	labels := map[string]string{
		"service_instance_id":    serviceInstanceID,
		"service_name":           ServiceName,
		"service_version":        ServiceVersion,
		"telemetry_sdk_language": "go",
		"telemetry_sdk_name":     "opentelemetry",
		"telemetry_sdk_version":  "",
	}
	fmt.Fprintf(b, "target_info%s 1.0\n", requestLabelStringMap(labels))
}

// BindMetricsRoutes registers the metrics route.
func BindMetricsRoutes(h *MetricsHandler, handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.PathMetrics, h.Serve)
}

// writeHTTPRequests renders the request total, in-progress and duration series
// under the reference's instrument names.
func writeHTTPRequests(b *strings.Builder, snap httpx.MetricsSnapshot, tenant string) {
	// A family exists only once it has a sample, as the reference's registry
	// does: before the first request the whole HTTP trio is absent rather than
	// declared with no series.
	if len(snap.Requests) > 0 {
		writeHelpType(b, "wenyuan_http_requests_total", "Total number of HTTP requests", "counter")
		for _, series := range snap.Requests {
			labels := requestLabels(series, true, tenant)
			fmt.Fprintf(b, "wenyuan_http_requests_total%s %s\n", labels, formatValue(series.Count))
		}
	}

	// The in-progress counter's exposed name gains the unit suffix the
	// reference's instrument name takes from its declared unit "requests".
	if len(snap.InProgress) > 0 {
		writeHelpType(b, "wenyuan_http_requests_in_progress_requests", "Number of HTTP requests in progress", "gauge")
		for _, series := range snap.InProgress {
			labels := methodEndpointLabels(series.Method, series.Endpoint, tenant)
			fmt.Fprintf(b, "wenyuan_http_requests_in_progress_requests%s %s\n", labels, formatValue(float64(series.Count)))
		}
	}

	if len(snap.Requests) == 0 {
		return
	}
	writeHelpType(b, "wenyuan_http_duration_seconds", "Duration of HTTP requests in seconds", "histogram")
	for _, series := range snap.Requests {
		labels := requestLabels(series, true, tenant)
		for i, boundary := range httpx.HTTPDurationBuckets {
			count := float64(0)
			if i < len(series.Buckets) {
				count = series.Buckets[i]
			}
			fmt.Fprintf(b, "wenyuan_http_duration_seconds_bucket%s %s\n",
				requestLabelsWithLe(series, formatBoundary(boundary), tenant), formatValue(count))
		}
		fmt.Fprintf(b, "wenyuan_http_duration_seconds_bucket%s %s\n",
			requestLabelsWithLe(series, "+Inf", tenant), formatValue(float64(series.DurationCount)))
		fmt.Fprintf(b, "wenyuan_http_duration_seconds_count%s %s\n", labels, formatValue(float64(series.DurationCount)))
		fmt.Fprintf(b, "wenyuan_http_duration_seconds_sum%s %s\n", labels, formatValue(series.DurationSum))
	}
}

// writeSamples renders gauge/counter samples, emitting the HELP/TYPE header once
// per metric name. Samples must be pre-sorted by name.
func writeSamples(b *strings.Builder, samples []Metric, tenant string) {
	last := ""
	for _, sample := range samples {
		if sample.Name != last {
			kind := sample.Type
			if kind == "" {
				kind = "gauge"
			}
			writeHelpType(b, sample.Name, helpOr(sample.Help), kind)
			last = sample.Name
		}
		fmt.Fprintf(b, "%s%s %s\n", sample.Name,
			requestLabelStringMap(withScope(mergeTenant(sample.Labels, tenant))), formatValue(sample.Value))
	}
}

// writeHistogramFamilies renders each family's buckets, sum and count. The
// families must be sorted by name: the HELP/TYPE block is emitted once per
// name, so several label sets of one family share one declaration rather than
// repeating it before every series.
func writeHistogramFamilies(b *strings.Builder, families []Histogram, tenant string) {
	last := ""
	// The reference's registry renders its histogram families in name order (the
	// Prometheus exporter walks sorted instrument names), whatever order the
	// caller collected them in. Sorting by name and then by rendered label set
	// reproduces that order and keeps it stable across scrapes.
	ordered := make([]Histogram, len(families))
	copy(ordered, families)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Name != ordered[j].Name {
			return ordered[i].Name < ordered[j].Name
		}
		return requestLabelStringMap(ordered[i].Labels) < requestLabelStringMap(ordered[j].Labels)
	})
	for _, family := range ordered {
		if family.Name != last {
			writeHelpType(b, family.Name, helpOr(family.Help), "histogram")
			last = family.Name
		}
		labels := withScope(mergeTenant(family.Labels, tenant))
		for i, boundary := range family.Buckets {
			var count uint64
			if i < len(family.Counts) {
				count = family.Counts[i]
			}
			fmt.Fprintf(b, "%s_bucket%s %s\n", family.Name,
				requestLabelStringMap(withLabel(labels, "le", formatBoundary(boundary))), formatValue(float64(count)))
		}
		fmt.Fprintf(b, "%s_bucket%s %s\n", family.Name,
			requestLabelStringMap(withLabel(labels, "le", "+Inf")), formatValue(float64(family.Count)))
		fmt.Fprintf(b, "%s_count%s %s\n", family.Name, requestLabelStringMap(labels), formatValue(float64(family.Count)))
		fmt.Fprintf(b, "%s_sum%s %s\n", family.Name, requestLabelStringMap(labels), formatValue(family.Sum))
	}
}

// sortMetrics orders samples deterministically, by name then rendered label set,
// so a scrape of per-bank series is stable. The input is left untouched.
func sortMetrics(samples []Metric) []Metric {
	sorted := make([]Metric, len(samples))
	copy(sorted, samples)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Name != sorted[j].Name {
			return sorted[i].Name < sorted[j].Name
		}
		return requestLabelStringMap(sorted[i].Labels) < requestLabelStringMap(sorted[j].Labels)
	})
	return sorted
}

// requestLabels renders the four request labels, optionally including the
// status code and class.
func requestLabels(series httpx.HTTPRequestSeries, withStatus bool, tenant string) string {
	return requestLabelString(series.Method, series.Endpoint, series.StatusCode, series.StatusClass, "", withStatus, tenant)
}

// requestLabelsWithLe renders the request labels plus the histogram's le bound.
func requestLabelsWithLe(series httpx.HTTPRequestSeries, le string, tenant string) string {
	return requestLabelString(series.Method, series.Endpoint, series.StatusCode, series.StatusClass, le, true, tenant)
}

// methodEndpointLabels renders the two labels the in-progress counter carries.
func methodEndpointLabels(method, endpoint, tenant string) string {
	return requestLabelString(method, endpoint, "", "", "", false, tenant)
}

func requestLabelString(method, endpoint, statusCode, statusClass, le string, withStatus bool, tenant string) string {
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
	return requestLabelStringMap(withScope(mergeTenant(labels, tenant)))
}

// mergeTenant returns the labels with a tenant label added when one is set. It
// never mutates the input.
func mergeTenant(labels map[string]string, tenant string) map[string]string {
	if tenant == "" {
		return labels
	}
	out := make(map[string]string, len(labels)+1)
	for name, value := range labels {
		out[name] = value
	}
	out["tenant"] = tenant
	return out
}

// withLabel returns the labels with one label added, never mutating the input.
func withLabel(labels map[string]string, name, value string) map[string]string {
	out := make(map[string]string, len(labels)+1)
	for k, v := range labels {
		out[k] = v
	}
	out[name] = value
	return out
}

// withScope returns the labels with the OpenTelemetry instrumentation-scope
// labels every wenyuan_* series carries, never mutating the input. The
// reference's Prometheus exporter stamps otel_scope_name (the meter's module),
// otel_scope_version and otel_scope_schema_url onto each rendered series.
func withScope(labels map[string]string) map[string]string {
	out := make(map[string]string, len(labels)+3)
	for k, v := range labels {
		out[k] = v
	}
	out["otel_scope_name"] = MetricsScopeName
	out["otel_scope_schema_url"] = ""
	out["otel_scope_version"] = ""
	return out
}

// escapeLabelValue escapes a Prometheus label value.
func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	return value
}

// formatValue renders a sample value the way the reference's exposition does:
// CPython's repr of the float, then the reference exporter's one adjustment,
// which re-spells a positive value with more than six integer digits in
// exponent form ("2.8493856768e+010") because Go switches to exponents later
// than Python. A whole value therefore keeps the ".0" a scraper would not
// need but the reference's text carries.
func formatValue(value float64) string {
	switch {
	case math.IsNaN(value):
		return "NaN"
	case math.IsInf(value, 1):
		return "+Inf"
	case math.IsInf(value, -1):
		return "-Inf"
	}
	text := pythonFloat(value)
	if value > 0 {
		if dot := strings.IndexByte(text, '.'); dot > 6 {
			mantissa := strings.TrimRight(text[:1]+"."+text[1:dot]+text[dot+1:], "0.")
			return mantissa + "e+0" + strconv.Itoa(dot-1)
		}
	}
	return text
}

// formatBoundary renders a histogram boundary the way the exposition format
// spells it: CPython's str of the float, so a whole boundary keeps its ".0"
// ("1.0", not "1") and a boundary is the same label value on both sides.
func formatBoundary(boundary float64) string {
	return pythonFloat(boundary)
}

// pythonFloat renders a float the way CPython's repr does: the shortest
// decimal that round-trips, in fixed notation while the decimal exponent is in
// [-4, 16) and in scientific notation (signed, at least two exponent digits)
// outside it. FormatFloat's 'e' is used to read the exponent because Go's
// 'g'/'f' switch to exponents on different bounds than Python's repr.
func pythonFloat(value float64) string {
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	_, exponent, ok := strings.Cut(scientific, "e")
	if !ok {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	exp, err := strconv.Atoi(exponent)
	if err != nil || exp < -4 || exp >= 16 {
		return scientific
	}
	fixed := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}

func writeHelpType(b *strings.Builder, name, help, kind string) {
	fmt.Fprintf(b, "# HELP %s %s\n", name, help)
	fmt.Fprintf(b, "# TYPE %s %s\n", name, kind)
}

// helpOr substitutes a generic HELP when a producer leaves it blank.
func helpOr(help string) string {
	if help == "" {
		return "Deployment-provided metric."
	}
	return help
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
