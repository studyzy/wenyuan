package oteltrace

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"testing"
	"time"
)

// capture collects the export payloads a fake collector receives.
type capture struct {
	mu       sync.Mutex
	requests []map[string]any
	headers  []http.Header
	status   int
	attempts int
}

func (c *capture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	if c.status != 0 {
		c.mu.Lock()
		c.attempts++
		c.mu.Unlock()
		w.WriteHeader(c.status)
		return
	}
	var decoded map[string]any
	_ = json.Unmarshal(body, &decoded)
	c.mu.Lock()
	c.requests = append(c.requests, decoded)
	c.headers = append(c.headers, r.Header.Clone())
	c.mu.Unlock()
	w.WriteHeader(http.StatusOK)
}

// spanFromPayload digs the first span out of an OTLP JSON payload.
func spanFromPayload(t *testing.T, payload map[string]any) map[string]any {
	t.Helper()
	resourceSpans, ok := payload["resourceSpans"].([]any)
	if !ok || len(resourceSpans) == 0 {
		t.Fatalf("payload has no resourceSpans: %v", payload)
	}
	scopeSpans := resourceSpans[0].(map[string]any)["scopeSpans"].([]any)
	spans := scopeSpans[0].(map[string]any)["spans"].([]any)
	if len(spans) == 0 {
		t.Fatal("payload has no spans")
	}
	return spans[0].(map[string]any)
}

// A finished span reaches the collector with method, timing and attributes.
func TestTracerExportsSpan(t *testing.T) {
	collector := &capture{}
	server := httptest.NewServer(collector)
	defer server.Close()

	tracer := New(Config{
		Endpoint:    server.URL,
		Headers:     map[string]string{"Authorization": "Bearer token"},
		ServiceName: "wenyuan-test",
	}, WithQueueSize(4))
	start := time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC)
	tracer.Emit(Span{
		TraceID: NewTraceID(), SpanID: NewSpanID(), Name: "chat",
		Start: start, End: start.Add(2 * time.Second),
		Attributes: map[string]any{
			"gen_ai.system":              "openai",
			"gen_ai.usage.output_tokens": int64(42),
			"gen_ai.usage.input_tokens":  int64(7),
		},
		StatusCode: StatusOK,
	})
	tracer.Close()

	collector.mu.Lock()
	defer collector.mu.Unlock()
	if len(collector.requests) != 1 {
		t.Fatalf("collector saw %d exports, want 1", len(collector.requests))
	}
	if got := collector.headers[0].Get("Authorization"); got != "Bearer token" {
		t.Fatalf("authorization header = %q", got)
	}
	span := spanFromPayload(t, collector.requests[0])
	if span["name"] != "chat" {
		t.Fatalf("span name = %v", span["name"])
	}
	if span["startTimeUnixNano"] != strconv.FormatInt(start.UnixNano(), 10) {
		t.Fatalf("start = %v", span["startTimeUnixNano"])
	}
	status := span["status"].(map[string]any)
	if status["code"] != float64(StatusOK) {
		t.Fatalf("status = %v", status)
	}
	attrs := map[string]any{}
	for _, raw := range span["attributes"].([]any) {
		attr := raw.(map[string]any)
		value := attr["value"].(map[string]any)
		if text, ok := value["stringValue"]; ok {
			attrs[attr["key"].(string)] = text
		}
		if number, ok := value["intValue"]; ok {
			attrs[attr["key"].(string)] = number
		}
	}
	if attrs["gen_ai.system"] != "openai" {
		t.Fatalf("gen_ai.system = %v", attrs["gen_ai.system"])
	}
	if attrs["gen_ai.usage.output_tokens"] != "42" {
		t.Fatalf("output tokens attribute = %v", attrs["gen_ai.usage.output_tokens"])
	}
}

// A bare collector address is completed with the OTLP traces path, and a
// path-bearing endpoint is used as given.
func TestExportURLCompletesPath(t *testing.T) {
	cases := map[string]string{
		"http://collector:4318":                      "http://collector:4318/v1/traces",
		"http://collector:4318/":                     "http://collector:4318/v1/traces",
		"https://cloud.langfuse.com/api/public/otel": "https://cloud.langfuse.com/api/public/otel/v1/traces",
		"http://collector:4318/v1/traces":            "http://collector:4318/v1/traces",
		"":                                           "",
	}
	for endpoint, want := range cases {
		if got := exportURL(endpoint); got != want {
			t.Errorf("exportURL(%q) = %q, want %q", endpoint, got, want)
		}
	}
}

// A full queue sheds spans instead of growing without bound, and counts them.
func TestTracerShedsWhenQueueFull(t *testing.T) {
	tracer := New(Config{Endpoint: "http://127.0.0.1:1"}, WithQueueSize(1))
	// The first span may leave the queue immediately; emit enough that the
	// queue is provably full at some point.
	for range 64 {
		tracer.Emit(Span{Name: "chat", Start: time.Now(), End: time.Now()})
	}
	if tracer.Dropped() == 0 {
		t.Fatal("a saturated queue must shed spans")
	}
	tracer.Close()
	// A closed tracer accepts no further spans and does not panic.
	tracer.Emit(Span{Name: "chat"})
}

// An export failure is reported to the logger rather than retried forever.
func TestTracerSurvivesCollectorFailure(t *testing.T) {
	collector := &capture{status: http.StatusInternalServerError}
	server := httptest.NewServer(collector)
	defer server.Close()
	tracer := New(Config{Endpoint: server.URL}, WithQueueSize(4))
	tracer.Emit(Span{Name: "chat", Start: time.Now(), End: time.Now()})
	tracer.Close()
	collector.mu.Lock()
	defer collector.mu.Unlock()
	if collector.attempts == 0 {
		t.Fatal("a rejecting collector should still have been sent the batch")
	}
	if len(collector.requests) != 0 {
		t.Fatal("a rejected export must not be recorded as delivered")
	}
}

// The exporter posts to the resolved URL with the JSON content type.
func TestExporterPostsJSON(t *testing.T) {
	var gotType string
	var gotPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotType = r.Header.Get("Content-Type")
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	exp := newExporter(Config{Endpoint: server.URL})
	err := exp.export(context.Background(), []Span{{
		TraceID: NewTraceID(), SpanID: NewSpanID(), Name: "chat",
		Start: time.Now(), End: time.Now(),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if gotType != "application/json" {
		t.Fatalf("content type = %q", gotType)
	}
	if gotPath != "/v1/traces" {
		t.Fatalf("path = %q", gotPath)
	}
}
