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

// test-guard-refuses-other-schemes-and-shapes: only an absolute
// credential-free HTTP(S) URL is accepted. `http` is accepted because the
// reference accepts it: what makes a target safe is the address it resolves to,
// not the scheme, and a plain-HTTP receiver is exactly what an operator
// allowlists for testing.
func TestGuardRefusesOtherSchemes(t *testing.T) {
	guard := NewGuard(GuardConfig{})
	cases := []struct {
		name string
		url  string
	}{
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

// test-guard-honours-an-allowlist: the allowlist is an exemption from the
// private-address block, the way the reference defines it — it names the hosts
// an operator has deliberately permitted, and it does not restrict anything
// else. A private host that is not on it is still refused.
func TestGuardHonoursAllowlist(t *testing.T) {
	guard := NewGuard(GuardConfig{AllowedHosts: []string{"127.0.0.1"}})
	if err := guard.Validate("http://127.0.0.1:8080/endpoint"); err != nil {
		t.Fatalf("an allowlisted host must be accepted, http included: %v", err)
	}
	if err := guard.Validate("http://10.0.0.5/endpoint"); err == nil {
		t.Fatal("a private host outside the allowlist must be refused")
	}
	if err := guard.Validate("https://example.com/endpoint"); err != nil {
		t.Fatalf("a public host need not be allowlisted: %v", err)
	}
}

// test-guard-requires-https: the reference accepts http and https alike, at
// registration and at delivery.
func TestGuardAcceptsPlainHTTP(t *testing.T) {
	guard := NewGuard(GuardConfig{AllowPrivateHosts: true})
	if err := guard.Validate("http://hooks.example.com/endpoint"); err != nil {
		t.Fatalf("plain http must be accepted: %v", err)
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

// test-dial-time-address-check: the delivery transport vets the resolved
// address, so a name that resolves differently between validation and the
// connection is refused at the hop it would actually dial.
func TestGuardCheckResolvedAddress(t *testing.T) {
	strict := NewGuard(GuardConfig{})
	if err := strict.CheckResolvedAddress("10.0.0.5"); err == nil ||
		!errors.Is(err, storage.ErrWebhookURLNotAllowed) {
		t.Fatalf("a private resolved address must be refused, got %v", err)
	}
	if err := strict.CheckResolvedAddress("8.8.8.8"); err != nil {
		t.Fatalf("a public resolved address must be accepted: %v", err)
	}
	if err := strict.CheckResolvedAddress("not-an-address"); err == nil {
		t.Fatal("an unparsable address must be refused")
	}
	permissive := NewGuard(GuardConfig{AllowPrivateHosts: true})
	if err := permissive.CheckResolvedAddress("127.0.0.1"); err != nil {
		t.Fatalf("the opt-in guard must accept a private resolved address: %v", err)
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
