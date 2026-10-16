package storage

import (
	"sync"
	"time"
)

// BankCacheTTL is the lifetime of a per-process bank profile/configuration
// cache entry. A bank-row write in the same process invalidates its entry
// immediately; a write made by another process becomes visible only once the
// entry expires.
const BankCacheTTL = 5 * time.Second

// BankStatsCacheTTL is the lifetime of a bank-statistics summary in the
// cross-process cache table. The summary is approximate and powers a UI widget,
// so a few tens of seconds of staleness is safe and saves every polling client
// from re-running the aggregate.
const BankStatsCacheTTL = 30 * time.Second

// bankCache is a per-process, tenant-scoped cache of bank rows. It is never
// used to authorise or route a request and never retains a negative existence
// result: a miss is refetched.
type bankCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]bankCacheEntry
	clock   func() time.Time
	// maxEntries bounds how many bank rows one process holds. Zero means
	// unbounded, which is what a deployment that configured no bound asks for.
	maxEntries int
}

type bankCacheEntry struct {
	bank      Bank
	expiresAt time.Time
}

// newBankCache builds an empty cache bound to a TTL.
func newBankCache(ttl time.Duration) *bankCache {
	return newBankCacheWithBound(ttl, 0)
}

// newBankCacheWithBound builds an empty cache bound to a TTL and an entry
// count. The bound is what keeps a deployment that talks to many banks from
// holding every row it has ever read.
func newBankCacheWithBound(ttl time.Duration, maxEntries int) *bankCache {
	return &bankCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    map[string]bankCacheEntry{},
		clock:      func() time.Time { return time.Now().UTC() },
	}
}

// key scopes a cache key by tenant schema and bank identifier so two tenants
// that choose the same identifier never share a row.
func bankCacheKey(schema, bankID string) string {
	return schema + "\x00" + bankID
}

// get returns a cached bank row when present and unexpired.
func (c *bankCache) get(schema, bankID string) (Bank, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[bankCacheKey(schema, bankID)]
	if !ok || !c.clock().Before(entry.expiresAt) {
		return Bank{}, false
	}
	return entry.bank, true
}

// put stores a positive bank row under its tenant-scoped key.
//
// A cache at its bound drops the entries that have expired first, and only then
// the one expiring soonest: an eviction must never remove a row another reader
// would have hit.
func (c *bankCache) put(schema, bankID string, bank Bank) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.evictLocked(now)
	c.entries[bankCacheKey(schema, bankID)] = bankCacheEntry{
		bank:      bank,
		expiresAt: now.Add(c.ttl),
	}
}

// evictLocked makes room for one more entry: expired rows go first, then the
// soonest to expire.
func (c *bankCache) evictLocked(now time.Time) {
	if c.maxEntries <= 0 {
		return
	}
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	for len(c.entries) >= c.maxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.expiresAt.Before(oldest) {
				oldestKey, oldest = key, entry.expiresAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.entries, oldestKey)
	}
}

// invalidate drops one bank's entry.
func (c *bankCache) invalidate(bankID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key := range c.entries {
		if len(key) > len(bankID) && key[len(key)-len(bankID):] == bankID {
			delete(c.entries, key)
		}
	}
}

// bankStatsCache is the per-process, tenant-scoped cache of bank-statistics
// summaries. It is the counterpart of the reference's in-process stats cache,
// used where the cross-process table is not: the summary is expensive to compute
// and intentionally approximate, so serving it for a short window saves every
// polling client from re-running the aggregate.
//
// A nil cache is a disabled cache: a deployment that set no lifetime leaves the
// summary uncached and every read computes afresh.
type bankStatsCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]bankStatsEntry
	clock   func() time.Time
	// maxEntries bounds how many summaries one process holds. Zero means
	// unbounded, which is what a deployment that configured no bound asks for.
	maxEntries int
}

type bankStatsEntry struct {
	stats     BankStats
	expiresAt time.Time
}

// newBankStatsCache builds an empty summary cache bound to a lifetime and an
// entry count. A non-positive lifetime yields nil, so the caller's reads fall
// through to a fresh computation and nothing is stored.
func newBankStatsCache(ttl time.Duration, maxEntries int) *bankStatsCache {
	if ttl <= 0 {
		return nil
	}
	return &bankStatsCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    map[string]bankStatsEntry{},
		clock:      func() time.Time { return time.Now().UTC() },
	}
}

// get returns a cached summary when present and unexpired.
func (c *bankStatsCache) get(schema, bankID string) (BankStats, bool) {
	if c == nil {
		return BankStats{}, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[bankCacheKey(schema, bankID)]
	if !ok || !c.clock().Before(entry.expiresAt) {
		return BankStats{}, false
	}
	return entry.stats, true
}

// put stores a computed summary under its tenant-scoped key. A cache at its
// bound drops the entries that have expired first, and only then the one
// expiring soonest, so an eviction never removes a row another reader would
// have hit.
func (c *bankStatsCache) put(schema, bankID string, stats BankStats) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.evictLocked(now)
	c.entries[bankCacheKey(schema, bankID)] = bankStatsEntry{
		stats:     stats,
		expiresAt: now.Add(c.ttl),
	}
}

// evictLocked makes room for one more entry: expired rows go first, then the
// soonest to expire.
func (c *bankStatsCache) evictLocked(now time.Time) {
	if c.maxEntries <= 0 {
		return
	}
	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)
		}
	}
	for len(c.entries) >= c.maxEntries {
		var oldestKey string
		var oldest time.Time
		for key, entry := range c.entries {
			if oldestKey == "" || entry.expiresAt.Before(oldest) {
				oldestKey, oldest = key, entry.expiresAt
			}
		}
		if oldestKey == "" {
			return
		}
		delete(c.entries, oldestKey)
	}
}
