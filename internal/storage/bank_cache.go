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

// bankCache is a per-process, tenant-scoped cache of bank rows. It is never
// used to authorise or route a request and never retains a negative existence
// result: a miss is refetched.
type bankCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	entries map[string]bankCacheEntry
	clock   func() time.Time
}

type bankCacheEntry struct {
	bank      Bank
	expiresAt time.Time
}

// newBankCache builds an empty cache bound to a TTL.
func newBankCache(ttl time.Duration) *bankCache {
	return &bankCache{
		ttl:     ttl,
		entries: map[string]bankCacheEntry{},
		clock:   func() time.Time { return time.Now().UTC() },
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
func (c *bankCache) put(schema, bankID string, bank Bank) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[bankCacheKey(schema, bankID)] = bankCacheEntry{
		bank:      bank,
		expiresAt: c.clock().Add(c.ttl),
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
