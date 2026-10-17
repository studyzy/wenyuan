package storage

import (
	"context"
	"errors"
	"testing"
)

// recordingObjectStore is an in-memory payload backend that records the calls
// the store made, so a test can assert payloads were routed away from the rows.
type recordingObjectStore struct {
	objects map[string][]byte
	types   map[string]string
	puts    int
	gets    int
	deletes int
}

func newRecordingObjectStore() *recordingObjectStore {
	return &recordingObjectStore{objects: map[string][]byte{}, types: map[string]string{}}
}

func (r *recordingObjectStore) Put(_ context.Context, key string, data []byte, contentType string) error {
	r.puts++
	r.objects[key] = data
	r.types[key] = contentType
	return nil
}

func (r *recordingObjectStore) Get(_ context.Context, key string) ([]byte, string, error) {
	r.gets++
	data, ok := r.objects[key]
	if !ok {
		return nil, "", ErrObjectNotFound
	}
	return data, r.types[key], nil
}

func (r *recordingObjectStore) Delete(_ context.Context, key string) error {
	r.deletes++
	delete(r.objects, key)
	delete(r.types, key)
	return nil
}

// A stored-file removal routes to the object store when one is attached, so an
// export operation's archive is dropped from the blob backend rather than
// orphaned there.
func TestObjectStoreDeleteRoutesThroughObjectStore(t *testing.T) {
	objectStore := newRecordingObjectStore()
	store := (&PostgresStore{}).WithObjectStore(objectStore)
	ctx := context.Background()
	key := StoredFileKeyForBank("b1", "exports.zip")
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, Data: []byte("bytes")}); err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteStoredFile(ctx, key); err != nil {
		t.Fatal(err)
	}
	if objectStore.deletes != 1 {
		t.Fatalf("object store deletes = %d, want the removal routed to it", objectStore.deletes)
	}
	if _, err := store.GetStoredFile(ctx, key); !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("payload survived the delete: err = %v", err)
	}
	// A key that names nothing is a no-op rather than a failure, so cleanup
	// never blocks on an already-gone blob.
	if err := store.DeleteStoredFile(ctx, key); err != nil {
		t.Fatalf("repeat delete = %v, want no error", err)
	}
}

// With an object store attached, a payload round-trips through it rather than
// the database, and the content type is derived from the key.
func TestStoredFileRoundTripsThroughObjectStore(t *testing.T) {
	objectStore := newRecordingObjectStore()
	store := (&PostgresStore{}).WithObjectStore(objectStore)
	if !store.UsesObjectStore() {
		t.Fatal("the store must report an object store once one is attached")
	}

	ctx := context.Background()
	key := StoredFileKeyForBank("b1", "attachment.pdf")
	if err := store.PutStoredFile(ctx, StoredFile{Key: key, Data: []byte("bytes")}); err != nil {
		t.Fatal(err)
	}
	if objectStore.puts != 1 {
		t.Fatalf("object store puts = %d, want the payload routed to it", objectStore.puts)
	}
	got, err := store.GetStoredFile(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if string(got.Data) != "bytes" {
		t.Errorf("data = %q, want the stored payload", got.Data)
	}
	if got.ContentType == "" {
		t.Error("content type must be derived from the key")
	}
}

// A miss in the object store surfaces as the storage area's own sentinel, so a
// caller cannot tell which backend held the payload.
func TestObjectStoreMissUsesStoredFileSentinel(t *testing.T) {
	store := (&PostgresStore{}).WithObjectStore(newRecordingObjectStore())
	_, err := store.GetStoredFile(context.Background(), StoredFileKeyForBank("b1", "absent.bin"))
	if !errors.Is(err, ErrStoredFileNotFound) {
		t.Fatalf("err = %v, want ErrStoredFileNotFound", err)
	}
}

// An invalid key is rejected before any backend is consulted, so a malformed
// caller key cannot reach the object store.
func TestObjectStoreRejectsInvalidKey(t *testing.T) {
	objectStore := newRecordingObjectStore()
	store := (&PostgresStore{}).WithObjectStore(objectStore)
	if err := store.PutStoredFile(context.Background(), StoredFile{Key: "../escape", Data: []byte("x")}); err == nil {
		t.Fatal("an invalid key must be rejected")
	}
	if objectStore.puts != 0 {
		t.Fatalf("object store puts = %d, want none for an invalid key", objectStore.puts)
	}
}
