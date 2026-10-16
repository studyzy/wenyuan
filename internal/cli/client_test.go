package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// recordingDoer captures the request for header assertions.
type recordingDoer struct {
	req *http.Request
}

func (r *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	r.req = req
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(`{}`))}, nil
}

func TestClientHeaders(t *testing.T) {
	t.Run("bearer and user agent with a key", func(t *testing.T) {
		rec := &recordingDoer{}
		c := &Client{BaseURL: "http://api:1/", APIKey: "secret", HTTP: rec}
		if err := c.Do(context.Background(), "POST", "/v1/x", map[string]string{"a": "b"}, nil); err != nil {
			t.Fatal(err)
		}
		if got := rec.req.Header.Get("Authorization"); got != "Bearer secret" {
			t.Errorf("Authorization = %q, want bearer", got)
		}
		if got := rec.req.Header.Get("User-Agent"); got != UserAgent {
			t.Errorf("User-Agent = %q, want %q", got, UserAgent)
		}
		if got := rec.req.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q", got)
		}
		if got := rec.req.URL.String(); got != "http://api:1/v1/x" {
			t.Errorf("URL = %q, want the trailing slash tolerated", got)
		}
	})
	t.Run("no key means no header", func(t *testing.T) {
		rec := &recordingDoer{}
		c := &Client{BaseURL: "http://api:1", HTTP: rec}
		if err := c.Do(context.Background(), "GET", "/health", nil, nil); err != nil {
			t.Fatal(err)
		}
		if _, ok := rec.req.Header["Authorization"]; ok {
			t.Error("Authorization header sent without a key")
		}
	})
}

func TestClientErrorShapes(t *testing.T) {
	cases := []struct {
		name       string
		status     int
		statusText string
		body       string
		wantText   string
	}{
		{
			name: "non-2xx with a body", status: 404, statusText: "404 Not Found",
			body:     `{"detail":"gone"}`,
			wantText: `API request failed (404 Not Found): {"detail":"gone"}`,
		},
		{
			name: "non-2xx with an empty body", status: 500, statusText: "500 Internal Server Error",
			wantText: "API request failed (500 Internal Server Error)",
		},
		{
			name: "non-2xx with a whitespace body", status: 503, statusText: "503 Service Unavailable",
			body:     "   \n",
			wantText: "API request failed (503 Service Unavailable)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer srv.Close()
			c := NewClient(srv.URL, "")
			err := c.Do(context.Background(), "GET", "/x", nil, nil)
			var apiErr *APIError
			if !errors.As(err, &apiErr) {
				t.Fatalf("error = %v, want an APIError", err)
			}
			if apiErr.Error() != tc.wantText {
				t.Errorf("Error() = %q, want %q", apiErr.Error(), tc.wantText)
			}
			if apiErr.StatusCode != tc.status {
				t.Errorf("StatusCode = %d, want %d", apiErr.StatusCode, tc.status)
			}
		})
	}
}

func TestClientDecodeError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "<html>not json</html>")
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	var out map[string]any
	err := c.Do(context.Background(), "GET", "/x", nil, &out)
	var dec *DecodeError
	if !errors.As(err, &dec) {
		t.Fatalf("error = %v, want a DecodeError", err)
	}
	if !strings.HasPrefix(dec.Error(), "Invalid response payload: ") || !strings.Contains(dec.Error(), "\nResponse was: <html>") {
		t.Errorf("Error() = %q", dec.Error())
	}
}

func TestClientGetText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "metric 1\n")
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")
	text, err := c.GetText(context.Background(), "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	if text != "metric 1\n" {
		t.Errorf("text = %q", text)
	}
}

func TestClientConnectionRefused(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	url := srv.URL
	srv.Close()
	c := NewClient(url, "")
	err := c.Do(context.Background(), "GET", "/health", nil, nil)
	if err == nil {
		t.Fatal("expected a dial error")
	}
	classified := Classify(err, url)
	if classified.Kind != KindConnection {
		t.Fatalf("kind = %d, want connection", classified.Kind)
	}
}

func TestClientInvalidURL(t *testing.T) {
	c := NewClient("http://exa mple.invalid", "")
	err := c.Do(context.Background(), "GET", "/x", nil, nil)
	if err == nil || !strings.HasPrefix(err.Error(), "invalid URL: ") {
		t.Fatalf("error = %v, want an invalid URL wrap", err)
	}
	if got := Classify(err, "http://x").Kind; got != KindInvalidURL {
		t.Fatalf("kind = %d, want invalid URL", got)
	}
}

// apiErrText builds the error text the API layer produces for a status and
// body, without standing up a server.
func apiErrText(status, body string) error {
	if body == "" {
		return fmt.Errorf("API request failed (%s)", status)
	}
	return fmt.Errorf("API request failed (%s): %s", status, body)
}

func TestClassifyInvariants(t *testing.T) {
	const url = "http://api:8888"
	cases := []struct {
		name      string
		err       error
		wantKind  APIErrorKind
		wantTitle string
		contains  []string // substrings the full message must carry
		omits     []string // substrings the full message must not carry
	}{
		{
			name:     "400 with a timeout body stays 400",
			err:      apiErrText("400 Bad Request", `{"detail":"query would timeout"}`),
			wantKind: KindBadRequest, wantTitle: "Request rejected (400)",
			contains: []string{"query would timeout"},
		},
		{
			name:     "400 without a detail falls back to the full text",
			err:      apiErrText("400 Bad Request", "plain failure"),
			wantKind: KindBadRequest,
			contains: []string{"Server response:", "plain failure"},
		},
		{
			name:     "403 with a timeout body stays 403",
			err:      apiErrText("403 Forbidden", `{"detail":"timed out upstream"}`),
			wantKind: KindPermission, wantTitle: "Permission denied (403)",
		},
		{
			name:     "500 with a 404 body stays 500",
			err:      apiErrText("500 Internal Server Error", `{"detail":"404 somewhere"}`),
			wantKind: KindServer, wantTitle: "API server error",
			contains: []string{"Internal server error (500)", "404 somewhere"},
		},
		{
			name:     "502 uses the unavailable bullet",
			err:      apiErrText("502 Bad Gateway", ""),
			wantKind: KindServer,
			contains: []string{"Service temporarily unavailable"},
		},
		{
			// 504 is not matched by the 5xx branch; its wire text carries
			// "Timeout", so it lands in the timeout class instead.
			name:     "504 falls through to the timeout class",
			err:      apiErrText("504 Gateway Timeout", ""),
			wantKind: KindTimeout, wantTitle: "Request timed out",
			omits: []string{"API server error"},
		},
		{
			name:     "404 with a detail replaces the guidance",
			err:      apiErrText("404 Not Found", `{"detail":"Bank x not found"}`),
			wantKind: KindNotFound, wantTitle: "Not found (404): Bank x not found",
			omits: []string{"The API endpoint path has changed"},
		},
		{
			name:     "404 without a body keeps the guidance",
			err:      apiErrText("404 Not Found", ""),
			wantKind: KindNotFound, wantTitle: "API endpoint not found (404)",
			contains: []string{"The API endpoint path has changed", "Check that you're using the correct API version"},
		},
		{
			name:     "404 non-JSON body surfaces verbatim",
			err:      apiErrText("404 Not Found", "nope"),
			wantKind: KindNotFound, wantTitle: "Not found (404): nope",
		},
		{
			name:     "404 detail object serializes",
			err:      apiErrText("404 Not Found", `{"detail":{"a":1}}`),
			wantKind: KindNotFound, wantTitle: `Not found (404): {"a":1}`,
		},
		{
			name:     "404 nested detail is not traversed",
			err:      apiErrText("404 Not Found", `{"error":{"detail":"x"}}`),
			wantKind: KindNotFound, wantTitle: `Not found (404): {"error":{"detail":"x"}}`,
		},
		{
			name:     "404 disabled bank configuration API",
			err:      apiErrText("404 Not Found", "Bank configuration API is disabled"),
			wantKind: KindNotFound, wantTitle: "Bank configuration API is disabled",
			contains: []string{"HINDSIGHT_API_ENABLE_BANK_CONFIG_API=true", "This allows per-bank LLM configuration overrides via API"},
			omits:    []string{"Not found (404):"},
		},
		{
			name:     "401 with a server response block",
			err:      apiErrText("401 Unauthorized", `{"detail":"bad key"}`),
			wantKind: KindAuth, wantTitle: "Authentication failed",
			contains: []string{"Check if the API requires an API key or token", "Server response:", "bad key"},
		},
		{
			name:     "401 without a body has no server block",
			err:      apiErrText("401 Unauthorized", ""),
			wantKind: KindAuth,
			omits:    []string{"Server response:"},
		},
		{
			name:     "decode sentinel beats body text mentioning 500 and timeout",
			err:      errors.New(`Invalid response payload: boom` + "\n" + `Response was: {"error":"500 timeout"}`),
			wantKind: KindDecode, wantTitle: "Invalid API response format",
			contains: []string{"Decoder error:\nboom", "Actual response:\n{\"error\":\"500 timeout\"}", "Run with --verbose flag to see the full request/response"},
			omits:    []string{"API server error"},
		},
		{
			name:     "connection refused hides the body",
			err:      errors.New(`Get "http://api:8888/x": dial tcp 1.2.3.4:8888: connect: connection refused`),
			wantKind: KindConnection, wantTitle: "Cannot connect to Wenyuan API",
			contains: []string{"A firewall is blocking the connection", "API URL: " + url},
			omits:    []string{"dial tcp", "Server response:"},
		},
		{
			name:     "go timeout shape classifies as timeout",
			err:      errors.New(`Get "http://api:8888/x": context deadline exceeded (Client.Timeout exceeded while awaiting headers)`),
			wantKind: KindTimeout, wantTitle: "Request timed out",
			omits: []string{"Client.Timeout"},
		},
		{
			name:     "go dns shape classifies as dns",
			err:      errors.New(`Get "http://api:8888/x": dial tcp: lookup api.invalid: no such host`),
			wantKind: KindDNS, wantTitle: "Cannot resolve API hostname",
			contains: []string{"Check the HINDSIGHT_API_URL environment variable"},
		},
		{
			name:     "invalid url shape",
			err:      errors.New(`invalid URL: parse "http://": missing protocol scheme`),
			wantKind: KindInvalidURL, wantTitle: "Invalid API URL",
			contains: []string{"export HINDSIGHT_API_URL=http://localhost:18888"},
		},
		{
			name:     "parenthetical with a non-digit status is not split",
			err:      errors.New("some failure (not a status): 404 somewhere"),
			wantKind: KindOther, wantTitle: "API request failed",
			contains: []string{"Error:", "some failure (not a status): 404 somewhere"},
		},
		{
			name:     "fallback shows the full text",
			err:      errors.New("weird envelope: -32000"),
			wantKind: KindOther,
			contains: []string{"Error:", "weird envelope: -32000", "Verify network connectivity to the API server"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err, url)
			if got.Kind != tc.wantKind {
				t.Fatalf("kind = %d, want %d\nmessage:\n%s", got.Kind, tc.wantKind, got.Error())
			}
			msg := got.Error()
			if !strings.HasPrefix(msg, tc.wantTitle) {
				t.Fatalf("message = %q, want title %q", msg, tc.wantTitle)
			}
			for _, sub := range tc.contains {
				if !strings.Contains(msg, sub) {
					t.Errorf("message missing %q:\n%s", sub, msg)
				}
			}
			for _, sub := range tc.omits {
				if strings.Contains(msg, sub) {
					t.Errorf("message must not contain %q:\n%s", sub, msg)
				}
			}
		})
	}
}

func TestClassifiedMessageLayout(t *testing.T) {
	// Pin the exact block layout once, for one class.
	got := Classify(apiErrText("403 Forbidden", ""), "http://api:8888").Error()
	want := "Permission denied (403)\n" +
		"\n" +
		"API URL: http://api:8888\n" +
		"\n" +
		"Possible causes:\n" +
		"  - This operation is not allowed\n" +
		"  - The feature may be disabled on the server\n" +
		"\n" +
		"Try:\n" +
		"  - Check server configuration or contact your administrator"
	if got != want {
		t.Fatalf("message =\n%q\nwant\n%q", got, want)
	}
}

func TestEndToEndClassifiedErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"detail":"invalid key"}`)
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "k")
	err := c.Do(context.Background(), "GET", "/v1/default/banks", nil, nil)
	got := Classify(err, srv.URL)
	if got.Kind != KindAuth {
		t.Fatalf("kind = %d, want auth", got.Kind)
	}
	if !strings.Contains(got.Error(), "invalid key") {
		t.Errorf("message missing the server detail:\n%s", got.Error())
	}
}
