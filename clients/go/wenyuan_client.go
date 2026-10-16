// Package wenyuan is a Go client for the WenYuan agent-memory HTTP API.
//
// The API surface (services and models) is generated from the OpenAPI document
// this repository serves; this file and the module metadata are maintained by
// hand. Client is a thin convenience layer over the generated *APIClient: a
// constructor that reads the server URL from the environment, plus one method
// per core operation (retain, recall, reflect, get/create bank).
package wenyuan

import (
	"context"
	"net/http"
	"os"
	"runtime/debug"
	"time"
)

const (
	// BaseURLEnvVar names the environment variable the default constructor
	// reads the server base URL from.
	BaseURLEnvVar = "HINDSIGHT_API_URL"

	// DefaultBaseURL is used when BaseURLEnvVar is unset.
	DefaultBaseURL = "http://localhost:18888"
)

// defaultUserAgent returns the User-Agent sent on every request unless the
// caller overrides cfg.UserAgent. The version comes from build info so it
// tracks the module version, falling back to "devel" for an unpinned checkout.
func defaultUserAgent() string {
	version := "devel"
	if info, ok := debug.ReadBuildInfo(); ok {
		for _, dep := range info.Deps {
			if dep.Path == "github.com/studyzy/wenyuan/clients/go" {
				version = dep.Version
				break
			}
		}
	}
	return "wenyuan-client-go/" + version
}

// DefaultUserAgent is the User-Agent sent on every request unless the caller
// overrides cfg.UserAgent.
var DefaultUserAgent = defaultUserAgent()

// Client is the generated API client plus the convenience methods below. The
// embedded *APIClient exposes every generated service and model for callers
// that need something beyond the core operations.
type Client struct {
	*APIClient
}

// BaseURL returns BaseURLEnvVar if it is set and non-empty, else DefaultBaseURL.
func BaseURL() string {
	if u := os.Getenv(BaseURLEnvVar); u != "" {
		return u
	}
	return DefaultBaseURL
}

// NewClient builds a client against BaseURL() with no credentials.
func NewClient() *Client {
	return NewClientWithBaseURL(BaseURL())
}

// NewClientWithBaseURL builds a client against an explicit base URL with no
// credentials.
func NewClientWithBaseURL(baseURL string) *Client {
	return NewClientWithToken(baseURL, "")
}

// NewClientWithToken builds a client that sends token as a Bearer credential on
// every request. An empty token adds no Authorization header. The returned
// client uses http.DefaultClient (no timeout); use NewClientWithTimeout to bound
// requests.
func NewClientWithToken(baseURL, token string) *Client {
	return NewClientWithTimeout(baseURL, token, 0)
}

// NewClientWithTimeout is NewClientWithToken with a per-request timeout. A
// timeout of 0 leaves the client without one.
func NewClientWithTimeout(baseURL, token string, timeout time.Duration) *Client {
	cfg := NewConfiguration()
	cfg.UserAgent = DefaultUserAgent
	cfg.Servers = ServerConfigurations{{URL: baseURL}}
	if token != "" {
		cfg.AddDefaultHeader("Authorization", "Bearer "+token)
	}
	if timeout > 0 {
		cfg.HTTPClient = &http.Client{Timeout: timeout}
	}
	return &Client{APIClient: NewAPIClient(cfg)}
}

// TextContent wraps a plain string as a retain item's content. The generated
// Content type is a union (a string or an ordered list of content blocks), so
// a bare string does not satisfy it; this restores the common case:
//
//	RetainRequest{Items: []MemoryItem{{Content: wenyuan.TextContent("...")}}}
func TextContent(text string) Content {
	return Content{String: &text}
}

// Retain stores items in a bank and returns the retain result. The underlying
// HTTP response body is closed before returning.
func (c *Client) Retain(ctx context.Context, bankID string, req RetainRequest) (*RetainResponse, error) {
	resp, httpResp, err := c.MemoryAPI.RetainMemories(ctx, bankID).RetainRequest(req).Execute()
	closeBody(httpResp)
	return resp, err
}

// Recall retrieves memories for a query and returns the matched results. The
// underlying HTTP response body is closed before returning.
func (c *Client) Recall(ctx context.Context, bankID string, req RecallRequest) (*RecallResponse, error) {
	resp, httpResp, err := c.MemoryAPI.RecallMemories(ctx, bankID).RecallRequest(req).Execute()
	closeBody(httpResp)
	return resp, err
}

// Reflect synthesises an answer from a bank's memories. The underlying HTTP
// response body is closed before returning.
func (c *Client) Reflect(ctx context.Context, bankID string, req ReflectRequest) (*ReflectResponse, error) {
	resp, httpResp, err := c.MemoryAPI.Reflect(ctx, bankID).ReflectRequest(req).Execute()
	closeBody(httpResp)
	return resp, err
}

// CreateBank creates or updates a bank. The underlying HTTP response body is
// closed before returning.
func (c *Client) CreateBank(ctx context.Context, bankID string, req CreateBankRequest) (*BankProfileResponse, error) {
	resp, httpResp, err := c.BanksAPI.CreateOrUpdateBank(ctx, bankID).CreateBankRequest(req).Execute()
	closeBody(httpResp)
	return resp, err
}

// GetBankConfig fetches a bank's resolved configuration. The underlying HTTP
// response body is closed before returning.
//
// This reads the configuration endpoint rather than the profile one on purpose:
// the contract retires `GET .../profile`, `PUT .../profile` and
// `POST .../background` (the generated client still carries them, marked
// "removed"), and a server answers them with 410 Gone. Use this for what the
// retired profile read answered.
func (c *Client) GetBankConfig(ctx context.Context, bankID string) (*BankConfigResponse, error) {
	resp, httpResp, err := c.BanksAPI.GetBankConfig(ctx, bankID).Execute()
	closeBody(httpResp)
	return resp, err
}

func closeBody(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}
