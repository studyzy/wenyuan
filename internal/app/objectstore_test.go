package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Only a database-backed store takes an object store; the in-process backend
// keeps its payloads in memory and is returned unchanged.
func TestAttachObjectStoreSkipsMemoryBackend(t *testing.T) {
	memory := storage.NewMemoryStore("public")
	cfg := config.Config{FileStorage: config.FileStorageConfig{Type: config.FileStorageS3}}
	got, err := attachObjectStore(memory, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != storage.Store(memory) {
		t.Fatalf("the in-process backend must be returned unchanged, got %T", got)
	}
}

// With no object store configured the database-backed store is left as it is.
func TestAttachObjectStoreDisabled(t *testing.T) {
	pg := &storage.PostgresStore{}
	cfg := config.Config{FileStorage: config.FileStorageConfig{Type: config.FileStoragePostgres}}
	got, err := attachObjectStore(pg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != storage.Store(pg) {
		t.Fatal("a disabled object store must leave the store unchanged")
	}
	if pg.UsesObjectStore() {
		t.Fatal("the store must not report an object store when none is configured")
	}
}

// With an object store configured the database-backed store takes it and
// reports that payloads live outside the database.
func TestAttachObjectStoreEnabled(t *testing.T) {
	pg := &storage.PostgresStore{}
	cfg := config.Config{FileStorage: config.FileStorageConfig{
		Type: config.FileStorageS3,
		S3:   config.S3Config{Bucket: "bank-data"},
	}}
	got, err := attachObjectStore(pg, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got != storage.Store(pg) {
		t.Fatal("the database-backed store must be returned with the object store attached")
	}
	if !pg.UsesObjectStore() {
		t.Fatal("the store must report an object store once one is attached")
	}
}

// The adapter translates the object store's miss into the storage area's own
// sentinel, so a caller cannot tell which backend held the payload.
func TestObjectStoreAdapterTranslatesMiss(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	adapter := testObjectStoreS3(t, config.S3Config{Bucket: "bucket", Endpoint: server.URL})
	if _, _, err := adapter.Get(context.Background(), "absent"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("err = %v, want the storage area's miss sentinel", err)
	}
}

// A write through the adapter reaches the configured endpoint with the payload.
func TestObjectStoreAdapterPutReachesEndpoint(t *testing.T) {
	var stored []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		stored, _ = io.ReadAll(r.Body)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	adapter := testObjectStoreS3(t, config.S3Config{Bucket: "bucket", Endpoint: server.URL})
	if err := adapter.Put(context.Background(), "banks/b1/f.bin", []byte("payload"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if string(stored) != "payload" {
		t.Fatalf("stored = %q, want the payload", stored)
	}
}

// testObjectStoreS3 builds the S3 payload backend through the same selection
// the composition root uses, so a test exercises the real constructor.
func testObjectStoreS3(t *testing.T, cfg config.S3Config) storage.ObjectStore {
	t.Helper()
	store, err := newObjectStore(config.FileStorageConfig{Type: config.FileStorageS3, S3: cfg})
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// streamingAdapter is the method set a store must expose for the storage area
// to serve a payload without buffering it.
type streamingAdapter interface {
	OpenStream(ctx context.Context, key string) (io.ReadCloser, string, int64, error)
}

// The adapter exposes the object store's streaming read, so a stored-file
// handler can write the first chunk without the whole object resident. Without
// this method the storage area falls back to the buffered Get, which defeats
// the streaming path.
func TestObjectStoreAdapterStreams(t *testing.T) {
	body := "streamed-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(server.Close)

	streamer, ok := testObjectStoreS3(t, config.S3Config{Bucket: "bucket", Endpoint: server.URL}).(streamingAdapter)
	if !ok {
		t.Fatal("the adapter must expose the streaming capability")
	}
	reader, contentType, size, err := streamer.OpenStream(context.Background(), "banks/b1/f.bin")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reader.Close() }()
	if contentType != "application/octet-stream" {
		t.Fatalf("content type = %q", contentType)
	}
	if size != int64(len(body)) {
		t.Fatalf("size = %d, want %d", size, len(body))
	}
	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}

// A streaming miss is translated into the storage area's sentinel, so a caller
// cannot tell which backend held the payload.
func TestObjectStoreAdapterStreamMissIsTranslated(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(server.Close)

	streamer := testObjectStoreS3(t, config.S3Config{Bucket: "bucket", Endpoint: server.URL}).(streamingAdapter)
	if _, _, _, err := streamer.OpenStream(context.Background(), "absent"); !errors.Is(err, storage.ErrObjectNotFound) {
		t.Fatalf("err = %v, want the storage area's miss sentinel", err)
	}
}
