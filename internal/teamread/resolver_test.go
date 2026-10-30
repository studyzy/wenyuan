package teamread

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// stubRegistry answers ResolveProjectForMember from a map and counts calls.
type stubRegistry struct {
	storage.TeamRegistry
	projects map[string]tenant.TeamProject
	err      error
	calls    int
}

func (s *stubRegistry) ResolveProjectForMember(_ context.Context, tenantID, projectName string) (tenant.TeamProject, error) {
	s.calls++
	if s.err != nil {
		return tenant.TeamProject{}, s.err
	}
	project, ok := s.projects[tenantID+"\x00"+projectName]
	if !ok {
		return tenant.TeamProject{}, tenant.ErrNotFound
	}
	return project, nil
}

func TestParseBankID(t *testing.T) {
	cases := []struct {
		name       string
		bankID     string
		wantTenant string
		wantName   string
		wantOK     bool
	}{
		{"two-part", "alice::Web", "alice", "Web", true},
		{"no separator", "alice", "", "", false},
		{"empty prefix", "::Web", "", "", false},
		{"empty name", "alice::", "", "", false},
		{"name with separator", "alice::a::b", "alice", "a::b", true},
		{"leading separator only", "::", "", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tenantID, name, ok := ParseBankID(tc.bankID)
			if ok != tc.wantOK || tenantID != tc.wantTenant || name != tc.wantName {
				t.Fatalf("ParseBankID(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.bankID, tenantID, name, ok, tc.wantTenant, tc.wantName, tc.wantOK)
			}
		})
	}
}

// TestResolveFallsBackForAMember pins the only case that produces a scope: the
// bank id carries the caller's own tenant and the registry says they take part in
// that project.
func TestResolveFallsBackForAMember(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{
		"alice\x00Web": {TeamID: "acme", ProjectID: "web", Name: "Web", BankID: "acme::Web"},
	}}
	resolver := NewResolver(registry)
	scope, err := resolver.Resolve(context.Background(), "alice", "alice::Web")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scope == nil {
		t.Fatal("a member's read did not fall back")
	}
	if scope.Schema != "acme" || scope.BankID != "acme::Web" {
		t.Fatalf("scope = %+v, want the team schema and bank", scope)
	}
}

// TestResolveStaysPutWhenNotAMember pins that membership is what authorises the
// redirect: a bank id that merely looks like a project's is not enough.
func TestResolveStaysPutWhenNotAMember(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{}}
	resolver := NewResolver(registry)
	scope, err := resolver.Resolve(context.Background(), "alice", "alice::Web")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scope != nil {
		t.Fatalf("scope = %+v, want no fallback for a non-member", scope)
	}
}

// TestResolveIgnoresAnotherTenantsBank pins that the prefix must be the caller's
// own tenant: Bob's bank is Bob's to read, not a team's to serve.
func TestResolveIgnoresAnotherTenantsBank(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{
		// Even if a registry row existed for it, the prefix check refuses first.
		"alice\x00Web": {TeamID: "acme", BankID: "acme::Web"},
	}}
	resolver := NewResolver(registry)
	scope, err := resolver.Resolve(context.Background(), "alice", "bob::Web")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scope != nil {
		t.Fatalf("scope = %+v, want no fallback for another tenant's bank", scope)
	}
	if registry.calls != 0 {
		t.Fatalf("registry was consulted %d times for another tenant's bank, want 0", registry.calls)
	}
}

// TestResolveIgnoresAnUnseparatedBankID pins that a plain bank id has no team
// meaning at all.
func TestResolveIgnoresAnUnseparatedBankID(t *testing.T) {
	resolver := NewResolver(&stubRegistry{})
	scope, err := resolver.Resolve(context.Background(), "alice", "plainbank")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scope != nil {
		t.Fatalf("scope = %+v, want no fallback", scope)
	}
}

// TestResolveIsNilWithoutARegistry pins the single-tenant path: no registry means
// no fallback, and the resolver object itself may be nil.
func TestResolveIsNilWithoutARegistry(t *testing.T) {
	if NewResolver(nil) != nil {
		t.Fatal("a resolver over no registry must be nil")
	}
	var resolver *Resolver
	scope, err := resolver.Resolve(context.Background(), "alice", "alice::Web")
	if err != nil {
		t.Fatalf("Resolve on a nil resolver: %v", err)
	}
	if scope != nil {
		t.Fatalf("scope = %+v, want no fallback", scope)
	}
}

// TestResolveCachesBothAnswers pins the cache: a membership and a
// non-membership are both reused, because the ordinary read is the one that must
// not pay a registry round trip.
func TestResolveCachesBothAnswers(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{
		"alice\x00Web": {TeamID: "acme", BankID: "acme::Web"},
	}}
	resolver := NewResolver(registry)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		if _, err := resolver.Resolve(ctx, "alice", "alice::Web"); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
		if _, err := resolver.Resolve(ctx, "alice", "alice::Other"); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	if registry.calls != 2 {
		t.Fatalf("registry calls = %d, want 2 (one per distinct key)", registry.calls)
	}
}

// TestResolveExpiresACachedAnswer pins that a membership change is seen once the
// TTL passes, which is the only invalidation a multi-process deployment can
// honestly offer.
func TestResolveExpiresACachedAnswer(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{}}
	resolver := NewResolver(registry)
	resolver.ttl = time.Millisecond
	ctx := context.Background()
	if scope, _ := resolver.Resolve(ctx, "alice", "alice::Web"); scope != nil {
		t.Fatal("a non-member fell back")
	}
	registry.projects["alice\x00Web"] = tenant.TeamProject{TeamID: "acme", BankID: "acme::Web"}
	time.Sleep(5 * time.Millisecond)
	scope, err := resolver.Resolve(ctx, "alice", "alice::Web")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scope == nil {
		t.Fatal("the cached non-membership survived its TTL")
	}
}

// TestResolveFlushDropsTheCache pins the explicit invalidation a test or a console
// action uses.
func TestResolveFlushDropsTheCache(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{}}
	resolver := NewResolver(registry)
	ctx := context.Background()
	if _, err := resolver.Resolve(ctx, "alice", "alice::Web"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	resolver.Flush()
	if _, err := resolver.Resolve(ctx, "alice", "alice::Web"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if registry.calls != 2 {
		t.Fatalf("registry calls = %d, want 2 after a flush", registry.calls)
	}
}

// TestResolvePropagatesARegistryFailure pins that a real storage failure is
// reported rather than silently treated as "not a member": the handler decides to
// degrade, and it cannot do that if the resolver hides the distinction.
func TestResolvePropagatesARegistryFailure(t *testing.T) {
	registry := &stubRegistry{err: errors.New("database is down")}
	resolver := NewResolver(registry)
	_, err := resolver.Resolve(context.Background(), "alice", "alice::Web")
	if err == nil {
		t.Fatal("a registry failure must be reported")
	}
}

// TestResolveCachesAtMostTheCap pins that the cache is bounded, so a deployment
// with many bank ids cannot grow it without limit.
func TestResolveCachesAtMostTheCap(t *testing.T) {
	resolver := NewResolver(&stubRegistry{projects: map[string]tenant.TeamProject{}})
	resolver.max = 2
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		bankID := "alice::project" + string(rune('a'+i))
		if _, err := resolver.Resolve(ctx, "alice", bankID); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	resolver.mu.RLock()
	entries := len(resolver.entries)
	resolver.mu.RUnlock()
	if entries > resolver.max {
		t.Fatalf("cache holds %d entries, want at most %d", entries, resolver.max)
	}
}

// TestInvalidateMemberDropsOnlyThatTenantsAnswers pins the targeted invalidation:
// a membership change must take effect at once for the tenant it concerns, without
// throwing away every other reader's cached answers.
func TestInvalidateMemberDropsOnlyThatTenantsAnswers(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{}}
	resolver := NewResolver(registry)
	ctx := context.Background()
	for _, bankID := range []string{"alice::Web", "bob::Web"} {
		if _, err := resolver.Resolve(ctx, strings.TrimSuffix(bankID, "::Web"), bankID); err != nil {
			t.Fatalf("Resolve: %v", err)
		}
	}
	if registry.calls != 2 {
		t.Fatalf("registry calls = %d, want 2 before invalidation", registry.calls)
	}
	// Alice joins the project; her cached non-membership must be gone.
	registry.projects["alice\x00Web"] = tenant.TeamProject{TeamID: "acme", BankID: "acme::Web"}
	resolver.InvalidateMember("alice")

	scope, err := resolver.Resolve(ctx, "alice", "alice::Web")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if scope == nil {
		t.Fatal("alice's membership was not seen after invalidation")
	}
	if registry.calls != 3 {
		t.Fatalf("registry calls = %d, want alice re-read (3)", registry.calls)
	}
	// Bob's answer survived.
	before := registry.calls
	if _, err := resolver.Resolve(ctx, "bob", "bob::Web"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if registry.calls != before {
		t.Fatal("bob's cached answer was dropped by alice's invalidation")
	}
}

// TestInvalidateMemberIsCaseInsensitive pins that the invalidation matches the
// cache's own key normalisation, or it would silently miss.
func TestInvalidateMemberIsCaseInsensitive(t *testing.T) {
	registry := &stubRegistry{projects: map[string]tenant.TeamProject{}}
	resolver := NewResolver(registry)
	ctx := context.Background()
	if _, err := resolver.Resolve(ctx, "Alice", "Alice::Web"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	resolver.InvalidateMember("alice")
	if _, err := resolver.Resolve(ctx, "Alice", "Alice::Web"); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if registry.calls != 2 {
		t.Fatalf("registry calls = %d, want the answer re-read", registry.calls)
	}
}

// TestInvalidateMemberIsNilSafe pins that a deployment without teams does not
// panic when a membership route runs.
func TestInvalidateMemberIsNilSafe(t *testing.T) {
	var resolver *Resolver
	resolver.InvalidateMember("alice")
}
