package fsengine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// fetchTimeout bounds one snapshot request.
const fetchTimeout = 60 * time.Second

// fetchUserAgent is cosmetic; it is not part of the wire contract.
const fetchUserAgent = "wenyuan-fs/0.1.0"

// Fetcher performs the two snapshot requests of one sync pass against the
// knowledge-base endpoints of one bank.
type Fetcher struct {
	// BaseURL is the API root; trailing slashes are stripped at use.
	BaseURL string
	// Token is the optional bearer credential.
	Token string
	// HTTP performs requests; nil uses a client with the 60s timeout.
	HTTP interface {
		Do(req *http.Request) (*http.Response, error)
	}
}

func (f *Fetcher) client() interface {
	Do(req *http.Request) (*http.Response, error)
} {
	if f.HTTP != nil {
		return f.HTTP
	}
	return &http.Client{Timeout: fetchTimeout}
}

func (f *Fetcher) get(ctx context.Context, bankID, suffix string) ([]byte, error) {
	path := "/v1/default/banks/" + EncodePathSegment(bankID) + "/knowledge-base/" + suffix
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		strings.TrimRight(f.BaseURL, "/")+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", fetchUserAgent)
	if f.Token != "" {
		req.Header.Set("Authorization", "Bearer "+f.Token)
	}
	// bodyclose flags the wrapped client, but the body is deferred-closed below.
	resp, err := f.client().Do(req) //nolint:bodyclose
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fetchError(suffix, bankID, resp.StatusCode, data)
	}
	return data, nil
}

// fetchError builds the failure string that reaches lastError and the log:
// the phase-named message, the HTTP status, and up to 200 body characters.
func fetchError(suffix, bankID string, status int, body []byte) error {
	msg := fmt.Sprintf("Failed to fetch knowledge-base %s knowledge base for bank %q (HTTP %d)",
		suffix, bankID, status)
	if trimmed := strings.TrimSpace(string(body)); trimmed != "" {
		if len(trimmed) > 200 {
			trimmed = trimmed[:200]
		}
		msg += ": " + trimmed
	}
	return fmt.Errorf("%s", msg)
}

// FetchTree downloads the complete tree snapshot.
func (f *Fetcher) FetchTree(ctx context.Context, bankID string) (*Tree, error) {
	data, err := f.get(ctx, bankID, "tree")
	if err != nil {
		return nil, err
	}
	var tree Tree
	if err := json.Unmarshal(data, &tree); err != nil {
		return nil, fmt.Errorf("invalid knowledge-base tree payload: %s", err)
	}
	return &tree, nil
}

// FetchExport downloads the export bundle and reduces it to page documents:
// only entries ending in .md survive, excluding index.md and *.log.md. The
// page id is the path with the .md suffix stripped.
func (f *Fetcher) FetchExport(ctx context.Context, bankID string) (map[string]string, error) {
	data, err := f.get(ctx, bankID, "export")
	if err != nil {
		return nil, err
	}
	var bundle struct {
		Files []struct {
			Path    string `json:"path"`
			Content string `json:"content"`
		} `json:"files"`
	}
	if err := json.Unmarshal(data, &bundle); err != nil {
		return nil, fmt.Errorf("invalid knowledge-base export payload: %s", err)
	}
	docs := map[string]string{}
	for _, file := range bundle.Files {
		if !strings.HasSuffix(file.Path, ".md") || file.Path == "index.md" ||
			strings.HasSuffix(file.Path, ".log.md") {
			continue
		}
		docs[strings.TrimSuffix(file.Path, ".md")] = file.Content
	}
	return docs, nil
}
