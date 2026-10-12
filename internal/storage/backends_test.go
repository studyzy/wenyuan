package storage

import (
	"context"
	"errors"
	"testing"
)

// ts-0032: backend selection honours configuration.
func TestSelectBackend(t *testing.T) {
	for _, name := range []string{"", "postgres", "postgresql", "pgx"} {
		backend, err := SelectBackend(name)
		if err != nil {
			t.Fatalf("backend %q: %v", name, err)
		}
		if _, ok := backend.(*PostgresBackend); !ok {
			t.Fatalf("backend %q resolved to %T", name, backend)
		}
	}
	backend, err := SelectBackend("memory")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := backend.(*MemoryBackend); !ok {
		t.Fatalf("memory backend resolved to %T", backend)
	}
	if _, err := SelectBackend("oracle"); !errors.Is(err, ErrUnknownBackend) {
		t.Fatalf("unsupported backend must error, got %v", err)
	}
}

// ts-0032: the store is opened against the configured schema target.
func TestMemoryBackendBindsSchema(t *testing.T) {
	backend := &MemoryBackend{}
	store, err := backend.Open(context.Background(), BackendConfig{Schema: "tenant_a"})
	if err != nil {
		t.Fatal(err)
	}
	memoryStore, ok := store.(*MemoryStore)
	if !ok {
		t.Fatalf("unexpected store type %T", store)
	}
	if memoryStore.Schema() != "tenant_a" {
		t.Fatalf("schema = %q", memoryStore.Schema())
	}
}

func TestPostgresBackendRequiresURL(t *testing.T) {
	backend := &PostgresBackend{}
	if _, err := backend.Open(context.Background(), BackendConfig{}); err == nil {
		t.Fatal("postgres backend must require a database url")
	}
}
