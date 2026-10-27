package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The adapter exposes a signed download URL when the backend can sign one, and
// the storage area delegates to it, so the download route can hand the client
// a direct URL instead of proxying the payload.
func TestObjectStoreDownloadURLIsDelegated(t *testing.T) {
	store := testObjectStoreS3(t, config.S3Config{
		Bucket: "bucket", Region: "us-east-1", Endpoint: "https://minio.internal",
		AccessKeyID: "access", SecretAccessKey: "secret",
	})
	pg := (&storage.PostgresStore{}).WithObjectStore(store)
	direct, err := pg.StoredFileDownloadURL(context.Background(), "banks/b1/exports/e.zip", time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(direct, "X-Amz-Signature=") {
		t.Fatalf("url = %q, want a signed URL", direct)
	}
}

// A backend with no credentials has nothing to sign, so the signed URL is
// empty and the caller keeps serving the payload itself.
func TestObjectStoreDownloadURLUnsignedWithoutCredentials(t *testing.T) {
	store := testObjectStoreS3(t, config.S3Config{Bucket: "bucket", Endpoint: "https://minio.internal"})
	direct, err := store.(interface {
		GetDownloadURL(ctx context.Context, key string, expiresIn time.Duration) (string, error)
	}).GetDownloadURL(context.Background(), "banks/b1/exports/e.zip", time.Hour)
	if err != nil || direct != "" {
		t.Fatalf("url = %q err = %v, want empty", direct, err)
	}
}

// The row backend offers no direct URL — the route that would serve its API
// path is the caller — so it yields the empty string rather than a redirect
// loop.
func TestRowBackendHasNoDownloadURL(t *testing.T) {
	pg := &storage.PostgresStore{}
	direct, err := pg.StoredFileDownloadURL(context.Background(), "banks/b1/exports/e.zip", time.Hour)
	if err != nil || direct != "" {
		t.Fatalf("url = %q err = %v, want empty", direct, err)
	}
}

// The backend is selected by the resolved file-storage type, and a selection
// whose required setting is missing fails naming the variable to set.
func TestNewObjectStoreSelectsBackend(t *testing.T) {
	cases := []struct {
		name    string
		cfg     config.FileStorageConfig
		wantErr string
	}{
		{"s3", config.FileStorageConfig{Type: config.FileStorageS3, S3: config.S3Config{Bucket: "b"}}, ""},
		{"gcs", config.FileStorageConfig{Type: config.FileStorageGCS, GCS: config.GCSConfig{Bucket: "b"}}, ""},
		{"azure", config.FileStorageConfig{Type: config.FileStorageAzure, Azure: config.AzureConfig{Container: "c"}}, ""},
		{"gcs without bucket", config.FileStorageConfig{Type: config.FileStorageGCS}, "WENYUAN_API_FILE_STORAGE_GCS_BUCKET"},
		{"azure without container", config.FileStorageConfig{Type: config.FileStorageAzure}, "WENYUAN_API_FILE_STORAGE_AZURE_CONTAINER"},
		{"unknown", config.FileStorageConfig{Type: "sftp"}, "sftp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store, err := newObjectStore(tc.cfg)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want a mention of %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if store == nil {
				t.Fatal("no store built")
			}
		})
	}
}

// A construction failure reaches the caller instead of starting a service with
// an unusable payload backend.
func TestAttachObjectStorePropagatesConstructionError(t *testing.T) {
	pg := &storage.PostgresStore{}
	cfg := config.Config{FileStorage: config.FileStorageConfig{Type: config.FileStorageGCS}}
	got, err := attachObjectStore(pg, cfg)
	if err == nil || !strings.Contains(err.Error(), "WENYUAN_API_FILE_STORAGE_GCS_BUCKET") {
		t.Fatalf("err = %v", err)
	}
	if got != nil {
		t.Fatalf("store = %v, want nil on a construction failure", got)
	}
	if pg.UsesObjectStore() {
		t.Fatal("a failed attachment must not leave an object store behind")
	}
}

// A GCS backend built through the selection speaks the JSON API against the
// endpoint it was given, so the wiring reaches the right service.
func TestNewObjectStoreGCSUsesEndpoint(t *testing.T) {
	var sawUpload bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/upload/storage/v1/b/bucket/o") {
			sawUpload = true
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(server.Close)

	cfg := config.FileStorageConfig{Type: config.FileStorageGCS, GCS: config.GCSConfig{Bucket: "bucket", Endpoint: server.URL}}
	store, err := newObjectStore(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Put(context.Background(), "banks/b1/f.bin", []byte("x"), "application/octet-stream"); err != nil {
		t.Fatal(err)
	}
	if !sawUpload {
		t.Fatal("the upload must reach the configured GCS endpoint")
	}
}
