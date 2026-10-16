package storage

import (
	"testing"
	"time"
)

// A cache at its bound evicts rather than growing without limit, and it drops
// the entry that expires soonest first so a live row is not the one removed.
func TestBankCacheEvictsAtItsBound(t *testing.T) {
	cache := newBankCacheWithBound(time.Minute, 2)
	base := time.Now().UTC()
	cache.clock = func() time.Time { return base }
	cache.put("public", "b1", Bank{BankID: "b1"})
	cache.put("public", "b2", Bank{BankID: "b2"})
	cache.put("public", "b3", Bank{BankID: "b3"})

	if len(cache.entries) != 2 {
		t.Fatalf("cache holds %d entries, want the bound of 2", len(cache.entries))
	}
	if _, ok := cache.get("public", "b3"); !ok {
		t.Fatal("the entry just stored is missing")
	}
	if _, ok := cache.get("public", "b1"); ok && len(cache.entries) != 2 {
		t.Fatal("the cache grew past its bound on a read")
	}
}

// An expired entry is the one an eviction takes first, even when a live entry
// was stored earlier.
func TestBankCacheEvictsExpiredEntriesFirst(t *testing.T) {
	cache := newBankCacheWithBound(time.Minute, 2)
	base := time.Now().UTC()
	cache.clock = func() time.Time { return base }
	cache.put("public", "b1", Bank{BankID: "b1"})
	// b2 is stored later but expires sooner.
	cache.entries["public\x00b2"] = bankCacheEntry{bank: Bank{BankID: "b2"}, expiresAt: base.Add(time.Second)}

	cache.clock = func() time.Time { return base.Add(2 * time.Second) }
	cache.put("public", "b3", Bank{BankID: "b3"})

	if _, ok := cache.get("public", "b2"); ok {
		t.Fatal("the expired entry survived an eviction")
	}
	if _, ok := cache.get("public", "b1"); !ok {
		t.Fatal("a live entry was evicted before the expired one")
	}
}

// A zero bound leaves the cache unbounded, which is what a deployment that
// configured no bound asks for.
func TestBankCacheWithoutBoundKeepsEveryEntry(t *testing.T) {
	cache := newBankCache(time.Minute)
	for _, id := range []string{"b1", "b2", "b3", "b4"} {
		cache.put("public", id, Bank{BankID: id})
	}
	if len(cache.entries) != 4 {
		t.Fatalf("cache holds %d entries, want 4", len(cache.entries))
	}
}

// test-bank-stats-cache-bound: the per-process bank-statistics cache evicts at
// its configured bound rather than holding every summary a process has computed.
func TestBankStatsCacheEvictsAtItsBound(t *testing.T) {
	cache := newBankStatsCache(time.Minute, 2)
	base := time.Now().UTC()
	cache.clock = func() time.Time { return base }
	cache.put("public", "b1", BankStats{BankID: "b1"})
	cache.put("public", "b2", BankStats{BankID: "b2"})
	cache.put("public", "b3", BankStats{BankID: "b3"})

	if len(cache.entries) != 2 {
		t.Fatalf("cache holds %d entries, want the bound of 2", len(cache.entries))
	}
	if _, ok := cache.get("public", "b3"); !ok {
		t.Fatal("the summary just stored is missing")
	}
}

// A non-positive lifetime disables the summary cache, so every read computes
// afresh and nothing is retained.
func TestBankStatsCacheDisabledWithoutLifetime(t *testing.T) {
	cache := newBankStatsCache(0, 1024)
	if cache != nil {
		t.Fatal("a zero lifetime must disable the cache")
	}
	// A nil cache tolerates the read and write path so the caller needs no
	// branch of its own.
	if _, ok := cache.get("public", "b1"); ok {
		t.Fatal("a disabled cache must never hit")
	}
	cache.put("public", "b1", BankStats{BankID: "b1"})
}
