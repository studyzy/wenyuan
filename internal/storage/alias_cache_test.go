package storage

import (
	"context"
	"testing"
	"time"
)

// aliasTestBank creates a bank so an alias can be attached to it.
func aliasTestBank(t *testing.T, store *MemoryStore, id string) {
	t.Helper()
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: id, Name: id}); err != nil {
		t.Fatalf("create bank %q: %v", id, err)
	}
}

// moveAliasBehindTheCache rewrites the store's alias table without going
// through AddAlias, standing in for a write another process made: the process
// cache must not learn about it.
func moveAliasBehindTheCache(store *MemoryStore, alias, owner string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.aliasOwners[alias] = owner
}

// A resolution repeated inside the TTL is answered from the cache, so it never
// reaches the store a second time.
func TestAliasCacheServesRepeatedResolution(t *testing.T) {
	store := NewMemoryStore("public")
	aliasTestBank(t, store, "b")
	if _, err := store.AddAlias(context.TODO(), "b", "nick", false); err != nil {
		t.Fatal(err)
	}
	aliasCachePtr.Store(newAliasCache(time.Hour, 0))
	defer SetBankAliasCache(0, 0)

	first, err := store.ResolveBankID(context.TODO(), "nick")
	if err != nil || first != "b" {
		t.Fatalf("first resolve = %q, err = %v", first, err)
	}
	// A write the store makes behind the cache's back. If the second resolution
	// read the store it would see the new owner; it must answer from the cache.
	moveAliasBehindTheCache(store, "nick", "moved")

	second, err := store.ResolveBankID(context.TODO(), "nick")
	if err != nil {
		t.Fatal(err)
	}
	if second != "b" {
		t.Fatalf("second resolve = %q, want the cached %q: a hit inside the TTL must not read the store", second, "b")
	}
}

// A resolution repeated past the TTL reads the store again, so a write made by
// another process becomes visible once the entry expires.
func TestAliasCacheRereadsAfterTTL(t *testing.T) {
	store := NewMemoryStore("public")
	aliasTestBank(t, store, "b")
	if _, err := store.AddAlias(context.TODO(), "b", "nick", false); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	cache := newAliasCache(time.Minute, 0)
	cache.clock = func() time.Time { return now }
	aliasCachePtr.Store(cache)
	defer SetBankAliasCache(0, 0)

	if resolved, err := store.ResolveBankID(context.TODO(), "nick"); err != nil || resolved != "b" {
		t.Fatalf("resolve = %q, err = %v", resolved, err)
	}
	moveAliasBehindTheCache(store, "nick", "moved")
	if resolved, _ := store.ResolveBankID(context.TODO(), "nick"); resolved != "b" {
		t.Fatalf("inside the TTL the store must not be read: got %q", resolved)
	}

	now = now.Add(2 * time.Minute)
	resolved, err := store.ResolveBankID(context.TODO(), "nick")
	if err != nil {
		t.Fatal(err)
	}
	if resolved != "moved" {
		t.Fatalf("past the TTL the store must be read again: got %q, want %q", resolved, "moved")
	}
}

// An alias write in this process is visible to the very next resolution: the
// entry it invalidates is dropped immediately, not after the TTL.
func TestAliasCacheWriteInvalidatesImmediately(t *testing.T) {
	store := NewMemoryStore("public")
	aliasTestBank(t, store, "b")
	if _, err := store.AddAlias(context.TODO(), "b", "nick", false); err != nil {
		t.Fatal(err)
	}
	aliasCachePtr.Store(newAliasCache(time.Hour, 0))
	defer SetBankAliasCache(0, 0)

	if resolved, _ := store.ResolveBankID(context.TODO(), "nick"); resolved != "b" {
		t.Fatalf("resolve = %q, want b", resolved)
	}
	if _, err := store.RemoveAlias(context.TODO(), "b", "nick"); err != nil {
		t.Fatal(err)
	}
	if resolved, _ := store.ResolveBankID(context.TODO(), "nick"); resolved != "nick" {
		t.Fatalf("a removed alias must stop routing at once: got %q", resolved)
	}

	// A cached miss is invalidated the same way: the negative "later" is cached,
	// then the alias is created, and the next resolution must find it.
	if resolved, _ := store.ResolveBankID(context.TODO(), "later"); resolved != "later" {
		t.Fatalf("a miss resolves to itself: got %q", resolved)
	}
	if _, err := store.AddAlias(context.TODO(), "b", "later", false); err != nil {
		t.Fatal(err)
	}
	if resolved, _ := store.ResolveBankID(context.TODO(), "later"); resolved != "b" {
		t.Fatalf("a new alias must be visible at once: got %q, want b", resolved)
	}
}

// A cache at its bound evicts rather than growing without limit, and it drops
// the entries that have expired first so a live one is not the one removed.
func TestAliasCacheEvictsExpiredEntriesFirst(t *testing.T) {
	now := time.Now().UTC()
	cache := newAliasCache(time.Hour, 2)
	cache.clock = func() time.Time { return now }
	cache.put("public", "old", "A")
	// "old" expires at now+1h; move past that and store a fresh entry.
	now = now.Add(90 * time.Minute)
	cache.put("public", "live", "B")

	cache.put("public", "fresh", "C")

	if _, ok := cache.get("public", "old"); ok {
		t.Fatal("an expired entry must be the one an eviction takes")
	}
	if got, ok := cache.get("public", "live"); !ok || got != "B" {
		t.Fatalf("a live entry was evicted before the expired one: got %q, ok = %v", got, ok)
	}
	if got, ok := cache.get("public", "fresh"); !ok || got != "C" {
		t.Fatalf("the entry just stored is missing: got %q, ok = %v", got, ok)
	}
}

// With no expired entry to drop, an eviction takes the one that expires
// soonest, so the entry every reader just touched survives.
func TestAliasCacheBoundEvictsSoonestToExpire(t *testing.T) {
	cache := newAliasCache(time.Hour, 1)
	cache.put("public", "first", "A")
	cache.put("public", "second", "B")

	if len(cache.entries) != 1 {
		t.Fatalf("cache holds %d entries, want the bound of 1", len(cache.entries))
	}
	if got, ok := cache.get("public", "second"); !ok || got != "B" {
		t.Fatalf("the entry just stored is missing: got %q, ok = %v", got, ok)
	}
}

// A zero bound leaves the cache unbounded, which is what a deployment that
// configured no bound asks for.
func TestAliasCacheWithoutBoundKeepsEveryEntry(t *testing.T) {
	cache := newAliasCache(time.Hour, 0)
	for _, name := range []string{"a", "b", "c", "d"} {
		cache.put("public", name, name)
	}
	if len(cache.entries) != 4 {
		t.Fatalf("cache holds %d entries, want 4", len(cache.entries))
	}
}

// Two schemas that choose the same name never share an entry.
func TestAliasCacheScopesBySchema(t *testing.T) {
	cache := newAliasCache(time.Hour, 0)
	cache.put("tenant_a", "shared", "A")
	if _, ok := cache.get("tenant_b", "shared"); ok {
		t.Fatal("a cache lookup must not leak across tenants")
	}
	if got, ok := cache.get("tenant_a", "shared"); !ok || got != "A" {
		t.Fatalf("the owning tenant must still find its entry: got %q, ok = %v", got, ok)
	}
}

// A non-positive lifetime disables the cache, so every resolution reads the
// store and nothing is retained.
func TestAliasCacheDisabledWithoutLifetime(t *testing.T) {
	if cache := newAliasCache(0, 1024); cache != nil {
		t.Fatal("a zero lifetime must disable the cache")
	}
	if cache := newAliasCache(-time.Second, 1024); cache != nil {
		t.Fatal("a negative lifetime must disable the cache")
	}
	// A nil cache tolerates the read and write path so the caller needs no
	// branch of its own.
	nilCache := newAliasCache(0, 0)
	if _, ok := nilCache.get("public", "b"); ok {
		t.Fatal("a disabled cache must never hit")
	}
	nilCache.put("public", "b", "b")
	nilCache.invalidate("public", "b")

	SetBankAliasCache(0, 0)
	defer SetBankAliasCache(0, 0)
	if aliasCacheRef() != nil {
		t.Fatal("no configured lifetime must leave the cache off")
	}

	store := NewMemoryStore("public")
	aliasTestBank(t, store, "b")
	if _, err := store.AddAlias(context.TODO(), "b", "nick", false); err != nil {
		t.Fatal(err)
	}
	if resolved, _ := store.ResolveBankID(context.TODO(), "nick"); resolved != "b" {
		t.Fatalf("resolve = %q, want b", resolved)
	}
	// With the cache off nothing is retained, so a write another process made is
	// visible on the very next resolution.
	moveAliasBehindTheCache(store, "nick", "moved")
	if resolved, _ := store.ResolveBankID(context.TODO(), "nick"); resolved != "moved" {
		t.Fatalf("with the cache off the store must answer: got %q, want moved", resolved)
	}
}
