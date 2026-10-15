package objectstore

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/xml"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

// fakeAzure is an in-memory Azure Blob endpoint that records the requests it
// receives so a test can verify the shared-key authorization.
type fakeAzure struct {
	objects map[string][]byte
	types   map[string]string
	last    *http.Request
	key     []byte
	account string
	status  int
}

func newFakeAzure(key []byte) *fakeAzure {
	return &fakeAzure{objects: map[string][]byte{}, types: map[string]string{}, key: key, account: "acct"}
}

func (f *fakeAzure) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.last = r
	if f.status != 0 {
		w.WriteHeader(f.status)
		_, _ = io.WriteString(w, `<Error><Code>AuthenticationFailed</Code><Message>denied</Message></Error>`)
		return
	}
	if r.URL.Query().Get("comp") == "list" {
		prefix := r.URL.Query().Get("prefix")
		var blobs []string
		for name := range f.objects {
			if strings.HasPrefix(name, prefix) {
				blobs = append(blobs, "<Blob><Name>"+name+"</Name></Blob>")
			}
		}
		w.Header().Set("Content-Type", "application/xml")
		_, _ = io.WriteString(w, `<?xml version="1.0"?><EnumerationResults><Blobs>`+
			strings.Join(blobs, "")+`</Blobs><NextMarker></NextMarker></EnumerationResults>`)
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/container/")
	switch r.Method {
	case http.MethodPut:
		body, _ := io.ReadAll(r.Body)
		f.objects[name] = body
		f.types[name] = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusCreated)
	case http.MethodDelete:
		delete(f.objects, name)
		w.WriteHeader(http.StatusAccepted)
	case http.MethodGet:
		data, ok := f.objects[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `<Error><Code>BlobNotFound</Code><Message>missing</Message></Error>`)
			return
		}
		if ct := f.types[name]; ct != "" {
			w.Header().Set("Content-Type", ct)
		}
		w.Header().Set("Content-Length", strconv.Itoa(len(data)))
		_, _ = w.Write(data)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

type azureFixture struct {
	store *Azure
	fake  *fakeAzure
	key   []byte
}

func newAzureFixture(t *testing.T) azureFixture {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	fake := newFakeAzure(key)
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	store, err := NewAzure(AzureConfig{
		Container:   "container",
		AccountName: "acct",
		AccountKey:  base64.StdEncoding.EncodeToString(key),
		Endpoint:    server.URL,
	}, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC) }
	return azureFixture{store: store, fake: fake, key: key}
}

// verifyAzureSharedKey recomputes the SharedKey authorization from the request
// the server actually received, so a request signed differently fails here.
func verifyAzureSharedKey(t *testing.T, r *http.Request, account string, key []byte) string {
	t.Helper()
	contentLength := ""
	if r.ContentLength > 0 {
		contentLength = strconv.FormatInt(r.ContentLength, 10)
	}
	names := []string{}
	for name := range r.Header {
		lower := strings.ToLower(name)
		if strings.HasPrefix(lower, "x-ms-") {
			names = append(names, lower)
		}
	}
	sort.Strings(names)
	var canonicalHeaders strings.Builder
	for _, name := range names {
		canonicalHeaders.WriteString(name + ":" + strings.TrimSpace(r.Header.Get(name)) + "\n")
	}
	var resource strings.Builder
	resource.WriteString("/" + account + r.URL.EscapedPath())
	query := r.URL.Query()
	queryNames := []string{}
	for name := range query {
		queryNames = append(queryNames, name)
	}
	sort.Strings(queryNames)
	for _, name := range queryNames {
		values := query[name]
		sort.Strings(values)
		for _, value := range values {
			resource.WriteString("\n" + strings.ToLower(name) + ":" + value)
		}
	}
	stringToSign := strings.Join([]string{
		r.Method,
		r.Header.Get("Content-Encoding"),
		r.Header.Get("Content-Language"),
		contentLength,
		r.Header.Get("Content-MD5"),
		r.Header.Get("Content-Type"),
		"",
		r.Header.Get("If-Modified-Since"),
		r.Header.Get("If-Match"),
		r.Header.Get("If-None-Match"),
		r.Header.Get("If-Unmodified-Since"),
		r.Header.Get("Range"),
		canonicalHeaders.String() + resource.String(),
	}, "\n")
	return "SharedKey " + account + ":" + base64.StdEncoding.EncodeToString(hmacSHA256(key, stringToSign))
}

// A payload round-trips through the Blob REST API, and every request carries
// the shared-key authorization the account key produces.
func TestAzure_PutGetRoundTrip(t *testing.T) {
	fixture := newAzureFixture(t)
	ctx := context.Background()
	key := "banks/b1/exports/e1/transfer.zip"
	if err := fixture.store.Put(ctx, key, []byte("archive"), "application/zip"); err != nil {
		t.Fatal(err)
	}
	if got, want := fixture.fake.last.Header.Get("Authorization"), verifyAzureSharedKey(t, fixture.fake.last, "acct", fixture.key); got != want {
		t.Fatalf("authorization = %q, want %q", got, want)
	}
	if got := fixture.fake.last.Header.Get("x-ms-blob-type"); got != "BlockBlob" {
		t.Fatalf("blob type = %q", got)
	}
	data, contentType, err := fixture.store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "archive" || contentType != "application/zip" {
		t.Fatalf("round trip = %q %q", data, contentType)
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

// An unknown blob is a miss, not a transport failure.
func TestAzure_GetMissingIsNotFound(t *testing.T) {
	fixture := newAzureFixture(t)
	if _, _, err := fixture.store.Get(context.Background(), "banks/b1/nope.zip"); err != ErrNotFound {
		t.Fatalf("err = %v, want ErrNotFound", err)
	}
}

// DeletePrefix sweeps a bank's blobs through a container listing and leaves
// other banks alone.
func TestAzure_DeletePrefix(t *testing.T) {
	fixture := newAzureFixture(t)
	ctx := context.Background()
	for _, key := range []string{"banks/b1/a.zip", "banks/b1/b.zip", "banks/b2/c.zip"} {
		if err := fixture.store.Put(ctx, key, []byte("x"), "application/zip"); err != nil {
			t.Fatal(err)
		}
	}
	if err := fixture.store.DeletePrefix(ctx, "banks/b1/"); err != nil {
		t.Fatal(err)
	}
	if len(fixture.fake.objects) != 1 {
		t.Fatalf("blobs left = %v", fixture.fake.objects)
	}
	if _, ok := fixture.fake.objects["banks/b2/c.zip"]; !ok {
		t.Fatal("the sweep must not touch another bank's prefix")
	}
}

// A service SAS URL carries the read permission and the configured expiry, and
// its signature verifies against the account key over the blob's own path.
func TestAzure_PresignedURLVerifiesAndExpires(t *testing.T) {
	fixture := newAzureFixture(t)
	presigned, err := fixture.store.GetDownloadURL(context.Background(), "banks/b1/exports/e.zip", 900*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(presigned)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("sp") != "r" || query.Get("sr") != "b" || query.Get("sv") != azureAPIVersion {
		t.Fatalf("unexpected SAS fields: %s", parsed.RawQuery)
	}
	wantExpiry := "2026-10-14T12:15:00Z"
	if query.Get("se") != wantExpiry {
		t.Fatalf("expiry = %q, want %q", query.Get("se"), wantExpiry)
	}
	stringToSign := strings.Join([]string{
		"r", "", wantExpiry,
		"/blob/acct/container/banks/b1/exports/e.zip",
		"", "", "", azureAPIVersion, "b", "", "", "", "", "", "", "",
	}, "\n")
	wantSignature := base64.StdEncoding.EncodeToString(hmacSHA256(fixture.key, stringToSign))
	if query.Get("sig") != wantSignature {
		t.Fatalf("signature = %q, want %q", query.Get("sig"), wantSignature)
	}
}

// An Azure backend that stores an object under an encoded key serves it back
// under the same name and signs the same one.
func TestAzure_EncodedKeyRoundTrips(t *testing.T) {
	fixture := newAzureFixture(t)
	ctx := context.Background()
	key := "banks/b%2E1/exports/f.zip"
	if err := fixture.store.Put(ctx, key, []byte("z"), "application/zip"); err != nil {
		t.Fatal(err)
	}
	data, _, err := fixture.store.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "z" {
		t.Fatalf("data = %q", data)
	}
	if _, ok := fixture.fake.objects[key]; !ok {
		t.Fatalf("stored names = %v, want the literal key", fixture.fake.objects)
	}
}

// A missing container is refused before any request, naming the variable the
// operator has to set.
func TestAzure_MissingContainerRefused(t *testing.T) {
	_, err := NewAzure(AzureConfig{}, nil)
	if err == nil || !strings.Contains(err.Error(), "HINDSIGHT_API_FILE_STORAGE_AZURE_CONTAINER") {
		t.Fatalf("err = %v", err)
	}
}

// An account key that is not base64 is refused rather than silently degrading
// to anonymous access.
func TestAzure_BadAccountKeyRefused(t *testing.T) {
	if _, err := NewAzure(AzureConfig{Container: "c", AccountKey: "not base64!"}, nil); err == nil {
		t.Fatal("a malformed account key must be refused")
	}
}

// The container listing response is parsed through the shared XML vocabulary.
func TestAzure_ListParsesBlobNames(t *testing.T) {
	var result azureListResult
	body := `<?xml version="1.0"?><EnumerationResults><Blobs>` +
		`<Blob><Name>banks/b1/a.zip</Name></Blob><Blob><Name>banks/b1/b.zip</Name></Blob>` +
		`</Blobs><NextMarker>next</NextMarker></EnumerationResults>`
	if err := xml.Unmarshal([]byte(body), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Blobs.Blob) != 2 || result.Blobs.Blob[0].Name != "banks/b1/a.zip" || result.NextMarker != "next" {
		t.Fatalf("parsed = %+v", result)
	}
}

// The S3 presigned URL is signed over the query string it carries, so
// rebuilding that string from the URL and signing it with the same secret
// reproduces the signature — and a different secret does not.
func TestS3_PresignedURLVerifiesAndExpires(t *testing.T) {
	store, _ := testStore(t)
	presigned, err := store.GetDownloadURL(context.Background(), "banks/b1/exports/e.zip", 900*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(presigned)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if query.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" || query.Get("X-Amz-Expires") != "900" {
		t.Fatalf("unexpected query: %s", parsed.RawQuery)
	}
	canonicalQuery := parsed.RawQuery
	if idx := strings.LastIndex(canonicalQuery, "&X-Amz-Signature="); idx >= 0 {
		canonicalQuery = canonicalQuery[:idx]
	}
	signature := query.Get("X-Amz-Signature")
	canonicalRequest := strings.Join([]string{
		"GET", parsed.EscapedPath(), canonicalQuery,
		"host:" + parsed.Host + "\n", "host", "UNSIGNED-PAYLOAD",
	}, "\n")
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", query.Get("X-Amz-Date"),
		"20261014/us-east-1/s3/aws4_request", sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	want := hex.EncodeToString(hmacSHA256(store.signingKey("20261014", "us-east-1"), stringToSign))
	if signature != want {
		t.Fatalf("signature = %q, want %q", signature, want)
	}
	other := &Store{cfg: Config{SecretAccessKey: "different"}}
	if got := hex.EncodeToString(hmacSHA256(other.signingKey("20261014", "us-east-1"), stringToSign)); got == want {
		t.Fatal("a different secret must not reproduce the signature")
	}
}
