// Package objectstore persists opaque payloads in an S3-compatible object
// store.
//
// It is the external-storage counterpart to keeping attachment bytes in the
// database: a deployment can put the payloads on S3, MinIO, Cloudflare R2 or
// any other S3-compliant endpoint and leave the rows in PostgreSQL. The store
// speaks the small subset of the S3 REST API the attachment paths use — put,
// get and head — signed with the standard SigV4 scheme, so it needs no vendor
// SDK and no managed runtime.
package objectstore

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ErrNotFound is returned when an object key is unknown. It is distinct from a
// transport failure so a caller can treat a missing payload as data rather
// than as an outage.
var ErrNotFound = errors.New("object not found")

// Config is the resolved object-store configuration.
type Config struct {
	// Bucket is the bucket every object is written under.
	Bucket string
	// Region is the signing region. It must match the bucket's region for
	// AWS; an S3-compatible endpoint generally accepts any value.
	Region string
	// Endpoint is the scheme and host the requests are sent to, without a
	// trailing slash. Empty selects the AWS virtual-host default for the
	// bucket and region.
	Endpoint string
	// AccessKeyID and SecretAccessKey are the static credentials. Empty means
	// anonymous access, which a public-read bucket or a local MinIO with
	// anonymous writes may allow.
	AccessKeyID     string
	SecretAccessKey string
	// PathStyle addresses the bucket as a path segment rather than a
	// subdomain. AWS deprecated it but most self-hosted services require it.
	PathStyle bool
}

// Store reads and writes objects in one bucket.
type Store struct {
	cfg  Config
	http *http.Client
	// now is injectable so request signing is deterministic in tests.
	now func() time.Time
}

// New builds a store over the configuration. A nil transport uses a default
// client with a request timeout.
func New(cfg Config, doer *http.Client) *Store {
	if doer == nil {
		doer = &http.Client{Timeout: 60 * time.Second}
	}
	return &Store{cfg: cfg, http: doer, now: time.Now}
}

// Put stores the payload under the key, replacing any existing object.
func (s *Store) Put(ctx context.Context, key string, data []byte, contentType string) error {
	headers := map[string]string{
		"content-type": contentType,
		// The payload is sent whole, so its digest is known before the
		// request and must be signed with it.
		"x-amz-content-sha256": sha256Hex(data),
	}
	resp, err := s.do(ctx, http.MethodPut, key, data, headers)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	return s.statusError(resp)
}

// Get returns the payload stored under the key, or ErrNotFound.
func (s *Store) Get(ctx context.Context, key string) ([]byte, string, error) {
	// The payload digest is unknown before a read, so the empty-string digest
	// is signed, which is what the specification requires for a bodyless
	// request.
	resp, err := s.do(ctx, http.MethodGet, key, nil, map[string]string{"x-amz-content-sha256": emptyDigest})
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound {
		return nil, "", ErrNotFound
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", s.statusError(resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxPayloadBytes))
	if err != nil {
		return nil, "", fmt.Errorf("read object %q: %w", key, err)
	}
	return data, resp.Header.Get("Content-Type"), nil
}

// maxPayloadBytes caps how much of an object is read, so a substituted
// endpoint cannot exhaust memory.
const maxPayloadBytes = 64 << 20 // 64 MiB

// emptyDigest is the SHA-256 of the empty string, which a bodyless request
// signs in place of a payload digest.
const emptyDigest = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// DeletePrefix removes every object whose key begins with prefix, listing in
// pages so a bank with many attachments is swept without loading every key at
// once. It is what a bank teardown uses to reclaim object storage.
func (s *Store) DeletePrefix(ctx context.Context, prefix string) error {
	token := ""
	for {
		keys, next, err := s.listKeys(ctx, prefix, token)
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := s.Delete(ctx, key); err != nil {
				return err
			}
		}
		if next == "" {
			return nil
		}
		token = next
	}
}

// listObjectsResult is the subset of the ListObjectsV2 response we read.
type listObjectsResult struct {
	Contents []struct {
		Key string `xml:"Key"`
	} `xml:"Contents"`
	IsTruncated           bool   `xml:"IsTruncated"`
	NextContinuationToken string `xml:"NextContinuationToken"`
}

// listKeys pages one ListObjectsV2 response.
func (s *Store) listKeys(ctx context.Context, prefix, token string) ([]string, string, error) {
	base, host, err := s.bucketURL()
	if err != nil {
		return nil, "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return nil, "", err
	}
	query := req.URL.Query()
	query.Set("list-type", "2")
	query.Set("prefix", prefix)
	if token != "" {
		query.Set("continuation-token", token)
	}
	req.URL.RawQuery = query.Encode()
	req.Host = host
	now := s.now().UTC()
	req.Header.Set("x-amz-date", now.Format("20060102T150405Z"))
	if s.cfg.AccessKeyID != "" {
		req.Header.Set("x-amz-content-sha256", emptyDigest)
		req.Header.Set("Authorization", s.sign(req, host, now, emptyDigest))
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, "", s.statusError(resp)
	}
	var result listObjectsResult
	if err := xml.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", err
	}
	keys := make([]string, 0, len(result.Contents))
	for _, entry := range result.Contents {
		keys = append(keys, entry.Key)
	}
	if result.IsTruncated {
		return keys, result.NextContinuationToken, nil
	}
	return keys, "", nil
}

// Delete removes one object. A key that is already gone is not an error, so a
// delete is idempotent.
func (s *Store) Delete(ctx context.Context, key string) error {
	resp, err := s.do(ctx, http.MethodDelete, key, nil, map[string]string{
		"x-amz-content-sha256": emptyDigest,
	})
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || (resp.StatusCode >= 200 && resp.StatusCode < 300) {
		return nil
	}
	return s.statusError(resp)
}

// bucketURL builds the bucket-level URL (no key) used by the listing calls.
func (s *Store) bucketURL() (string, string, error) {
	if s.cfg.Bucket == "" {
		return "", "", errors.New("object store has no bucket configured")
	}
	endpoint := strings.TrimRight(s.cfg.Endpoint, "/")
	if endpoint == "" {
		region := s.cfg.Region
		if region == "" {
			region = "us-east-1"
		}
		if region == "us-east-1" {
			endpoint = "https://" + s.cfg.Bucket + ".s3.amazonaws.com"
		} else {
			endpoint = "https://" + s.cfg.Bucket + ".s3." + region + ".amazonaws.com"
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", "", err
		}
		return strings.TrimRight(endpoint, "/"), parsed.Host, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("object store endpoint is not a URL: %w", err)
	}
	if s.cfg.PathStyle {
		parsed.Path = joinPath(parsed.Path, s.cfg.Bucket)
		return strings.TrimRight(parsed.String(), "/"), parsed.Host, nil
	}
	parsed.Host = s.cfg.Bucket + "." + parsed.Host
	return strings.TrimRight(parsed.String(), "/"), parsed.Host, nil
}

// do signs and sends one request, returning the raw response for the caller to
// classify.
func (s *Store) do(ctx context.Context, method, key string, body []byte, headers map[string]string) (*http.Response, error) {
	requestURL, host, err := s.objectURL(key)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, requestURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	now := s.now().UTC()
	req.Header.Set("x-amz-date", now.Format("20060102T150405Z"))
	if s.cfg.AccessKeyID != "" {
		req.Header.Set("x-amz-content-sha256", headers["x-amz-content-sha256"])
		auth := s.sign(req, host, now, headers["x-amz-content-sha256"])
		req.Header.Set("Authorization", auth)
	}
	return s.http.Do(req)
}

// objectURL builds the request URL and the host header value for a key. The
// path style puts the bucket in the path, which is what a self-hosted service
// expects; the virtual-host style puts it in the subdomain, which is the AWS
// default.
//
// The key is appended verbatim rather than joined, because path cleaning would
// rewrite a key the storage area already validated and escaped — collapsing a
// legitimate segment or, worse, resolving a parent-directory form instead of
// preserving it.
func (s *Store) objectURL(key string) (string, string, error) {
	if s.cfg.Bucket == "" {
		return "", "", errors.New("object store has no bucket configured")
	}
	escaped := escapeKey(key)
	endpoint := strings.TrimRight(s.cfg.Endpoint, "/")
	if endpoint == "" {
		region := s.cfg.Region
		if region == "" {
			region = "us-east-1"
		}
		// AWS serves a regional virtual-host address; us-east-1 keeps the
		// legacy global host so an existing bucket keeps working.
		if region == "us-east-1" {
			endpoint = "https://" + s.cfg.Bucket + ".s3.amazonaws.com"
		} else {
			endpoint = "https://" + s.cfg.Bucket + ".s3." + region + ".amazonaws.com"
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", "", err
		}
		return endpoint + "/" + escaped, parsed.Host, nil
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return "", "", fmt.Errorf("object store endpoint is not a URL: %w", err)
	}
	if s.cfg.PathStyle {
		parsed.Path = joinPath(parsed.Path, s.cfg.Bucket, escaped)
		return parsed.String(), parsed.Host, nil
	}
	parsed.Host = s.cfg.Bucket + "." + parsed.Host
	parsed.Path = joinPath(parsed.Path, escaped)
	return parsed.String(), parsed.Host, nil
}

// joinPath appends path segments to a base without cleaning the result, so the
// caller's own escaping survives. It only normalizes the leading separator.
func joinPath(base string, segments ...string) string {
	trimmed := strings.TrimSuffix(base, "/")
	var out strings.Builder
	out.WriteString(trimmed)
	for _, segment := range segments {
		out.WriteString("/")
		out.WriteString(strings.TrimPrefix(segment, "/"))
	}
	return out.String()
}

// sign computes the SigV4 Authorization header for a request.
func (s *Store) sign(req *http.Request, host string, now time.Time, payloadHash string) string {
	region := s.cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	date := now.Format("20060102")
	scope := date + "/" + region + "/s3/aws4_request"

	// The signed header set is the host, the date and every x-amz-* header, all
	// lowercased and sorted, which is the scheme's own rule.
	signed := map[string]string{
		"host":                 host,
		"x-amz-content-sha256": payloadHash,
		"x-amz-date":           req.Header.Get("x-amz-date"),
	}
	names := make([]string, 0, len(signed))
	for name := range signed {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(signed[name]))
		canonicalHeaders.WriteByte('\n')
	}
	signedNames := strings.Join(names, ";")

	canonicalRequest := strings.Join([]string{
		req.Method,
		req.URL.EscapedPath(),
		canonicalQuery(req.URL),
		canonicalHeaders.String(),
		signedNames,
		payloadHash,
	}, "\n")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		req.Header.Get("x-amz-date"),
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")

	signature := hex.EncodeToString(hmacSHA256(s.signingKey(date, region), stringToSign))

	return "AWS4-HMAC-SHA256 Credential=" + s.cfg.AccessKeyID + "/" + scope +
		", SignedHeaders=" + signedNames + ", Signature=" + signature
}

// signingKey derives the SigV4 signing key for a date and region. The
// derivation is the same for a header signature and for the query-string form
// a presigned URL uses, so it lives in one place.
func (s *Store) signingKey(date, region string) []byte {
	key := hmacSHA256([]byte("AWS4"+s.cfg.SecretAccessKey), date)
	key = hmacSHA256(key, region)
	key = hmacSHA256(key, "s3")
	return hmacSHA256(key, "aws4_request")
}

// canonicalQuery renders the query string in the sorted, escaped form the
// signature covers. Keys in this store carry no query, so it is empty, but the
// canonical request must still carry the terminating newline its own line
// implies.
func canonicalQuery(u *url.URL) string {
	if u.RawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return u.RawQuery
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		sorted := values[key]
		sort.Strings(sorted)
		for _, value := range sorted {
			parts = append(parts, url.QueryEscape(key)+"="+url.QueryEscape(value))
		}
	}
	return strings.Join(parts, "&")
}

// escapeKey escapes each path segment of the key while leaving the separators.
//
// The storage area already percent-encodes every segment it builds a key from,
// so a `%` in the key is a literal part of the stored name rather than a
// character awaiting escape: re-escaping it would look up a different object
// and report a miss on a payload that was written successfully. Only the
// characters that cannot appear raw in a request path are escaped here.
func escapeKey(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = escapeSegment(segment)
	}
	return strings.Join(segments, "/")
}

// escapeSegment escapes the characters that cannot appear in a request path,
// leaving an existing `%` escape sequence intact.
func escapeSegment(segment string) string {
	var out strings.Builder
	for i := 0; i < len(segment); i++ {
		c := segment[i]
		switch c {
		case ' ', '"', '<', '>', '\\', '^', '`', '{', '|', '}':
			out.WriteString("%")
			out.WriteString(strings.ToUpper(hex.EncodeToString([]byte{c})))
		default:
			out.WriteByte(c)
		}
	}
	return out.String()
}

// statusError turns a non-success response into a typed error, reading the S3
// error envelope when one was returned.
func (s *Store) statusError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var envelope struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
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

// ResponseError reports a non-success object-store response.
type ResponseError struct {
	// Status is the HTTP status the endpoint returned.
	Status int
	// Detail is the endpoint's error message, when it supplied one.
	Detail string
}

func (e *ResponseError) Error() string {
	return fmt.Sprintf("object store returned status %d: %s", e.Status, e.Detail)
}

// sha256Hex returns the lowercase hex SHA-256 of the input.
func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hmacSHA256 returns the HMAC-SHA256 of the message under the key.
func hmacSHA256(key []byte, message string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(message))
	return mac.Sum(nil)
}
