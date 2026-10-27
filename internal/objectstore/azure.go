package objectstore

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// AzureConfig is the Azure Blob Storage backend configuration.
type AzureConfig struct {
	// Container is the blob container every object is written under.
	Container string
	// AccountName is the storage account, which is the host label of the
	// default service endpoint and the identity a shared key signs for.
	AccountName string
	// AccountKey is the base64 storage account key. Empty selects anonymous
	// access, which a public container allows.
	AccountKey string
	// Endpoint overrides the blob service base URL, which points the backend
	// at an emulator or a stub. Empty uses
	// https://{account}.blob.core.windows.net.
	Endpoint string
}

// azureAPIVersion is the storage service version every request declares. It is
// also the version the shared-access signature is computed against.
const azureAPIVersion = "2021-08-06"

// Azure is an Azure Blob Storage backend. It speaks the Blob REST API and
// signs requests and download URLs with the account key, so it needs no vendor
// SDK.
type Azure struct {
	cfg  AzureConfig
	http *http.Client
	now  func() time.Time
	// key is the decoded account key. Nil means anonymous access.
	key []byte
}

// NewAzure builds an Azure Blob Storage backend. A nil transport uses a
// default client with a request timeout.
func NewAzure(cfg AzureConfig, doer *http.Client) (*Azure, error) {
	if doer == nil {
		doer = &http.Client{Timeout: 60 * time.Second}
	}
	store := &Azure{cfg: cfg, http: doer, now: time.Now}
	if strings.TrimSpace(cfg.Container) == "" {
		return nil, errors.New("WENYUAN_API_FILE_STORAGE_AZURE_CONTAINER is required for Azure storage")
	}
	if raw := strings.TrimSpace(cfg.AccountKey); raw != "" {
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, fmt.Errorf("decode Azure account key: %w", err)
		}
		store.key = decoded
	}
	return store, nil
}

// Put stores the payload under the key, replacing any existing object.
func (a *Azure) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	resp, err := a.do(ctx, http.MethodPut, a.blobURL(key), nil, data, contentType,
		map[string]string{"x-ms-blob-type": "BlockBlob"})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return a.checkStatus(resp)
}

// Get returns the payload stored under the key, or ErrNotFound.
func (a *Azure) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := a.do(ctx, http.MethodGet, a.blobURL(key), nil, nil, "", nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", a.statusError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPayloadBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read object %q: %w", key, err)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// OpenStream returns the object body without buffering it.
func (a *Azure) OpenStream(ctx context.Context, key string) (io.ReadCloser, string, int64, error) {
	resp, err := a.do(ctx, http.MethodGet, a.blobURL(key), nil, nil, "", nil)
	if err != nil {
		return nil, "", -1, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", -1, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, "", -1, a.statusError(resp)
	}
	return resp.Body, resp.Header.Get("Content-Type"), resp.ContentLength, nil
}

// DeletePrefix removes every object whose key begins with prefix, listing in
// pages so the sweep never holds every key at once.
func (a *Azure) DeletePrefix(ctx context.Context, prefix string) error {
	marker := ""
	for {
		keys, next, err := a.listKeys(ctx, prefix, marker)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := a.Delete(ctx, key); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		marker = next
	}
}

// azureListResult is the subset of a container listing this backend reads.
type azureListResult struct {
	Blobs struct {
		Blob []struct {
			Name string `xml:"Name"`
		} `xml:"Blob"`
	} `xml:"Blobs"`
	NextMarker string `xml:"NextMarker"`
}

// listKeys pages one container listing.
func (a *Azure) listKeys(ctx context.Context, prefix, marker string) ([]string, string, error) {
	query := url.Values{}
	query.Set("restype", "container")
	query.Set("comp", "list")
	query.Set("prefix", prefix)
	if marker != "" {
		query.Set("marker", marker)
	}
	resp, err := a.do(ctx, http.MethodGet, a.containerURL(), query, nil, "", nil)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := a.checkStatus(resp); err != nil {
		return nil, "", err
	}
	var result azureListResult
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", err
	}
	keys := make([]string, 0, len(result.Blobs.Blob))
	for _, blob := range result.Blobs.Blob {
		keys = append(keys, blob.Name)
	}
	return keys, result.NextMarker, nil
}

// Delete removes one blob. A blob that is already gone is not an error, so a
// delete is idempotent.
func (a *Azure) Delete(ctx context.Context, key string) error {
	resp, err := a.do(ctx, http.MethodDelete, a.blobURL(key), nil, nil, "", nil)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return a.checkStatus(resp)
}

// GetDownloadURL returns a service SAS URL that grants time-limited read
// access to the key. Anonymous access has nothing to sign, so it yields the
// empty string.
func (a *Azure) GetDownloadURL(_ context.Context, key string, expiresIn time.Duration) (string, error) {
	if a.key == nil {
		return "", nil
	}
	expiry := a.now().UTC().Add(clampExpiry(expiresIn)).UTC().Format("2006-01-02T15:04:05Z")
	// A service SAS for one blob: the canonicalized resource is the blob's own
	// path, and the string to sign is the fixed field list the version
	// defines.
	stringToSign := strings.Join([]string{
		"r", // signed permissions: read
		"",  // signed start
		expiry,
		"/blob/" + a.cfg.AccountName + "/" + a.cfg.Container + "/" + key,
		"", // signed identifier
		"", // signed IP
		"", // signed protocol
		azureAPIVersion,
		"b", // signed resource: blob
		"",  // signed snapshot time
		"",  // signed encryption scope
		"",  // response cache-control
		"",  // response content-disposition
		"",  // response content-encoding
		"",  // response content-language
		"",  // response content-type
	}, "\n")
	signature := base64.StdEncoding.EncodeToString(hmacSHA256(a.key, stringToSign))
	query := url.Values{}
	query.Set("sv", azureAPIVersion)
	query.Set("sr", "b")
	query.Set("sp", "r")
	query.Set("se", expiry)
	query.Set("sig", signature)
	return a.blobURL(key) + "?" + query.Encode(), nil
}

// do sends one request, signing it with the account key when one is
// configured.
func (a *Azure) do(ctx context.Context, method, urlStr string, query url.Values, body []byte, contentType string, extra map[string]string) (*http.Response, error) {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return nil, err
	}
	if len(query) > 0 {
		parsed.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-ms-date", a.now().UTC().Format(http.TimeFormat))
	req.Header.Set("x-ms-version", azureAPIVersion)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, value := range extra {
		req.Header.Set(name, value)
	}
	if a.key != nil {
		req.Header.Set("Authorization", a.sharedKeyAuth(req, parsed))
	}
	return a.http.Do(req)
}

// sharedKeyAuth computes the SharedKey authorization header for a request.
func (a *Azure) sharedKeyAuth(req *http.Request, parsed *url.URL) string {
	// A zero-length body is signed as the empty string for the versions this
	// backend declares.
	contentLength := ""
	if req.ContentLength > 0 {
		contentLength = strconv.FormatInt(req.ContentLength, 10)
	}
	// The signed headers are every x-ms-* header, lowercased, sorted, each
	// followed by its value and a newline.
	names := make([]string, 0, len(req.Header))
	for name := range req.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-ms-") {
			names = append(names, lower)
		}
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(req.Header.Get(name)))
		canonicalHeaders.WriteByte('\n')
	}
	stringToSign := strings.Join([]string{
		req.Method,
		req.Header.Get("Content-Encoding"),
		req.Header.Get("Content-Language"),
		contentLength,
		req.Header.Get("Content-MD5"),
		req.Header.Get("Content-Type"),
		"", // Date: the request carries x-ms-date instead
		req.Header.Get("If-Modified-Since"),
		req.Header.Get("If-Match"),
		req.Header.Get("If-None-Match"),
		req.Header.Get("If-Unmodified-Since"),
		req.Header.Get("Range"),
		canonicalHeaders.String() + a.canonicalResource(parsed),
	}, "\n")
	signature := base64.StdEncoding.EncodeToString(hmacSHA256(a.key, stringToSign))
	return "SharedKey " + a.cfg.AccountName + ":" + signature
}

// canonicalResource renders the shared-key resource: the account name and the
// request's escaped path, then the query parameters sorted by name with their
// values in order.
func (a *Azure) canonicalResource(parsed *url.URL) string {
	var out strings.Builder
	out.WriteString("/")
	out.WriteString(a.cfg.AccountName)
	out.WriteString(parsed.EscapedPath())
	values := parsed.Query()
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sorted := values[name]
		sort.Strings(sorted)
		for _, value := range sorted {
			out.WriteString("\n")
			out.WriteString(strings.ToLower(name))
			out.WriteString(":")
			out.WriteString(value)
		}
	}
	return out.String()
}

// checkStatus turns a non-success response into the shared error vocabulary.
func (a *Azure) checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return a.statusError(resp)
}

// statusError reads the Azure error envelope when one was returned.
func (a *Azure) statusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var envelope struct {
		Message string `xml:"Message"`
		Code    string `xml:"Code"`
	}
	detail := strings.TrimSpace(string(raw))
	if err := xml.Unmarshal(raw, &envelope); err == nil && envelope.Code != "" {
		detail = envelope.Code + ": " + envelope.Message
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return &ResponseError{Status: resp.StatusCode, Detail: detail}
}

// base is the blob service endpoint without a trailing slash.
func (a *Azure) base() string {
	endpoint := strings.TrimRight(strings.TrimSpace(a.cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = "https://" + a.cfg.AccountName + ".blob.core.windows.net"
	}
	return endpoint
}

// containerURL names the container, which is the resource a listing addresses.
func (a *Azure) containerURL() string {
	if a.cfg.Container == "" {
		return a.base()
	}
	return a.base() + "/" + a.cfg.Container
}

// blobURL names one blob under the container.
func (a *Azure) blobURL(key string) string {
	return a.base() + "/" + a.cfg.Container + "/" + signingPathEscape(key)
}
