// Package teamread decides whether a bank-scoped read should be served from a
// team's bank instead of the member's own.
//
// It holds no HTTP and no SQL: the caller resolves a fallback scope here and hands
// it to the storage layer, which does the redirecting. Keeping the decision pure
// is what lets it be tested without a database, and what keeps the "which bank did
// this request read" question answerable in one place.
package teamread

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// DefaultCacheTTL is how long a resolved membership is reused.
//
// Membership changes (joining a project, leaving one, a team being deleted) take
// effect within this window. Invalidation is by expiry rather than by a push from
// the management plane: several processes serve reads and a push would have to
// reach every one of them, so the TTL is the only honest bound.
const DefaultCacheTTL = 30 * time.Second

// DefaultCacheMaxEntries bounds the cache. It is a flat size, and overflowing it
// clears the whole cache rather than evicting least-recently-used entries: a read
// path that must stay cheap is not the place for a second data structure.
const DefaultCacheMaxEntries = 10000

// ParseBankID splits a bank id into its tenant prefix and project id.
//
// The split is on the first "::", because the prefix is a tenant id (which cannot
// contain one) while the suffix is the project id (a slug, which cannot either). A
// bank id without the separator has no team meaning at all.
func ParseBankID(bankID string) (tenantID, projectID string, ok bool) {
	index := strings.Index(bankID, "::")
	if index <= 0 || index+2 >= len(bankID) {
		return "", "", false
	}
	return bankID[:index], bankID[index+2:], true
}

// Resolver turns a requested bank into the team bank that should be read in its
// place.
type Resolver struct {
	teams storage.TeamRegistry
	ttl   time.Duration
	max   int

	mu        sync.RWMutex
	entries   map[string]entry
	lastSweep time.Time
}

// entry is one cached answer.
type entry struct {
	scope   *storage.FallbackScope
	expires time.Time
}

// NewResolver returns a resolver over a team registry.
//
// A nil registry means the deployment has no teams, so the resolver answers "no
// fallback" for everything; that is what a single-tenant process does.
func NewResolver(teams storage.TeamRegistry) *Resolver {
	if teams == nil {
		return nil
	}
	return &Resolver{
		teams:   teams,
		ttl:     DefaultCacheTTL,
		max:     DefaultCacheMaxEntries,
		entries: map[string]entry{},
	}
}

// Resolve returns the team bank a read of bankID should use instead, or nil when
// the read must stay on its own bank.
//
// Three conditions decide it, in order: the bank id must carry the caller's own
// tenant as its prefix, the caller must be a member of a project with that name,
// and that project's team must be active. The bank id is only ever an address —
// it is what the caller typed — so membership is what authorises the redirect,
// never the id's shape.
func (r *Resolver) Resolve(ctx context.Context, callerTenantID, bankID string) (*storage.FallbackScope, error) {
	if r == nil {
		return nil, nil
	}
	prefix, projectID, ok := ParseBankID(bankID)
	if !ok {
		return nil, nil
	}
	// The prefix must be the caller's own tenant. A member reading "bob::web" is
	// reading Bob's bank, which is Bob's to read, not a team's to serve.
	if !strings.EqualFold(prefix, callerTenantID) {
		return nil, nil
	}
	key := strings.ToLower(callerTenantID) + "\x00" + projectID
	if cached, hit := r.load(key); hit {
		return cached, nil
	}
	scope, err := r.lookup(ctx, callerTenantID, projectID)
	if err != nil {
		return nil, err
	}
	r.store(key, scope)
	return scope, nil
}

// lookup asks the registry, without the cache.
func (r *Resolver) lookup(ctx context.Context, callerTenantID, projectID string) (*storage.FallbackScope, error) {
	project, err := r.teams.ResolveProjectForMember(ctx, callerTenantID, projectID)
	if err != nil {
		if errors.Is(err, tenant.ErrNotFound) {
			return nil, nil
		}
		return nil, err
	}
	if project.TeamID == "" || project.BankID == "" {
		return nil, nil
	}
	return &storage.FallbackScope{Schema: project.TeamID, BankID: project.BankID}, nil
}

// load reads a cached answer. A miss and an expiry are the same thing to the
// caller.
func (r *Resolver) load(key string) (*storage.FallbackScope, bool) {
	r.mu.RLock()
	cached, ok := r.entries[key]
	r.mu.RUnlock()
	if !ok || time.Now().After(cached.expires) {
		return nil, false
	}
	return cached.scope, true
}

// store records an answer, including a non-membership one.
//
// The negative answer is cached too: the common case is a bank id that names no
// shared project, so leaving those uncached would send every ordinary read to the
// registry.
func (r *Resolver) store(key string, scope *storage.FallbackScope) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if len(r.entries) >= r.max {
		r.entries = map[string]entry{}
	}
	r.entries[key] = entry{scope: scope, expires: now.Add(r.ttl)}
	r.lastSweep = now
}

// InvalidateMember drops the cached answers for one calling tenant.
//
// It is what makes a membership change take effect at once rather than after the
// TTL. The cache is keyed by the caller and the project id, so a tenant whose
// membership changed is exactly the set of keys to drop — a wider invalidation
// would throw away every other reader's answers for no reason.
func (r *Resolver) InvalidateMember(callerTenantID string) {
	if r == nil || callerTenantID == "" {
		return
	}
	prefix := strings.ToLower(callerTenantID) + "\x00"
	r.mu.Lock()
	defer r.mu.Unlock()
	for key := range r.entries {
		if strings.HasPrefix(key, prefix) {
			delete(r.entries, key)
		}
	}
}

// Flush drops every cached answer. It exists for a caller that knows membership
// changed and wants the next read to see it, such as a test.
func (r *Resolver) Flush() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries = map[string]entry{}
}
