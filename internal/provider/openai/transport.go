package openai

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// maxResponseBytes caps how much of a response body is read, so a hostile or
// broken endpoint cannot exhaust memory.
const maxResponseBytes = 8 << 20 // 8 MiB

// semaphore bounds simultaneous calls. Permits are released before any
// backoff sleep, so waiting to retry never holds a permit.
type semaphore chan struct{}

func newSemaphore(limit int) semaphore {
	if limit <= 0 {
		return nil
	}
	return make(semaphore, limit)
}

// acquire takes a permit, returning a release function. A nil semaphore is
// unbounded.
func (s semaphore) acquire() func() {
	if s == nil {
		return func() {}
	}
	s <- struct{}{}
	return func() { <-s }
}

// Pool holds the process-wide permit shared by every scope built from one
// composition, so the global cap composes with per-operation caps.
type Pool struct {
	once sync.Once
	sem  semaphore
	size int
}

// NewPool returns a process-wide permit of the given size. A non-positive
// size leaves the pool unbounded.
func NewPool(size int) *Pool { return &Pool{size: size} }

func (p *Pool) get() semaphore {
	p.once.Do(func() { p.sem = newSemaphore(p.size) })
	return p.sem
}

// acquire takes the process-wide permit and the per-operation permit in that
// order, releasing them in reverse. It returns how long the caller waited for
// a permit plus a release function.
func (c *Client) acquire() (provider.Duration, func(), error) {
	started := c.now()
	releaseGlobal := func() {}
	if c.Pool != nil {
		releaseGlobal = c.Pool.get().acquire()
	}
	releaseOp := func() {}
	if limit := c.Settings.Concurrency.Effective(); limit > 0 {
		releaseOp = c.operationSemaphore(limit).acquire()
	}
	return provider.Duration(c.now().Sub(started)), func() {
		releaseOp()
		releaseGlobal()
	}, nil
}

// operationSemaphore returns the client's per-operation permit, building it on
// first use. It must be shared across calls: a permit constructed per call
// starts empty every time and therefore never applies back-pressure.
func (c *Client) operationSemaphore(limit int) semaphore {
	c.opOnce.Do(func() { c.opSem = newSemaphore(limit) })
	return c.opSem
}

// send issues one HTTP request against the endpoint, applying extra headers,
// the credential, and the per-attempt wall-clock bound.
func (c *Client) send(ctx context.Context, path string, body []byte) (*http.Response, error) {
	url := c.BaseURL() + "/" + strings.TrimPrefix(path, "/")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot build request", Cause: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if key := c.Settings.APIKey; key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	for name, value := range c.Settings.Headers {
		req.Header.Set(name, value)
	}
	return c.doer().Do(req)
}

// doer returns the configured transport, defaulting to a plain client.
func (c *Client) doer() HTTPDoer {
	if c.HTTP != nil {
		return c.HTTP
	}
	return http.DefaultClient
}

// readBody drains a bounded amount of a response body.
func readBody(resp *http.Response) ([]byte, error) {
	defer func() { _ = resp.Body.Close() }()
	return io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
}

// sleep waits between attempts, defaulting to the real clock.
func (c *Client) sleep(d time.Duration) {
	if d <= 0 {
		return
	}
	if c.Sleep != nil {
		c.Sleep(d)
		return
	}
	time.Sleep(d)
}
