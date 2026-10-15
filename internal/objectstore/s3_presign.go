package objectstore

import (
	"context"
	"encoding/hex"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// GetDownloadURL returns a presigned GET URL for the key using the SigV4
// query-string form: the signature is carried in the query instead of the
// Authorization header, so a client that holds no credentials can fetch the
// object until the URL expires.
//
// With no static credentials configured there is nothing to sign, so the empty
// string is returned and the caller falls back to serving the payload itself —
// the same choice a public-read bucket would force.
func (s *Store) GetDownloadURL(_ context.Context, key string, expiresIn time.Duration) (string, error) {
	if s.cfg.AccessKeyID == "" || s.cfg.SecretAccessKey == "" {
		return "", nil
	}
	objectURL, host, err := s.objectURL(key)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(objectURL)
	if err != nil {
		return "", err
	}
	region := s.cfg.Region
	if region == "" {
		region = "us-east-1"
	}
	now := s.now().UTC()
	date := now.Format("20060102")
	amzDate := now.Format("20060102T150405Z")
	scope := date + "/" + region + "/s3/aws4_request"
	seconds := int64(clampExpiry(expiresIn) / time.Second)

	// The signed query is fixed: the algorithm, the credential scope, the
	// timestamp, the lifetime and the one signed header (host). Sorted, it is
	// already in the order the canonical form requires.
	params := []queryParam{
		{"X-Amz-Algorithm", "AWS4-HMAC-SHA256"},
		{"X-Amz-Credential", s.cfg.AccessKeyID + "/" + scope},
		{"X-Amz-Date", amzDate},
		{"X-Amz-Expires", strconv.FormatInt(seconds, 10)},
		{"X-Amz-SignedHeaders", "host"},
	}
	query := canonicalQueryString(params)

	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		parsed.EscapedPath(),
		query,
		"host:" + host + "\n",
		"host",
		// A presigned URL grants access without a body, so the payload is
		// declared unsigned rather than hashed.
		"UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacSHA256(s.signingKey(date, region), stringToSign))

	return objectURL + "?" + query + "&X-Amz-Signature=" + signature, nil
}

// signingPathEscape escapes every path segment of a key so nothing but the
// unreserved characters and the separators survives: unlike escapeKey it also
// encodes "%", which the JSON and Blob APIs decode before matching. A key the
// storage area built from percent-encoded segments therefore addresses the same
// object through a path as it does through its literal name.
func signingPathEscape(key string) string {
	segments := strings.Split(key, "/")
	for i, segment := range segments {
		segments[i] = uriEscape(segment, true)
	}
	return strings.Join(segments, "/")
}
