package httpx

import (
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
)

// EndpointUnmatched is the endpoint label recorded when no route matches the
// request path. It mirrors the reference's fallback for an unmatched request.
const EndpointUnmatched = "unmatched"

// HTTPDurationBuckets are the histogram boundaries, in seconds, the reference
// uses for HTTP request latency: finer near zero, where most endpoints finish.
var HTTPDurationBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0, 30.0}

// requestKey labels the in-progress counter: method and route template.
type requestKey struct {
	Method   string
	Endpoint string
}

// requestStatusKey labels the request total and duration histogram: the
// in-progress labels plus the numeric status and its class.
type requestStatusKey struct {
	Method      string
	Endpoint    string
	StatusCode  string
	StatusClass string
}

// MetricsRecorder accumulates the labelled HTTP request series: a total
// counter keyed by method/endpoint/status_code/status_class, an in-progress
// up-down counter keyed by method/endpoint, and a duration histogram carrying
// the counter's labels.
type MetricsRecorder struct {
	mu         sync.Mutex
	totals     map[requestStatusKey]float64
	inProgress map[requestKey]int64
	buckets    map[requestStatusKey][]float64
	sums       map[requestStatusKey]float64
	counts     map[requestStatusKey]uint64
}

// addInProgress adjusts the in-progress depth for a method and route template.
func (m *MetricsRecorder) addInProgress(method, endpoint string, delta int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.inProgress == nil {
		m.inProgress = map[requestKey]int64{}
	}
	m.inProgress[requestKey{method, endpoint}] += delta
}

// Observe records one completed request's count and duration under the labels
// the reference uses.
func (m *MetricsRecorder) Observe(method, endpoint string, status int, duration time.Duration) {
	key := requestStatusKey{
		Method:      method,
		Endpoint:    endpoint,
		StatusCode:  strconv.Itoa(status),
		StatusClass: statusClass(status),
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.totals == nil {
		m.totals = map[requestStatusKey]float64{}
		m.buckets = map[requestStatusKey][]float64{}
		m.sums = map[requestStatusKey]float64{}
		m.counts = map[requestStatusKey]uint64{}
	}
	m.totals[key]++
	buckets, ok := m.buckets[key]
	if !ok {
		buckets = make([]float64, len(HTTPDurationBuckets))
		m.buckets[key] = buckets
	}
	seconds := duration.Seconds()
	for i, boundary := range HTTPDurationBuckets {
		if seconds <= boundary {
			buckets[i]++
		}
	}
	m.sums[key] += seconds
	m.counts[key]++
}

// statusClass renders a status code's class label, e.g. "2xx".
func statusClass(status int) string {
	return strconv.Itoa(status/100) + "xx"
}

// HTTPRequestSeries is one rendered request total/duration series, keyed by its
// labels.
type HTTPRequestSeries struct {
	Method      string
	Endpoint    string
	StatusCode  string
	StatusClass string
	// Count is the total counter value.
	Count float64
	// DurationCount is how many observations the histogram recorded.
	DurationCount uint64
	// DurationSum is the summed latency in seconds.
	DurationSum float64
	// Buckets holds the cumulative count at each HTTPDurationBuckets boundary,
	// in the same order.
	Buckets []float64
}

// HTTPInProgressSeries is one rendered in-progress series.
type HTTPInProgressSeries struct {
	Method   string
	Endpoint string
	Count    int64
}

// MetricsSnapshot is a consistent copy of the recorder's series, sorted so the
// rendered body is deterministic.
type MetricsSnapshot struct {
	Requests   []HTTPRequestSeries
	InProgress []HTTPInProgressSeries
}

// Snapshot copies the accumulated series out of the recorder.
func (m *MetricsRecorder) Snapshot() MetricsSnapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	snap := MetricsSnapshot{}
	for key, count := range m.totals {
		buckets := append([]float64(nil), m.buckets[key]...)
		snap.Requests = append(snap.Requests, HTTPRequestSeries{
			Method:        key.Method,
			Endpoint:      key.Endpoint,
			StatusCode:    key.StatusCode,
			StatusClass:   key.StatusClass,
			Count:         count,
			DurationCount: m.counts[key],
			DurationSum:   m.sums[key],
			Buckets:       buckets,
		})
	}
	sort.Slice(snap.Requests, func(i, j int) bool {
		a, b := snap.Requests[i], snap.Requests[j]
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		if a.Endpoint != b.Endpoint {
			return a.Endpoint < b.Endpoint
		}
		if a.StatusCode != b.StatusCode {
			return a.StatusCode < b.StatusCode
		}
		return a.StatusClass < b.StatusClass
	})
	for key, count := range m.inProgress {
		snap.InProgress = append(snap.InProgress, HTTPInProgressSeries{Method: key.Method, Endpoint: key.Endpoint, Count: count})
	}
	sort.Slice(snap.InProgress, func(i, j int) bool {
		a, b := snap.InProgress[i], snap.InProgress[j]
		if a.Method != b.Method {
			return a.Method < b.Method
		}
		return a.Endpoint < b.Endpoint
	})
	return snap
}

// statusCapturingWriter remembers the status a handler committed so the
// metrics middleware can label the request with it. The zero value is treated
// as 500, so a request that dies before any response start is recorded as a
// failure rather than quietly dropped.
type statusCapturingWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusCapturingWriter) WriteHeader(status int) {
	if !w.written {
		w.status = status
		w.written = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusCapturingWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(b)
}

// Flush forwards to the underlying writer so streaming handlers keep working
// through the metrics wrapper.
func (w *statusCapturingWriter) Flush() {
	if !w.written {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

// MetricsMiddleware records the labelled request series. The endpoint label is
// resolved before the request runs, so the in-progress counter is incremented
// and decremented under the same label.
func MetricsMiddleware(recorder *MetricsRecorder, resolve func(*http.Request) string) Middleware {
	return func(next http.Handler) http.Handler {
		if recorder == nil {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			endpoint := EndpointUnmatched
			if resolve != nil {
				endpoint = resolve(r)
			}
			method := r.Method
			recorder.addInProgress(method, endpoint, 1)
			captured := &statusCapturingWriter{ResponseWriter: w, status: http.StatusInternalServerError}
			start := time.Now()
			defer func() {
				recorder.addInProgress(method, endpoint, -1)
				recorder.Observe(method, endpoint, captured.status, time.Since(start))
			}()
			next.ServeHTTP(captured, r)
		})
	}
}
