package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

// streamingObjectStore is a recordingObjectStore that also exposes the
// streaming capability, so a test can tell a streaming read from a buffered
// one by which of the two it called.
type streamingObjectStore struct {
	recordingObjectStore
	streams int
}

func newStreamingObjectStore() *streamingObjectStore {
	return &streamingObjectStore{recordingObjectStore: *newRecordingObjectStore()}
}

func (s *streamingObjectStore) OpenStream(_ context.Context, key string) (io.ReadCloser, string, int64, error) {
	s.streams++
	data, ok := s.objects[key]
	if !ok {
		return nil, "", -1, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(data)), s.types[key], int64(len(data)), nil
}

// The in-memory backend exposes the streaming capability and streams the
// payload's bytes, media type and size. Without OpenStoredFile the assertion
// fails, so this is the test that pins the new capability to the backend.
func TestMemoryStoreStreamsStoredFile(t *testing.T) {
	var store Store = NewMemoryStore("public")
	streamer, ok := store.(StoredFileStreamer)
	if !ok {
		t.Fatal("the in-memory backend must expose the streaming capability")
	}

	ctx := context.Background()
	key := StoredFileKeyForBank("b1", "shot.png")
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, ContentType: "image/png", Data: []byte("png-bytes")}); err != nil {
		t.Fatal(err)
	}

	stream, err := streamer.OpenStoredFile(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Reader.Close() }()
	if stream.ContentType != "image/png" {
		t.Errorf("content type = %q", stream.ContentType)
	}
	if stream.Size != int64(len("png-bytes")) {
		t.Errorf("size = %d, want %d", stream.Size, len("png-bytes"))
	}
	got, err := io.ReadAll(stream.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "png-bytes" {
		t.Errorf("bytes = %q", got)
	}
}

// A streaming miss reports the same sentinel as the buffered read, so a caller
// treats them identically.
func TestMemoryStoreStreamMissIsNotFound(t *testing.T) {
	streamer := any(NewMemoryStore("public")).(StoredFileStreamer)
	_, err := streamer.OpenStoredFile(context.Background(), StoredFileKeyForBank("b1", "absent.bin"))
	if !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("err = %v, want ErrStoredFileNotFound", err)
	}
}

// With an object store that can stream, the stored-file read opens the object
// body directly rather than buffering it: the test asserts the streaming call
// was used and the buffered Get was not.
func TestPostgresStoreStreamsThroughObjectStore(t *testing.T) {
	objectStore := newStreamingObjectStore()
	store := (&PostgresStore{}).WithObjectStore(objectStore)
	if _, ok := any(objectStore).(objectStreamer); !ok {
		t.Fatal("the fake must expose the streaming capability")
	}

	ctx := context.Background()
	key := StoredFileKeyForBank("b1", "archive.zip")
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, Data: []byte("zip-bytes")}); err != nil {
		t.Fatal(err)
	}

	stream, err := store.OpenStoredFile(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Reader.Close() }()
	if objectStore.streams != 1 {
		t.Fatalf("object store streams = %d, want the streaming read used", objectStore.streams)
	}
	if objectStore.gets != 0 {
		t.Fatalf("object store gets = %d, want no buffered read when streaming is available", objectStore.gets)
	}
	if stream.ContentType != "application/zip" {
		t.Errorf("content type = %q, want the key-derived type when the backend reports none", stream.ContentType)
	}
	if stream.Size != int64(len("zip-bytes")) {
		t.Errorf("size = %d", stream.Size)
	}
	got, err := io.ReadAll(stream.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "zip-bytes" {
		t.Errorf("bytes = %q", got)
	}
}

// A streaming miss in the object store surfaces as the storage area's own
// sentinel, exactly as the buffered read does.
func TestPostgresStoreStreamMissIsNotFound(t *testing.T) {
	store := (&PostgresStore{}).WithObjectStore(newStreamingObjectStore())
	_, err := store.OpenStoredFile(context.Background(), StoredFileKeyForBank("b1", "absent.bin"))
	if !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("err = %v, want ErrStoredFileNotFound", err)
	}
}

// An object store without the streaming capability keeps working: the read
// falls back to the buffered path, so the capability is optional.
func TestPostgresStoreFallsBackWhenObjectStoreCannotStream(t *testing.T) {
	objectStore := newRecordingObjectStore()
	if _, ok := any(objectStore).(objectStreamer); ok {
		t.Fatal("the buffered fake must not expose the streaming capability")
	}
	store := (&PostgresStore{}).WithObjectStore(objectStore)

	ctx := context.Background()
	key := StoredFileKeyForBank("b1", "file.bin")
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, Data: []byte("buffered-bytes")}); err != nil {
		t.Fatal(err)
	}

	stream, err := store.OpenStoredFile(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Reader.Close() }()
	if objectStore.gets == 0 {
		t.Fatal("the buffered fallback must read the payload")
	}
	if stream.Size != int64(len("buffered-bytes")) {
		t.Errorf("size = %d", stream.Size)
	}
	got, err := io.ReadAll(stream.Reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "buffered-bytes" {
		t.Errorf("bytes = %q", got)
	}
}
