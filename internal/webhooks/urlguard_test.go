package webhooks

import (
	"errors"
	"net"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// test-guard-refuses-internal-targets: a registered URL is fetched by the
// server, so a target that resolves inside the service's own network must be
// refused before it can ever be stored.
func TestGuardRefusesInternalTargets(t *testing.T) {
	guard := NewGuard(GuardConfig{})
	cases := []struct {
		name string
		url  string
	}{
		{"loopback literal", "https://127.0.0.1/hook"},
		{"loopback name", "https://localhost/hook"},
		{"ipv6 loopback", "https://[::1]/hook"},
		{"private range", "https://10.0.0.5/hook"},
		{"private 192.168", "https://192.168.1.10/hook"},
		{"private 172.16", "https://172.16.0.1/hook"},
		{"link local metadata", "https://169.254.169.254/latest/meta-data"},
		{"unspecified", "https://0.0.0.0/hook"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := guard.Validate(tc.url)
			if err == nil {
				t.Fatalf("%s must be refused", tc.url)
			}
			if !errors.Is(err, storage.ErrWebhookURLNotAllowed) {
				t.Fatalf("refusal must carry the not-allowed sentinel, got %v", err)
			}
		})
	}
}

// test-guard-refuses-other-schemes-and-shapes: only an absolute credential-free
// HTTPS URL is accepted.
func TestGuardRefusesOtherSchemes(t *testing.T) {
	guard := NewGuard(GuardConfig{})
	cases := []struct {
		name string
		url  string
	}{
		{"plain http by default", "http://example.com/hook"},
		{"file scheme", "file:///etc/passwd"},
		{"gopher scheme", "gopher://example.com/"},
		{"no scheme", "example.com/hook"},
		{"no host", "https:///hook"},
		{"credentials", "https://user:pass@example.com/hook"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := guard.Validate(tc.url); err == nil {
				t.Fatalf("%q must be refused", tc.url)
			}
		})
	}
}

// test-guard-allows-an-explicit-private-host-when-configured: a deployment that
// deliberately delivers inside its own network can opt in.
func TestGuardAllowsPrivateHostOnlyWhenConfigured(t *testing.T) {
	strict := NewGuard(GuardConfig{})
	if err := strict.Validate("https://10.0.0.5/hook"); err == nil {
		t.Fatal("the default guard must refuse a private address")
	}
	permissive := NewGuard(GuardConfig{AllowPrivateHosts: true})
	if err := permissive.Validate("https://10.0.0.5/hook"); err != nil {
		t.Fatalf("the opt-in guard must accept a private address: %v", err)
	}
}

// test-guard-honours-an-allowlist: when a host set is configured, only those
// hosts are accepted even when they are otherwise reachable.
func TestGuardHonoursAllowlist(t *testing.T) {
	guard := NewGuard(GuardConfig{
		AllowPrivateHosts: true,
		AllowedHosts:      []string{"hooks.example.com"},
	})
	if err := guard.Validate("https://hooks.example.com/endpoint"); err != nil {
		t.Fatalf("an allowlisted host must be accepted: %v", err)
	}
	if err := guard.Validate("https://other.example.com/endpoint"); err == nil {
		t.Fatal("a host outside the allowlist must be refused")
	}
}

// test-guard-requires-https: a plain-HTTP target is refused unless the
// deployment explicitly opts in, so a delivery cannot be downgraded.
func TestGuardRequiresHTTPS(t *testing.T) {
	strict := NewGuard(GuardConfig{AllowPrivateHosts: true})
	if err := strict.Validate("http://hooks.example.com/endpoint"); err == nil {
		t.Fatal("plain http must be refused by default")
	}
	permissive := NewGuard(GuardConfig{AllowPrivateHosts: true, AllowInsecure: true})
	if err := permissive.Validate("http://hooks.example.com/endpoint"); err != nil {
		t.Fatalf("the opt-in guard must accept http: %v", err)
	}
}

// test-private-address-detection: the classifier covers the ranges that matter
// for a server-side fetch.
func TestIsPrivateAddress(t *testing.T) {
	private := []string{"127.0.0.1", "::1", "10.1.2.3", "192.168.0.1", "172.20.0.1", "169.254.169.254", "0.0.0.0", "fe80::1"}
	for _, raw := range private {
		if !isPrivateAddress(parseIP(t, raw)) {
			t.Fatalf("%s must be classified private", raw)
		}
	}
	public := []string{"8.8.8.8", "1.1.1.1", "2606:4700:4700::1111"}
	for _, raw := range public {
		if isPrivateAddress(parseIP(t, raw)) {
			t.Fatalf("%s must be classified public", raw)
		}
	}
}

// parseIP parses a test address, failing the test on a malformed literal.
func parseIP(t *testing.T, raw string) net.IP {
	t.Helper()
	ip := net.ParseIP(raw)
	if ip == nil {
		t.Fatalf("test address %q is malformed", raw)
	}
	return ip
}
