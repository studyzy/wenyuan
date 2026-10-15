// Package cli implements the wenyuan command-line client's shared
// infrastructure: configuration and named profiles, the HTTP client with its
// error classifier, and the output framework that later batches register
// resource commands on.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Version is the CLI's own version, reported in the User-Agent.
const Version = "0.1.0"

// UserAgent is the User-Agent header sent with every request.
const UserAgent = "wenyuan-cli/" + Version

// DefaultTimeout bounds one request.
const DefaultTimeout = 120 * time.Second

// HTTPDoer is the transport seam. Tests inject a local double so no test
// reaches a live endpoint.
type HTTPDoer interface {
	Do(req *http.Request) (*http.Response, error)
}

// Client performs requests against the API server. It holds the resolved
// connection settings and nothing else.
type Client struct {
	// BaseURL is the resolved API URL.
	BaseURL string
	// APIKey is the bearer credential; empty sends no Authorization header.
	APIKey string
	// HTTP performs requests; nil means a default client with DefaultTimeout.
	HTTP HTTPDoer
	// Debug, when non-nil, receives full request and response dumps
	// (--verbose).
	Debug io.Writer
}

// NewClient builds a client over resolved settings.
func NewClient(baseURL, apiKey string) *Client {
	return &Client{BaseURL: baseURL, APIKey: apiKey}
}

func (c *Client) doer() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: DefaultTimeout}
}

// APIError is a non-2xx response. Its message is the exact shape the error
// classifier's split logic depends on: "API request failed ({status}): {body}"
// with a body, or "API request failed ({status})" without one.
type APIError struct {
	StatusCode int
	Status     string // wire status line, e.g. "404 Not Found"
	Body       string
}

// Error renders the shape the classifier splits on.
func (e *APIError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("API request failed (%s)", e.Status)
	}
	return fmt.Sprintf("API request failed (%s): %s", e.Status, e.Body)
}

// DecodeError is a 2xx payload that failed JSON decoding. Its message starts
// with the sentinel the classifier keys on.
type DecodeError struct {
	Reason string
	Body   string
}

// Error renders the sentinel shape.
func (e *DecodeError) Error() string {
	return fmt.Sprintf("Invalid response payload: %s\nResponse was: %s", e.Reason, e.Body)
}

// Do executes a request. A nil body sends none; a nil out skips decoding.
// Transport and non-2xx failures surface as errors whose text feeds the
// classifier.
func (c *Client) Do(ctx context.Context, method, path string, body, out any) error {
	data, err := c.send(ctx, method, path, body)
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(data)}
	}
	return nil
}

// GetText performs a GET and returns the raw body, for text endpoints such
// as the Prometheus metrics surface.
func (c *Client) GetText(ctx context.Context, path string) (string, error) {
	data, err := c.send(ctx, "GET", path, nil)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func (c *Client) send(ctx context.Context, method, path string, body any) ([]byte, error) {
	var payload io.Reader
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return nil, err
		}
		payload = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, joinURL(c.BaseURL, path), payload)
	if err != nil {
		return nil, fmt.Errorf("invalid URL: %w", err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.APIKey)
	}
	req.Header.Set("User-Agent", UserAgent)
	c.debugRequest(req, raw)

	resp, err := c.doer().Do(req)
	if err != nil {
		// The raw transport error passes through: its text carries the
		// connection/timeout/DNS markers the classifier matches.
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	c.debugResponse(resp, data)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{StatusCode: resp.StatusCode, Status: resp.Status}
		if trimmed := strings.TrimSpace(string(data)); trimmed != "" {
			apiErr.Body = trimmed
		}
		return nil, apiErr
	}
	return data, nil
}

// joinURL appends the request path to the base URL, tolerating a trailing
// slash on either side.
func joinURL(base, path string) string {
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func (c *Client) debugRequest(req *http.Request, body []byte) {
	if c.Debug == nil {
		return
	}
	_, _ = fmt.Fprintf(c.Debug, "> %s %s\n", req.Method, req.URL)
	for k, v := range req.Header {
		_, _ = fmt.Fprintf(c.Debug, "> %s: %s\n", k, strings.Join(v, ", "))
	}
	if len(body) > 0 {
		_, _ = fmt.Fprintf(c.Debug, "> %s\n", body)
	}
}

func (c *Client) debugResponse(resp *http.Response, body []byte) {
	if c.Debug == nil {
		return
	}
	_, _ = fmt.Fprintf(c.Debug, "< %s\n", resp.Status)
	_, _ = fmt.Fprintf(c.Debug, "< %s\n", body)
}
