package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Only a database-backed store takes an object store; the in-process backend
// keeps its payloads in memory and is returned unchanged.
func TestAttachObjectStoreSkipsMemoryBackend(t *testing.T) {
	memory := storage.NewMemoryStore("public")
	cfg := config.Config{FileStorage: config.FileStorageConfig{Type: config.FileStorageS3}}
	got := attachObjectStore(memory, cfg)
	if got != storage.Store(memory) {
		t.Fatalf("the in-process backend must be returned unchanged, got %T", got)
	}
}

// With no object store configured the database-backed store is left as it is.
func TestAttachObjectStoreDisabled(t *testing.T) {
	pg := &storage.PostgresStore{}
	cfg := config.Config{FileStorage: config.FileStorageConfig{Type: config.FileStoragePostgres}}
	if got := attachObjectStore(pg, cfg); got != storage.Store(pg) {
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
	if got := attachObjectStore(pg, cfg); got != storage.Store(pg) {
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

	adapter := newObjectStore(config.S3Config{Bucket: "bucket", Endpoint: server.URL})
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

	adapter := newObjectStore(config.S3Config{Bucket: "bucket", Endpoint: server.URL})
	if err := adapter.Put(context.Background(), "banks/b1/f.bin", []byte("payload"), "text/plain"); err != nil {
		t.Fatal(err)
	}
	if string(stored) != "payload" {
		t.Fatalf("stored = %q, want the payload", stored)
	}
}
