package openai

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// transportDouble is the injected local transport for every adapter test. It
// returns queued responses so no test reaches a live endpoint or needs a real
// credential.
type transportDouble struct {
	responses []doubleResponse
	calls     int
	requests  []*http.Request
}

type doubleResponse struct {
	status  int
	body    string
	headers map[string]string
}

func (d *transportDouble) Do(req *http.Request) (*http.Response, error) {
	d.requests = append(d.requests, req)
	index := d.calls
	d.calls++
	if index >= len(d.responses) {
		index = len(d.responses) - 1
	}
	r := d.responses[index]
	header := http.Header{}
	for k, v := range r.headers {
		header.Set(k, v)
	}
	if header.Get("Content-Type") == "" && r.body != "" {
		header.Set("Content-Type", "application/json")
	}
	return &http.Response{
		StatusCode: r.status,
		Body:       io.NopCloser(strings.NewReader(r.body)),
		Header:     header,
	}, nil
}

// jsonResponse builds a 200 response carrying the given body.
func jsonResponse(body string) doubleResponse {
	return doubleResponse{status: http.StatusOK, body: body}
}

// testClient builds a client over the transport double with sensible
// settings, including an injectable clock and sleeper so retry tests do not
// wait in real time.
func testClient(responses ...doubleResponse) (*Client, *transportDouble) {
	doer := &transportDouble{responses: responses}
	c := NewClient(clientSettings(), doer)
	var elapsed int64
	c.Now = func() time.Time {
		elapsed += int64(time.Second)
		return time.Unix(0, 0).Add(time.Duration(elapsed))
	}
	c.Sleep = func(time.Duration) {}
	return c, doer
}

// clientSettings is a resolved settings value for adapter tests.
func clientSettings() provider.Settings {
	return provider.Settings{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  "https://example.test/v1",
		Model:    "test-model",
		Timeout:  5 * time.Second,
		Retry: provider.RetryPolicy{
			MaxRetries:     2,
			InitialBackoff: time.Millisecond,
			MaxBackoff:     time.Second,
		},
	}
}

// test-operation-permit-is-shared: the per-operation permit must be one
// shared bound for the whole client. A permit rebuilt on every call would
// always start empty and therefore never apply back-pressure, so this drives
// the real acquire path and asserts the second caller blocks.
func TestOperationPermitIsSharedAcrossCalls(t *testing.T) {
	c, _ := testClient(jsonResponse(`{"choices":[{"message":{"content":"ok"}}]}`))
	c.Settings.Concurrency = provider.ConcurrencyPolicy{PerOperation: 1}

	// Hold the only permitted slot through the real path.
	_, release, err := c.acquire()
	if err != nil {
		t.Fatal(err)
	}

	// A second acquire through the same path must block while the slot is held.
	acquired := make(chan struct{})
	go func() {
		_, releaseSecond, err := c.acquire()
		if err == nil {
			defer releaseSecond()
		}
		close(acquired)
	}()
	select {
	case <-acquired:
		t.Fatal("a second acquire must wait while the only per-operation permit is held")
	case <-time.After(50 * time.Millisecond):
	}
	release()
	select {
	case <-acquired:
	case <-time.After(time.Second):
		t.Fatal("release must let the waiting acquire proceed")
	}
}
