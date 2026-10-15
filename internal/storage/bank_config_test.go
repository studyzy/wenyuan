package storage

import (
	"context"
	"errors"
	"testing"
)

// A bank's configuration is readable on its own, without the rest of the row,
// and an unknown bank is reported rather than answered with an empty map.
func TestBankConfigReadsOverrides(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateBank(ctx, "b1", BankPatch{Config: &map[string]any{
		"enable_text_search": false,
		"retain_chunk_size":  500,
	}}); err != nil {
		t.Fatal(err)
	}

	config, err := store.BankConfig(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if enabled, _ := config["enable_text_search"].(bool); enabled {
		t.Fatalf("config = %+v, want the stored override", config)
	}
	if size, _ := config["retain_chunk_size"].(int); size != 500 {
		t.Fatalf("config = %+v, want the stored chunk size", config)
	}

	// A caller mutating what it received must not change what the store holds.
	config["retain_chunk_size"] = 1
	again, err := store.BankConfig(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if size, _ := again["retain_chunk_size"].(int); size != 500 {
		t.Fatalf("the store's config was mutated through the copy: %+v", again)
	}

	if _, err := store.BankConfig(ctx, "missing"); !errors.Is(err, ErrBankNotFound) {
		t.Fatalf("unknown bank = %v, want ErrBankNotFound", err)
	}
}

// The configuration read is fresh, not the cached row: a change written after a
// profile read is visible to the next configuration read.
func TestBankConfigIsNotServedFromTheCache(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	// Warm the cached row, then change the configuration.
	if _, err := store.GetBank(ctx, "b1"); err != nil {
		t.Fatal(err)
	}
	// A second store over the same backing map stands in for another server:
	// it has its own cache and must still observe the change.
	if _, err := store.UpdateBank(ctx, "b1", BankPatch{Config: &map[string]any{"retain_chunk_size": 900}}); err != nil {
		t.Fatal(err)
	}
	config, err := store.BankConfig(ctx, "b1")
	if err != nil {
		t.Fatal(err)
	}
	if size, _ := config["retain_chunk_size"].(int); size != 900 {
		t.Fatalf("config = %+v, want the fresh value", config)
	}
}
