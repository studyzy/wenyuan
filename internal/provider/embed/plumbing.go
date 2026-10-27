package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/provider"
)

// Configuration keys named in the errors these adapters return, so an operator
// is told which key to set rather than which member refused.
const (
	keyProvider          = "WENYUAN_API_EMBEDDINGS_PROVIDER"
	keyOpenAIAPIKey      = "WENYUAN_API_EMBEDDINGS_OPENAI_API_KEY"
	keyOpenRouterAPIKey  = "WENYUAN_API_EMBEDDINGS_OPENROUTER_API_KEY"
	keyRequestyAPIKey    = "WENYUAN_API_EMBEDDINGS_REQUESTY_API_KEY"
	keyCohereAPIKey      = "WENYUAN_API_EMBEDDINGS_COHERE_API_KEY"
	keyZeroEntropyAPIKey = "WENYUAN_API_EMBEDDINGS_ZEROENTROPY_API_KEY"
	keyGeminiAPIKey      = "WENYUAN_API_EMBEDDINGS_GEMINI_API_KEY"
	keyTEIURL            = "WENYUAN_API_EMBEDDINGS_TEI_URL"
)

// DefaultTimeout bounds one embedding request when the configuration names no
// timeout.
const DefaultTimeout = 60 * time.Second

// resolveDimensions reports the width the caller will store. A configured
// width wins; otherwise the boundary's fixed constant is used, because the
// destination's vector column is that width.
func resolveDimensions(cfg Config) int {
	if cfg.Dimensions > 0 {
		return cfg.Dimensions
	}
	return provider.DeclaredDimensions()
}

// resolveBatch reports the inputs per request, falling back to the member's
// documented default.
func resolveBatch(cfg Config, def int) int {
	if cfg.BatchSize > 0 {
		return cfg.BatchSize
	}
	return def
}

// resolveMaxConcurrent reports the in-flight request bound, never less than
// one.
func resolveMaxConcurrent(cfg Config) int {
	if cfg.MaxConcurrent > 0 {
		return cfg.MaxConcurrent
	}
	return DefaultMaxConcurrent
}

// resolveRetry reports the retry tuning. An entirely unset policy takes the
// boundary default rather than disabling retries by accident.
func resolveRetry(p provider.RetryPolicy) provider.RetryPolicy {
	if p == (provider.RetryPolicy{}) {
		return provider.DefaultRetryPolicy()
	}
	return p.Normalize()
}

// applyPrefix prepends the configured passage or query prefix to each input.
// The kind is the caller's; an unrecognized kind is treated as a passage.
func applyPrefix(texts []string, kind provider.EmbeddingKind, passagePrefix, queryPrefix string) []string {
	prefix := passagePrefix
	if kind == provider.EmbeddingQuery {
		prefix = queryPrefix
	}
	if prefix == "" {
		return texts
	}
	out := make([]string, len(texts))
	for i, t := range texts {
		out[i] = prefix + t
	}
	return out
}

// trimTrailingSlash removes trailing slashes so a base URL joins a path
// predictably.
func trimTrailingSlash(s string) string {
	for len(s) > 0 && s[len(s)-1] == '/' {
		s = s[:len(s)-1]
	}
	return s
}

// httpBackend is the request plumbing every REST member shares: one client,
// one retry policy, and a POST that reads a JSON body.
type httpBackend struct {
	name    string
	baseURL string
	client  *http.Client
	retry   provider.RetryPolicy
}

// newHTTPBackend builds the shared plumbing over one member's endpoint.
func newHTTPBackend(name, baseURL string, timeout time.Duration, retry provider.RetryPolicy) *httpBackend {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	return &httpBackend{
		name:    name,
		baseURL: trimTrailingSlash(baseURL),
		client:  &http.Client{Timeout: timeout},
		retry:   resolveRetry(retry),
	}
}

// postJSON sends one JSON body to a path under the base URL and returns the
// success body. Transient failures are retried per the resolved policy.
func (b *httpBackend) postJSON(ctx context.Context, path string, payload any, headers map[string]string) ([]byte, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, &provider.MalformedResponseError{Detail: "cannot encode request", Cause: err}
	}
	return doWithRetry(ctx, b.retry, func(ctx context.Context) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := b.client.Do(req)
		if err != nil {
			return nil, &provider.TransientError{Cause: err}
		}
		defer func() { _ = resp.Body.Close() }()
		raw, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, &provider.TransientError{Cause: err}
		}
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, statusError(b.name, resp.StatusCode, raw)
		}
		return raw, nil
	})
}

// statusError classifies a non-2xx response. Overload and server faults are
// retryable; anything else is a permanent refusal.
func statusError(name string, status int, body []byte) error {
	detail := strings.TrimSpace(string(body))
	if len(detail) > 512 {
		detail = detail[:512]
	}
	err := fmt.Errorf("%s: status %d: %s", name, status, detail)
	if status == http.StatusTooManyRequests || status >= 500 {
		return &provider.TransientError{Cause: err}
	}
	return err
}

// doWithRetry runs fn, retrying transient failures up to the policy's budget.
func doWithRetry(ctx context.Context, policy provider.RetryPolicy, fn func(context.Context) ([]byte, error)) ([]byte, error) {
	var last error
	for attempt := 0; attempt <= policy.MaxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, policy.BackoffFor(attempt-1)); err != nil {
				return nil, err
			}
		}
		raw, err := fn(ctx)
		if err == nil {
			return raw, nil
		}
		last = err
		if !provider.IsTransient(err) {
			return nil, err
		}
	}
	return nil, last
}

// sleep waits for the backoff delay, returning early when the context ends.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// decodeJSON unmarshals a success body, reporting a decode failure as a
// malformed response.
func decodeJSON(raw []byte, v any, detail string) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return &provider.MalformedResponseError{Detail: detail, Cause: err}
	}
	return nil
}

// batchEmbed splits texts into consecutive batches and runs fetch over them
// with bounded concurrency, reassembling one vector per input in input order
// regardless of completion order.
func batchEmbed(ctx context.Context, texts []string, batchSize, maxConcurrent int, fetch func(context.Context, []string) ([][]float32, error)) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	size := batchSize
	if size < 1 {
		size = DefaultBatchSize
	}
	limit := maxConcurrent
	if limit < 1 {
		limit = 1
	}
	out := make([][]float32, len(texts))
	permits := make(chan struct{}, limit)
	var wg sync.WaitGroup
	var mu sync.Mutex
	var firstErr error

	for start := 0; start < len(texts); start += size {
		end := min(start+size, len(texts))
		chunk := texts[start:end]
		offset := start
		wg.Add(1)
		go func() {
			defer wg.Done()
			permits <- struct{}{}
			defer func() { <-permits }()
			vectors, err := fetch(ctx, chunk)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return
			}
			copy(out[offset:], vectors)
		}()
	}
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	return out, nil
}

// onePerInput rejects a response that does not carry exactly one vector per
// input, so a partial or misaligned body never reaches storage.
func onePerInput(vectors [][]float32, expected int, member string) error {
	if len(vectors) != expected {
		return &provider.MalformedResponseError{
			Detail: fmt.Sprintf("%s returned %d vectors for %d inputs", member, len(vectors), expected),
		}
	}
	return nil
}
