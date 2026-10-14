package objectstore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeS3 is an in-memory S3-compatible endpoint that records the requests it
// receives so a test can assert the wire shape.
type fakeS3 struct {
	objects map[string][]byte
	types   map[string]string
	last    *http.Request
	calls   int
	status  int
}

func newFakeS3() *fakeS3 {
	return &fakeS3{objects: map[string][]byte{}, types: map[string]string{}}
}

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.calls++
	f.last = r
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `<Error><Code>Denied</Code><Message>nope</Message></Error>`)
		return
	}
	key := strings.TrimPrefix(r.URL.Path, "/bucket/")
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[key] = body
		f.types[key] = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
	case http.MethodGet:
		data, ok := f.objects[key]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>NoSuchKey</Code><Message>missing</Message></Error>`)
			return
		}
		if ct := f.types[key]; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		_, _ = w.Write(data)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func testStore(t *testing.T) (*Store, *fakeS3) {
	t.Helper()
	fake := newFakeS3()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	store := New(Config{
		Bucket:          "bucket",
		Region:          "us-east-1",
		Endpoint:        server.URL,
		AccessKeyID:     "access",
		SecretAccessKey: "secret",
		PathStyle:       true,
	}, server.Client())
	store.now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	return store, fake
}

// A stored payload round-trips unchanged, including its content type.
func TestPutGetRoundTrip(t *testing.T) {
	store, _ := testStore(t)
	ctx := context.Background()
	payload := []byte("attachment bytes")
	if err := store.Put(ctx, "banks/b1/docs/d1/file.pdf", payload, "application/pdf"); err != nil {
		t.Fatal(err)
	}
	data, contentType, err := store.Get(ctx, "banks/b1/docs/d1/file.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != string(payload) {
		t.Errorf("data = %q, want %q", data, payload)
	}
	if contentType != "application/pdf" {
		t.Errorf("content type = %q, want application/pdf", contentType)
	}
}

// An unknown key is reported as a miss rather than as a transport failure, so
// a caller can treat it as data.
func TestGetMissingIsNotFound(t *testing.T) {
	store, _ := testStore(t)
	_, _, err := store.Get(context.Background(), "banks/b1/missing.bin")
	if err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// The request carries a SigV4 authorization header naming the credential, the
// signed header set and a signature, and the date header its scope is derived
// from.
func TestRequestIsSigned(t *testing.T) {
	store, fake := testStore(t)
	if err := store.Put(context.Background(), "k", []byte("v"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	auth := fake.last.Header.Get("Authorization")
	if !strings.HasPrefix(auth, "AWS4-HMAC-SHA256 ") {
		t.Fatalf("authorization = %q, want a SigV4 header", auth)
	}
	if !strings.Contains(auth, "Credential=access/20261014/us-east-1/s3/aws4_request") {
		t.Errorf("authorization = %q, want the dated credential scope", auth)
	}
	if !strings.Contains(auth, "SignedHeaders=host;x-amz-content-sha256;x-amz-date") {
		t.Errorf("authorization = %q, want the signed header set", auth)
	}
	if !strings.Contains(auth, "Signature=") {
		t.Errorf("authorization = %q, want a signature", auth)
	}
	if got := fake.last.Header.Get("x-amz-date"); got != "20261014T120000Z" {
		t.Errorf("x-amz-date = %q, want the injected clock", got)
	}
}

// A bodyless read signs the empty-payload digest, which is what the scheme
// requires when the payload is not known in advance.
func TestGetSignsEmptyDigest(t *testing.T) {
	store, fake := testStore(t)
	if err := store.Put(context.Background(), "k", []byte("v"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Get(context.Background(), "k"); err != nil {
		t.Fatal(err)
	}
	if got := fake.last.Header.Get("x-amz-content-sha256"); got != emptyDigest {
		t.Errorf("x-amz-content-sha256 = %q, want the empty digest", got)
	}
}

// A non-success response is reported with its status and decoded detail.
func TestResponseErrorDetail(t *testing.T) {
	store, fake := testStore(t)
	fake.status = http.StatusForbidden
	err := store.Put(context.Background(), "k", []byte("v"), "text/plain")
	if err == nil {
		t.Fatal("a forbidden response must be an error")
	}
	responseErr, ok := err.(*ResponseError)
	if !ok {
		t.Fatalf("err = %T, want *ResponseError", err)
	}
	if responseErr.Status != http.StatusForbidden {
		t.Errorf("status = %d, want 403", responseErr.Status)
	}
	if !strings.Contains(responseErr.Detail, "Denied") {
		t.Errorf("detail = %q, want the decoded error code", responseErr.Detail)
	}
}

// The path-style addressing puts the bucket in the path, which is what a
// self-hosted endpoint requires.
func TestPathStyleAddressing(t *testing.T) {
	store, fake := testStore(t)
	if err := store.Put(context.Background(), "banks/b1/f.bin", []byte("v"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if got := fake.last.URL.Path; got != "/bucket/banks/b1/f.bin" {
		t.Errorf("path = %q, want the bucket in the path", got)
	}
}

// A key already carrying a percent-escape is stored under that literal name,
// not double-escaped: the storage area encodes its segments before the key
// reaches the object store, so re-escaping would look up a different object and
// miss a payload that was written.
func TestEncodedKeyIsNotDoubleEscaped(t *testing.T) {
	store, fake := testStore(t)
	// "Express AI" was percent-encoded by the storage area into "Express%20AI".
	key := "banks/b1/Express%20AI/file.pdf"
	if err := store.Put(context.Background(), key, []byte("v"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	if got := fake.last.URL.Path; got != "/bucket/banks/b1/Express%20AI/file.pdf" {
		t.Fatalf("path = %q, want the encoded key preserved verbatim", got)
	}
}

// A key segment containing a character that cannot appear raw in a path is
// escaped, so the request is well-formed.
func TestKeySegmentsAreEscaped(t *testing.T) {
	store, fake := testStore(t)
	if err := store.Put(context.Background(), "banks/b1/a b.bin", []byte("v"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if got := fake.last.URL.Path; got != "/bucket/banks/b1/a%20b.bin" {
		t.Fatalf("path = %q, want the space escaped", got)
	}
}

// A parent-directory segment in a key is preserved rather than resolved, so a
// caller-chosen identifier addresses a name inside its own prefix instead of
// escaping it — the storage area validates the key before it arrives.
func TestParentDirectorySegmentIsPreserved(t *testing.T) {
	store, fake := testStore(t)
	key := "banks/b1/..%2Fescape"
	if err := store.Put(context.Background(), key, []byte("v"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if got := fake.last.URL.Path; got != "/bucket/banks/b1/..%2Fescape" {
		t.Fatalf("path = %q, want the segment preserved rather than resolved", got)
	}
}

// A bare key with no bucket configured is refused rather than sent to a
// default bucket the operator never named.
func TestMissingBucketRefused(t *testing.T) {
	store := New(Config{Endpoint: "https://example.test"}, nil)
	if err := store.Put(context.Background(), "k", []byte("v"), "text/plain"); err == nil {
		t.Fatal("a store with no bucket must refuse a write")
	}
}

// The virtual-host style puts the bucket in the subdomain, which is the AWS
// default when no endpoint is configured.
func TestVirtualHostAddressing(t *testing.T) {
	store := New(Config{Bucket: "bank-data", Region: "eu-west-1"}, nil)
	_, host, err := store.objectURL("k")
	if err != nil {
		t.Fatal(err)
	}
	if host != "bank-data.s3.eu-west-1.amazonaws.com" {
		t.Fatalf("host = %q, want the regional virtual-host address", host)
	}
	global := New(Config{Bucket: "bank-data"}, nil)
	_, host, err = global.objectURL("k")
	if err != nil {
		t.Fatal(err)
	}
	if host != "bank-data.s3.amazonaws.com" {
		t.Fatalf("host = %q, want the legacy global address for us-east-1", host)
	}
}
