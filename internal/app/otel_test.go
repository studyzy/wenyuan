package app

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"

	"github.com/studyzy/wenyuan/internal/oteltrace"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/mock"
)

// spanCollector records the spans a fake OTLP collector is sent.
type spanCollector struct {
	mu    sync.Mutex
	spans []map[string]any
}

func (c *spanCollector) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)
	c.mu.Lock()
	defer c.mu.Unlock()
	resourceSpans, _ := payload["resourceSpans"].([]any)
	for _, rawRS := range resourceSpans {
		scopeSpans, _ := rawRS.(map[string]any)["scopeSpans"].([]any)
		for _, rawSS := range scopeSpans {
			spans, _ := rawSS.(map[string]any)["spans"].([]any)
			for _, rawSpan := range spans {
				if s, ok := rawSpan.(map[string]any); ok {
					c.spans = append(c.spans, s)
				}
			}
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (c *spanCollector) first(t *testing.T) map[string]any {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.spans) == 0 {
		t.Fatal("collector received no span")
	}
	return c.spans[0]
}

// attributeStrings flattens a span's string attributes.
func attributeStrings(span map[string]any) map[string]string {
	out := map[string]string{}
	attrs, _ := span["attributes"].([]any)
	for _, raw := range attrs {
		attr, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		value, ok := attr["value"].(map[string]any)
		if !ok {
			continue
		}
		if text, ok := value["stringValue"].(string); ok {
			out[attr["key"].(string)] = text
		}
	}
	return out
}

// A wrapped provider emits one GenAI span per completion.
func TestTraceSpansEmitsCompletionSpan(t *testing.T) {
	collector := &spanCollector{}
	server := httptest.NewServer(collector)
	defer server.Close()

	tracer := oteltrace.New(oteltrace.Config{Endpoint: server.URL, ServiceName: "wenyuan-test"})
	wrapped := TraceSpans(&mock.Provider{DimensionsValue: 4}, tracer)
	if wrapped == nil {
		t.Fatal("a non-nil provider and tracer must wrap")
	}
	temperature := 0.2
	result, err := wrapped.Complete(context.Background(), provider.CompletionRequest{
		Messages:    []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
		MaxTokens:   128,
		Temperature: &temperature,
		Scope:       "reflect",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hello" {
		t.Fatalf("completion = %q", result.Content)
	}
	tracer.Close()

	span := collector.first(t)
	if span["name"] != "chat" {
		t.Fatalf("span name = %v", span["name"])
	}
	attrs := attributeStrings(span)
	if attrs["gen_ai.system"] != mock.NameMock {
		t.Fatalf("gen_ai.system = %q", attrs["gen_ai.system"])
	}
	if attrs["wenyuan.scope"] != "reflect" {
		t.Fatalf("scope attribute = %q", attrs["wenyuan.scope"])
	}
	status, _ := span["status"].(map[string]any)
	if status["code"] != float64(oteltrace.StatusOK) {
		t.Fatalf("status = %v", status)
	}
}

// With tracing off the provider is returned unwrapped and unchanged.
func TestTraceSpansDisabled(t *testing.T) {
	inner := &mock.Provider{DimensionsValue: 4}
	if got := TraceSpans(inner, nil); got != provider.Provider(inner) {
		t.Fatal("a nil tracer must leave the provider unwrapped")
	}
	if got := NewTracer(config.TracingConfig{}); got != nil {
		t.Fatal("a disabled configuration must build no tracer")
	}
}
