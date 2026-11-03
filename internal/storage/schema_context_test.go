package storage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestWithSchemaRoundTrip(t *testing.T) {
	for _, want := range []string{"acme", "AcmeTeam", "ACME"} {
		ctx, err := WithSchema(context.Background(), want)
		if err != nil {
			t.Fatalf("WithSchema(%q): %v", want, err)
		}
		schema, ok := SchemaFrom(ctx)
		if !ok || schema != want {
			t.Fatalf("SchemaFrom = %q/%v, want %q/true", schema, ok, want)
		}
	}
}

func TestWithSchemaRejectsInvalid(t *testing.T) {
	for _, schema := range []string{
		"", "a", "1abc", "Admin", "ADMIN", "acme-corp", "admin", "pg_temp", "PG_Temp",
		"information_schema", "Information_Schema", strings.Repeat("a", 64),
	} {
		if _, err := WithSchema(context.Background(), schema); !errors.Is(err, ErrInvalidSchema) {
			t.Errorf("WithSchema(%q) = %v, want ErrInvalidSchema", schema, err)
		}
	}
	if _, err := WithSchema(context.Background(), "public"); err != nil {
		t.Fatalf("WithSchema(public) = %v, want nil", err)
	}
}

func TestSchemaForSingleModeUsesDefault(t *testing.T) {
	store := &PostgresStore{defaultSchema: "public"}
	schema, err := store.schemaFor(context.Background())
	if err != nil {
		t.Fatalf("schemaFor: %v", err)
	}
	if schema != "public" {
		t.Fatalf("schema = %q, want public", schema)
	}
}

func TestSchemaForMultiModeRequiresContext(t *testing.T) {
	store := &PostgresStore{defaultSchema: "public", requireSchema: true}
	if _, err := store.schemaFor(context.Background()); !errors.Is(err, ErrSchemaRequired) {
		t.Fatalf("schemaFor(background) = %v, want ErrSchemaRequired", err)
	}
	ctx, err := WithSchema(context.Background(), "acme")
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	schema, err := store.schemaFor(ctx)
	if err != nil {
		t.Fatalf("schemaFor(with schema): %v", err)
	}
	if schema != "acme" {
		t.Fatalf("schema = %q, want acme", schema)
	}
}
