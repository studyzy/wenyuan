// Package oteltrace exports OpenTelemetry spans over OTLP/HTTP with the JSON
// encoding.
//
// It is deliberately small: the service emits one span per model call carrying
// the GenAI semantic-convention attributes, and the payload is the subset of
// the OTLP JSON schema that shape needs. Speaking the wire protocol directly
// keeps the default build dependency-free, which matters because tracing is
// off unless a deployment turns it on.
package oteltrace

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SpanKindInternal is the OTLP span kind for an in-process operation such as a
// model call.
const (
	SpanKindInternal = 1
	spanKindClient   = 3
)

// StatusCode mirrors the OTLP status codes: 1 ok, 2 error.
const (
	StatusOK    = 1
	StatusError = 2
)

// Span is one finished unit of work to export.
type Span struct {
	// TraceID and SpanID are lowercase hex, 16 and 8 bytes respectively.
	TraceID string
	SpanID  string
	Name    string
	// Kind is the OTLP span kind; zero means client.
	Kind  int
	Start time.Time
	End   time.Time
	// Attributes are exported with their natural JSON type: string, bool,
	// int64, or float64.
	Attributes map[string]any
	// StatusCode is StatusOK or StatusError; zero omits the status.
	StatusCode int
	// StatusMessage accompanies an error status.
	StatusMessage string
}

// Config is the exporter's connection settings.
type Config struct {
	// Endpoint is the OTLP/HTTP endpoint. A bare collector address gets the
	// standard /v1/traces path appended; a path-bearing URL is used as given,
	// which is what Langfuse and other managed backends expect.
	Endpoint string
	// Headers are sent on every export request, e.g. an authorization token.
	Headers map[string]string
	// ServiceName and Environment become resource attributes.
	ServiceName string
	Environment string
	// Client is the HTTP client to use. A nil client gets a default one.
	Client *http.Client
}

// newSpanID returns a random lowercase hex identifier of n bytes.
func newSpanID(n int) string {
	var raw [16]byte
	if _, err := rand.Read(raw[:n]); err != nil {
		// crypto/rand does not fail in practice; a zero id is still exportable.
		return strings.Repeat("0", 2*n)
	}
	return hex.EncodeToString(raw[:n])
}

// NewTraceID returns a random 16-byte trace identifier.
func NewTraceID() string { return newSpanID(16) }

// NewSpanID returns a random 8-byte span identifier.
func NewSpanID() string { return newSpanID(8) }

// tracesPath is the OTLP/HTTP path a bare endpoint is completed with.
const tracesPath = "/v1/traces"

// exportURL resolves the endpoint the trace payload is posted to.
func exportURL(endpoint string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmed == "" {
		return ""
	}
	if strings.Contains(trimmed, "/v1/traces") || strings.HasSuffix(trimmed, "/traces") {
		return trimmed
	}
	return trimmed + tracesPath
}

// payload is the OTLP/HTTP JSON body for one batch of spans from one service.
type payload struct {
	ResourceSpans []resourceSpans `json:"resourceSpans"`
}

type resourceSpans struct {
	Resource   resource     `json:"resource"`
	ScopeSpans []scopeSpans `json:"scopeSpans"`
}

type resource struct {
	Attributes []attribute `json:"attributes"`
}

type scopeSpans struct {
	Scope scope  `json:"scope"`
	Spans []span `json:"spans"`
}

type scope struct {
	Name string `json:"name"`
}

type span struct {
	TraceID           string      `json:"traceId"`
	SpanID            string      `json:"spanId"`
	Name              string      `json:"name"`
	Kind              int         `json:"kind"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []attribute `json:"attributes,omitempty"`
	Status            *status     `json:"status,omitempty"`
}

type attribute struct {
	Key   string    `json:"key"`
	Value attrValue `json:"value"`
}

type attrValue struct {
	StringValue *string  `json:"stringValue,omitempty"`
	BoolValue   *bool    `json:"boolValue,omitempty"`
	IntValue    *string  `json:"intValue,omitempty"`
	DoubleValue *float64 `json:"doubleValue,omitempty"`
}

type status struct {
	Code    int    `json:"code"`
	Message string `json:"message,omitempty"`
}

// encodePayload renders the batch as OTLP JSON.
func encodePayload(cfg Config, spanList []Span) ([]byte, error) {
	spans := make([]span, 0, len(spanList))
	for _, s := range spanList {
		kind := s.Kind
		if kind == 0 {
			kind = spanKindClient
		}
		out := span{
			TraceID:           s.TraceID,
			SpanID:            s.SpanID,
			Name:              s.Name,
			Kind:              kind,
			StartTimeUnixNano: strconv.FormatInt(s.Start.UnixNano(), 10),
			EndTimeUnixNano:   strconv.FormatInt(s.End.UnixNano(), 10),
			Attributes:        encodeAttributes(s.Attributes),
		}
		if s.StatusCode != 0 {
			out.Status = &status{Code: s.StatusCode, Message: s.StatusMessage}
		}
		spans = append(spans, out)
	}
	service := cfg.ServiceName
	if service == "" {
		service = "wenyuan"
	}
	resourceAttrs := []attribute{stringAttribute("service.name", service)}
	if cfg.Environment != "" {
		resourceAttrs = append(resourceAttrs, stringAttribute("deployment.environment", cfg.Environment))
	}
	body := payload{ResourceSpans: []resourceSpans{{
		Resource:   resource{Attributes: resourceAttrs},
		ScopeSpans: []scopeSpans{{Scope: scope{Name: "wenyuan"}, Spans: spans}},
	}}}
	return json.Marshal(body)
}

// stringAttribute builds a string-valued attribute.
func stringAttribute(key, value string) attribute {
	return attribute{Key: key, Value: attrValue{StringValue: &value}}
}

// encodeAttributes renders attributes with their natural JSON type.
func encodeAttributes(attrs map[string]any) []attribute {
	if len(attrs) == 0 {
		return nil
	}
	out := make([]attribute, 0, len(attrs))
	for key, raw := range attrs {
		value := attrValue{}
		switch typed := raw.(type) {
		case string:
			value.StringValue = &typed
		case bool:
			value.BoolValue = &typed
		case int:
			encoded := strconv.FormatInt(int64(typed), 10)
			value.IntValue = &encoded
		case int64:
			encoded := strconv.FormatInt(typed, 10)
			value.IntValue = &encoded
		case float64:
			value.DoubleValue = &typed
		case nil:
			continue
		default:
			encoded := fmt.Sprint(typed)
			value.StringValue = &encoded
		}
		out = append(out, attribute{Key: key, Value: value})
	}
	return out
}

// exporter posts span batches to the OTLP endpoint.
type exporter struct {
	cfg    Config
	client *http.Client
}

// newExporter builds an exporter, defaulting the HTTP client.
func newExporter(cfg Config) *exporter {
	client := cfg.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &exporter{cfg: cfg, client: client}
}

// export posts one batch. The caller has already decided the failure is
// non-fatal, so the error is returned only to be logged.
func (e *exporter) export(ctx context.Context, spans []Span) error {
	url := exportURL(e.cfg.Endpoint)
	if url == "" || len(spans) == 0 {
		return nil
	}
	body, err := encodePayload(e.cfg, spans)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for key, value := range e.cfg.Headers {
		req.Header.Set(key, value)
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("otlp export returned status %d", resp.StatusCode)
	}
	return nil
}
