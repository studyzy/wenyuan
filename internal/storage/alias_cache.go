package storage

import (
	"sync"
	"sync/atomic"
	"time"
)

// aliasCache is the per-process, tenant-scoped cache of alias resolutions. It
// is the Go counterpart of the reference's process-wide alias cache: a name that
// is looked up again within the TTL is answered without touching the store, and
// misses are cached too, because nearly every request names a real bank and
// without a negative entry each one would pay a store read that can only ever
// answer "not an alias".
//
// The cache is keyed on (schema, name), never the name alone: the schema is the
// tenant boundary and names are caller-chosen, so two tenants routinely pick the
// same string and a key without the schema would route one tenant's traffic into
// another's bank.
//
// A nil cache is a disabled cache: a deployment that configured no lifetime
// leaves every name read from the store, which is the behaviour before the cache
// existed.
type aliasCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]aliasCacheEntry
	clock   func() time.Time
	// maxEntries bounds how many resolutions one process holds. Zero means
	// unbounded, which is what a deployment that configured no bound asks for.
	maxEntries int
}

type aliasCacheEntry struct {
	bankID    string
	expiresAt time.Time
}

// newAliasCache builds an empty alias cache bound to a lifetime and an entry
// count. A non-positive lifetime yields nil, so the caller's resolutions fall
// through to the store and nothing is retained.
func newAliasCache(ttl time.Duration, maxEntries int) *aliasCache {
	if ttl <= 0 {
		return nil
	}
	return &aliasCache{
		ttl:        ttl,
		maxEntries: maxEntries,
		entries:    map[string]aliasCacheEntry{},
		clock:      func() time.Time { return time.Now().UTC() },
	}
}

// get returns the canonical bank id a name resolved to when the entry is present
// and unexpired.
func (c *aliasCache) get(schema, name string) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, ok := c.entries[bankCacheKey(schema, name)]
	if !ok || !c.clock().Before(entry.expiresAt) {
		return "", false
	}
	return entry.bankID, true
}

// put stores a name's resolution under its tenant-scoped key.
//
// A cache at its bound drops the entries that have expired first, and only then
// the one expiring soonest: an eviction must never remove a resolution another
// reader would have hit.
func (c *aliasCache) put(schema, name, bankID string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.clock()
	c.evictLocked(now)
	c.entries[bankCacheKey(schema, name)] = aliasCacheEntry{
		bankID:    bankID,
		expiresAt: now.Add(c.ttl),
	}
}

// evictLocked makes room for one more entry: expired rows go first, then the
// soonest to expire.
func (c *aliasCache) evictLocked(now time.Time) {
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

// invalidate drops one name's entry. It is called by every path that writes an
// alias, so a write made in this process is visible to the very next resolution;
// a write made by another process becomes visible only once the entry expires.
func (c *aliasCache) invalidate(schema, name string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.entries, bankCacheKey(schema, name))
}

// aliasCachePtr holds the process-wide alias cache. It is nil — disabled — until
// the composition root installs the configured one.
var aliasCachePtr atomic.Pointer[aliasCache]

// SetBankAliasCache installs the process-wide alias-resolution cache with the
// configured lifetime and entry bound. A non-positive lifetime disables it, so
// every resolution reads the store; that is the behaviour when no lifetime is
// configured. The composition root calls it once at startup.
func SetBankAliasCache(ttl time.Duration, maxEntries int) {
	aliasCachePtr.Store(newAliasCache(ttl, maxEntries))
}

// aliasCacheRef returns the installed cache, or nil when none is configured.
func aliasCacheRef() *aliasCache { return aliasCachePtr.Load() }
