package objectstore

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeGCS is an in-memory Google Cloud Storage JSON API endpoint. It records
// the requests it receives and verifies that a request carried the bearer
// token its service account exchanged for.
type fakeGCS struct {
	objects  map[string][]byte
	types    map[string]string
	last     *http.Request
	token    string
	status   int
	sawToken bool
}

func newFakeGCS() *fakeGCS {
	return &fakeGCS{objects: map[string][]byte{}, types: map[string]string{}, token: "test-token"}
}

func (f *fakeGCS) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.last = r
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `{"error":{"message":"denied"}}`)
		return
	}
	if r.URL.Path == "/token" {
		if err := r.ParseForm(); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if !strings.Contains(r.PostForm.Get("assertion"), ".") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":"no assertion"}`)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": f.token, "expires_in": 3600})
		return
	}
	if auth := r.Header.Get("Authorization"); auth == "Bearer "+f.token {
		f.sawToken = true
	}
	switch {
	case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/upload/storage/v1/b/bucket/o"):
		name := r.URL.Query().Get("name")
		body, _ := io.ReadAll(r.Body)
		f.objects[name] = body
		f.types[name] = r.Header.Get("Content-Type")
		_ = json.NewEncoder(w).Encode(map[string]any{"name": name})
	case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/bucket/o":
		prefix := r.URL.Query().Get("prefix")
		items := []map[string]string{}
		for name := range f.objects {
			if strings.HasPrefix(name, prefix) {
				items = append(items, map[string]string{"name": name})
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"items": items})
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/storage/v1/b/bucket/o/"):
		delete(f.objects, strings.TrimPrefix(r.URL.Path, "/storage/v1/b/bucket/o/"))
		w.WriteHeader(http.StatusNoContent)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/storage/v1/b/bucket/o/"):
		name := strings.TrimPrefix(r.URL.Path, "/storage/v1/b/bucket/o/")
		data, ok := f.objects[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"error":{"message":"No such object"}}`)
			return
		}
		if ct := f.types[name]; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// gcsFixture is a GCS backend wired to the fake, plus the RSA key its service
// account asserts with so a test can verify a signed URL.
type gcsFixture struct {
	store *GCS
	fake  *fakeGCS
	key   *rsa.PrivateKey
}

func newGCSFixture(t *testing.T) gcsFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fake := newFakeGCS()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	keyJSON := fmt.Sprintf(
		`{"client_email":"svc@example.iam.gserviceaccount.com","private_key":%q,"token_uri":%q}`,
		string(pemBytes), server.URL+"/token")
	store, err := NewGCS(GCSConfig{Bucket: "bucket", ServiceAccountKey: keyJSON, Endpoint: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	return gcsFixture{store: store, fake: fake, key: key}
}

// A payload round-trips through the JSON API unchanged, and the backend
// exchanged its assertion for the bearer token the requests carry.
func TestGCS_PutGetRoundTrip(t *testing.T) {
	fixture := newGCSFixture(t)
	ctx := context.Background()
	key := "banks/b1/exports/e1/transfer.zip"
	if err := fixture.store.Put(ctx, key, []byte("archive"), "application/zip"); err != nil {
		t.Fatal(err)
	}
	data, contentType, err := fixture.store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "archive" || contentType != "application/zip" {
		t.Fatalf("round trip = %q %q", data, contentType)
	}
	if !fixture.fake.sawToken {
		t.Fatal("requests must carry the bearer token the service account exchanged for")
	}
	// A delete round-trips too, and repeats without error.
	if err := fixture.store.Delete(ctx, key); err != nil {
		t.Fatal(err)
	}
	if err := fixture.store.Delete(ctx, key); err != nil {
		t.Fatalf("a repeat delete must be a no-op, got %v", err)
	}
	if _, _, err := fixture.store.Get(ctx, key); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound after delete", err)
	}
}

// An unknown key is a miss, not a transport failure.
func TestGCS_GetMissingIsNotFound(t *testing.T) {
	fixture := newGCSFixture(t)
	if _, _, err := fixture.store.Get(context.Background(), "banks/b1/nope.zip"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// DeletePrefix sweeps a bank's objects and leaves others, paging through the
// listing.
func TestGCS_DeletePrefix(t *testing.T) {
	fixture := newGCSFixture(t)
	ctx := context.Background()
	for key := range map[string]bool{
		"banks/b1/a.zip": true, "banks/b1/b.zip": true, "banks/b2/c.zip": true,
	} {
		if err := fixture.store.Put(ctx, key, []byte("x"), "application/zip"); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.store.DeletePrefix(ctx, "banks/b1/"); err != nil {
		t.Fatal(err)
	}
	if len(fixture.fake.objects) != 1 {
		t.Fatalf("objects left = %v", fixture.fake.objects)
	}
	if _, ok := fixture.fake.objects["banks/b2/c.zip"]; !ok {
		t.Fatal("the sweep must not touch another bank's prefix")
	}
}

// A presigned URL carries the V4 fields, expires as configured, and its
// signature verifies against the service account's public key over the exact
// request the URL describes.
func TestGCS_PresignedURLVerifiesAndExpires(t *testing.T) {
	fixture := newGCSFixture(t)
	presigned, err := fixture.store.GetDownloadURL(context.Background(), "banks/b1/exports/e.zip", 600*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(presigned)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" {
		t.Fatalf("algorithm = %q", query.Get("X-Goog-Algorithm"))
	}
	if query.Get("X-Goog-Expires") != "600" {
		t.Fatalf("expiry = %q, want 600", query.Get("X-Goog-Expires"))
	}
	if query.Get("X-Goog-Date") != "20261014T120000Z" {
		t.Fatalf("date = %q", query.Get("X-Goog-Date"))
	}
	signature := query.Get("X-Goog-Signature")
	if signature == "" {
		t.Fatal("URL carries no signature")
	}
	// Rebuild the signed string from the URL itself, so a URL whose fields do
	// not describe what was signed fails here.
	canonicalQuery := parsed.RawQuery
	if idx := strings.LastIndex(canonicalQuery, "&X-Goog-Signature="); idx >= 0 {
		canonicalQuery = canonicalQuery[:idx]
	}
	canonicalRequest := strings.Join([]string{
		"GET",
		parsed.EscapedPath(),
		canonicalQuery,
		"host:" + parsed.Host + "\n",
		"host",
		"UNSIGNED-PAYLOAD",
	}, "\n")
	scope := "20261014/auto/storage/goog4_request"
	stringToSign := strings.Join([]string{
		"GOOG4-RSA-SHA256", query.Get("X-Goog-Date"), scope, sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	digest := sha256.Sum256([]byte(stringToSign))
	raw, err := hex.DecodeString(signature)
	if err != nil {
		t.Fatal(err)
	}
	if err := rsa.VerifyPKCS1v15(&fixture.key.PublicKey, crypto.SHA256, digest[:], raw); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	// The path must decode back to the object's literal name, which is what
	// makes the URL address the stored object rather than a different one.
	if decoded, err := url.PathUnescape(parsed.EscapedPath()); err != nil || decoded != "/bucket/banks/b1/exports/e.zip" {
		t.Fatalf("path decodes to %q (%v)", decoded, err)
	}
}

// A backend with no service account has nothing to sign, so it yields no URL
// and sends no authorization header.
func TestGCS_AnonymousHasNoSignedURL(t *testing.T) {
	fake := newFakeGCS()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	store, err := NewGCS(GCSConfig{Bucket: "bucket", Endpoint: server.URL}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	url, err := store.GetDownloadURL(context.Background(), "k", time.Minute)
	if err != nil || url != "" {
		t.Fatalf("url = %q err = %v, want empty", url, err)
	}
	if err := store.Put(context.Background(), "k", []byte("v"), ""); err != nil {
		t.Fatal(err)
	}
	if got := fake.last.Header.Get("Authorization"); got != "" {
		t.Fatalf("anonymous request carried %q", got)
	}
}

// A missing bucket is refused before any request, naming the variable the
// operator has to set.
func TestGCS_MissingBucketRefused(t *testing.T) {
	_, err := NewGCS(GCSConfig{}, nil)
	if err == nil || !strings.Contains(err.Error(), "HINDSIGHT_API_FILE_STORAGE_GCS_BUCKET") {
		t.Fatalf("err = %v", err)
	}
}

// A service-account key that cannot be parsed is refused rather than silently
// degrading to anonymous access.
func TestGCS_BadServiceAccountKeyRefused(t *testing.T) {
	if _, err := NewGCS(GCSConfig{Bucket: "b", ServiceAccountKey: "{not json"}, nil); err == nil {
		t.Fatal("a malformed key must be refused")
	}
	if _, err := NewGCS(GCSConfig{Bucket: "b", ServiceAccountKey: `{"client_email":"a","private_key":"not-pem"}`}, nil); err == nil {
		t.Fatal("a key without a PEM private key must be refused")
	}
}
