package objectstore

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// GCSConfig is the Google Cloud Storage backend configuration.
type GCSConfig struct {
	// Bucket is the bucket every object is written under.
	Bucket string
	// ServiceAccountKey is a service-account JSON key, either its content or a
	// path to the file. Empty selects anonymous access, which a public bucket
	// allows.
	ServiceAccountKey string
	// Endpoint overrides the API host, which points the backend at an emulator
	// or a stub. Empty uses storage.googleapis.com.
	Endpoint string
}

// GCS is a Google Cloud Storage backend. It speaks the JSON API over plain
// HTTP and signs V4 download URLs with the service account's own RSA key, so
// it needs no vendor SDK.
type GCS struct {
	cfg   GCSConfig
	http  *http.Client
	now   func() time.Time
	creds *gcsCredentials
	// tokenMu guards the cached OAuth access token.
	tokenMu     sync.Mutex
	token       string
	tokenExpiry time.Time
}

// gcsScope is the OAuth scope the storage API requires.
const gcsScope = "https://www.googleapis.com/auth/cloud-platform"

// defaultGCSTokenURI is where a service account exchanges its signed assertion
// for an access token.
const defaultGCSTokenURI = "https://oauth2.googleapis.com/token"

// gcsCredentials is a parsed service-account key: the identity to assert and
// the key that both signs the assertion and the presigned URLs.
type gcsCredentials struct {
	clientEmail string
	privateKey  *rsa.PrivateKey
	tokenURI    string
}

// NewGCS builds a Google Cloud Storage backend. A nil transport uses a default
// client with a request timeout.
func NewGCS(cfg GCSConfig, doer *http.Client) (*GCS, error) {
	if doer == nil {
		doer = &http.Client{Timeout: 60 * time.Second}
	}
	store := &GCS{cfg: cfg, http: doer, now: time.Now}
	if strings.TrimSpace(cfg.Bucket) == "" {
		// The key is named because the operator has to know which variable to
		// set; the failure happens before any request is attempted.
		return nil, errors.New("WENYUAN_API_FILE_STORAGE_GCS_BUCKET is required for GCS storage")
	}
	if strings.TrimSpace(cfg.ServiceAccountKey) != "" {
		creds, err := parseGCSServiceAccount(cfg.ServiceAccountKey)
		if err != nil {
			return nil, err
		}
		store.creds = creds
	}
	return store, nil
}

// parseGCSServiceAccount reads a service-account key from either its JSON
// content or the path of a key file, and parses the RSA private key it holds.
func parseGCSServiceAccount(value string) (*gcsCredentials, error) {
	raw := strings.TrimSpace(value)
	if !strings.HasPrefix(raw, "{") {
		data, err := os.ReadFile(raw)
		if err != nil {
			return nil, fmt.Errorf("read GCS service account key: %w", err)
		}
		raw = string(data)
	}
	var key struct {
		ClientEmail string `json:"client_email"`
		PrivateKey  string `json:"private_key"`
		TokenURI    string `json:"token_uri"`
	}
	if err := json.Unmarshal([]byte(raw), &key); err != nil {
		return nil, fmt.Errorf("parse GCS service account key: %w", err)
	}
	if key.ClientEmail == "" || key.PrivateKey == "" {
		return nil, errors.New("GCS service account key is missing client_email or private_key")
	}
	block, _ := pem.Decode([]byte(key.PrivateKey))
	if block == nil {
		return nil, errors.New("GCS service account key is not a PEM-encoded private key")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse GCS service account private key: %w", err)
	}
	rsaKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, errors.New("GCS service account private key is not RSA")
	}
	tokenURI := key.TokenURI
	if tokenURI == "" {
		tokenURI = defaultGCSTokenURI
	}
	return &gcsCredentials{clientEmail: key.ClientEmail, privateKey: rsaKey, tokenURI: tokenURI}, nil
}

// Put stores the payload under the key, replacing any existing object.
func (g *GCS) Put(ctx context.Context, key string, data []byte, contentType string) error {
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	query := url.Values{}
	query.Set("uploadType", "media")
	query.Set("name", key)
	uploadURL := g.base() + "/upload/storage/v1/b/" + url.PathEscape(g.cfg.Bucket) + "/o?" + query.Encode()
	resp, err := g.do(ctx, http.MethodPost, uploadURL, data, contentType)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	return g.checkStatus(resp)
}

// Get returns the payload stored under the key, or ErrNotFound.
func (g *GCS) Get(ctx context.Context, key string) ([]byte, string, error) {
	resp, err := g.do(ctx, http.MethodGet, g.objectURL(key)+"?alt=media", nil, "")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", g.statusError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPayloadBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read object %q: %w", key, err)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// OpenStream returns the object body without buffering it.
func (g *GCS) OpenStream(ctx context.Context, key string) (io.ReadCloser, string, int64, error) {
	resp, err := g.do(ctx, http.MethodGet, g.objectURL(key)+"?alt=media", nil, "")
	if err != nil {
		return nil, "", -1, err
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, "", -1, ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer func() { _ = resp.Body.Close() }()
		return nil, "", -1, g.statusError(resp)
	}
	return resp.Body, resp.Header.Get("Content-Type"), resp.ContentLength, nil
}

// DeletePrefix removes every object whose key begins with prefix, listing in
// pages so the sweep never holds every key at once.
func (g *GCS) DeletePrefix(ctx context.Context, prefix string) error {
	token := ""
	for {
		keys, next, err := g.listKeys(ctx, prefix, token)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := g.Delete(ctx, key); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		token = next
	}
}

// gcsListResult is the subset of a GCS object listing this backend reads.
type gcsListResult struct {
	Items []struct {
		Name string `json:"name"`
	} `json:"items"`
	NextPageToken string `json:"nextPageToken"`
}

// listKeys pages one object listing.
func (g *GCS) listKeys(ctx context.Context, prefix, token string) ([]string, string, error) {
	query := url.Values{}
	query.Set("prefix", prefix)
	if token != "" {
		query.Set("pageToken", token)
	}
	listURL := g.base() + "/storage/v1/b/" + url.PathEscape(g.cfg.Bucket) + "/o?" + query.Encode()
	resp, err := g.do(ctx, http.MethodGet, listURL, nil, "")
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if err := g.checkStatus(resp); err != nil {
		return nil, "", err
	}
	var result gcsListResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", err
	}
	keys := make([]string, 0, len(result.Items))
	for _, item := range result.Items {
		keys = append(keys, item.Name)
	}
	return keys, result.NextPageToken, nil
}

// Delete removes one object. A key that is already gone is not an error, so a
// delete is idempotent.
func (g *GCS) Delete(ctx context.Context, key string) error {
	resp, err := g.do(ctx, http.MethodDelete, g.objectURL(key), nil, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	return g.checkStatus(resp)
}

// GetDownloadURL returns a V4-signed URL that grants time-limited read access
// to the key. Anonymous access has nothing to sign, so it yields the empty
// string.
func (g *GCS) GetDownloadURL(_ context.Context, key string, expiresIn time.Duration) (string, error) {
	if g.creds == nil {
		return "", nil
	}
	seconds := int64(clampExpiry(expiresIn) / time.Second)
	now := g.now().UTC()
	date := now.Format("20060102")
	timestamp := now.Format("20060102T150405Z")
	// The bucket's location is not needed to sign, so "auto" is the scope
	// region, as the V4 documentation's own examples use.
	scope := date + "/auto/storage/goog4_request"
	resource := "/" + url.PathEscape(g.cfg.Bucket) + "/" + signingPathEscape(key)
	host, err := hostOf(g.base())
	if err != nil {
		return "", err
	}
	params := []queryParam{
		{"X-Goog-Algorithm", "GOOG4-RSA-SHA256"},
		{"X-Goog-Credential", g.creds.clientEmail + "/" + scope},
		{"X-Goog-Date", timestamp},
		{"X-Goog-Expires", strconv.FormatInt(seconds, 10)},
		{"X-Goog-SignedHeaders", "host"},
	}
	query := canonicalQueryString(params)
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		resource,
		query,
		"host:" + host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{
		"GOOG4-RSA-SHA256",
		timestamp,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	digest := sha256.Sum256([]byte(stringToSign))
	signature, err := rsa.SignPKCS1v15(rand.Reader, g.creds.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GCS download URL: %w", err)
	}
	return g.base() + resource + "?" + query + "&X-Goog-Signature=" + hex.EncodeToString(signature), nil
}

// accessToken returns a bearer token, exchanging a signed assertion for one
// and caching it until shortly before it expires. Anonymous access returns an
// empty token.
func (g *GCS) accessToken(ctx context.Context) (string, error) {
	if g.creds == nil {
		return "", nil
	}
	g.tokenMu.Lock()
	defer g.tokenMu.Unlock()
	now := g.now().UTC()
	if g.token != "" && now.Before(g.tokenExpiry.Add(-time.Minute)) {
		return g.token, nil
	}
	assertion, err := g.creds.assertion(now)
	if err != nil {
		return "", err
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:jwt-bearer")
	form.Set("assertion", assertion)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.creds.tokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.http.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", g.statusError(resp)
	}
	var body struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int64  `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return "", err
	}
	if body.AccessToken == "" {
		return "", errors.New("GCS token response carried no access token")
	}
	lifetime := time.Duration(body.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = time.Hour
	}
	g.token = body.AccessToken
	g.tokenExpiry = now.Add(lifetime)
	return g.token, nil
}

// assertion builds the RS256-signed JWT a service account exchanges for an
// access token.
func (c *gcsCredentials) assertion(now time.Time) (string, error) {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]any{
		"iss":   c.clientEmail,
		"scope": gcsScope,
		"aud":   c.tokenURI,
		"iat":   now.Unix(),
		"exp":   now.Add(time.Hour).Unix(),
	})
	if err != nil {
		return "", err
	}
	signingInput := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, c.privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return "", fmt.Errorf("sign GCS token assertion: %w", err)
	}
	return signingInput + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

// do sends one authenticated request.
func (g *GCS) do(ctx context.Context, method, urlStr string, body []byte, contentType string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	token, err := g.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return g.http.Do(req)
}

// checkStatus turns a non-success response into the shared error vocabulary. A
// missing object is reported as ErrNotFound so a caller cannot tell one backend
// from another.
func (g *GCS) checkStatus(resp *http.Response) error {
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode == http.StatusNotFound {
		return ErrNotFound
	}
	return g.statusError(resp)
}

// statusError reads the GCS JSON error envelope when one was returned.
func (g *GCS) statusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var envelope struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	detail := strings.TrimSpace(string(raw))
	if err := json.Unmarshal(raw, &envelope); err == nil && envelope.Error.Message != "" {
		detail = envelope.Error.Message
	}
	return &ResponseError{Status: resp.StatusCode, Detail: detail}
}

// base is the API endpoint without a trailing slash.
func (g *GCS) base() string {
	endpoint := strings.TrimRight(strings.TrimSpace(g.cfg.Endpoint), "/")
	if endpoint == "" {
		endpoint = "https://storage.googleapis.com"
	}
	return endpoint
}

// objectURL names one object in the JSON API. The key is escaped as a single
// path segment, so a "/" inside a bank's key stays part of the object name
// rather than becoming a path separator.
func (g *GCS) objectURL(key string) string {
	return g.base() + "/storage/v1/b/" + url.PathEscape(g.cfg.Bucket) + "/o/" + url.PathEscape(key)
}

// hostOf returns the host (and port) of a base URL, which is what a signed
// host header must match.
func hostOf(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("object store endpoint is not a URL: %w", err)
	}
	return parsed.Host, nil
}
