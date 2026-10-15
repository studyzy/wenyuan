// Package webhooks serves the webhook route family, including the guard that
// keeps a registered URL from reaching the service's own network.
package webhooks

import (
	"fmt"
	"net"
	"net/url"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// GuardConfig tunes the URL guard.
type GuardConfig struct {
	// AllowInsecure permits plain HTTP. A production deployment leaves this
	// false so a delivery cannot be downgraded to cleartext.
	AllowInsecure bool
	// AllowPrivateHosts permits loopback, link-local and private addresses. It
	// exists for tests and for a deployment that deliberately delivers inside
	// its own network; the default refuses them.
	AllowPrivateHosts bool
	// AllowedHosts, when non-empty, is the only host set a delivery may reach.
	AllowedHosts []string
}

// Guard validates a webhook target URL.
//
// A registered URL is fetched by the server, so an unchecked one is a
// server-side request forgery: it could reach the metadata service, an internal
// admin API, or a loopback port that is not otherwise exposed. The guard
// therefore refuses anything but an absolute HTTPS URL whose host resolves only
// to public addresses.
type Guard struct{ cfg GuardConfig }

// NewGuard builds a guard from the configuration.
func NewGuard(cfg GuardConfig) *Guard { return &Guard{cfg: cfg} }

// Validate reports whether a URL may be stored as a webhook target.
func (g *Guard) Validate(raw string) error {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return fmt.Errorf("%w: url is required", storage.ErrWebhookURLNotAllowed)
	}
	parsed, err := url.Parse(trimmed)
	if err != nil {
		return fmt.Errorf("%w: url is not parseable", storage.ErrWebhookURLNotAllowed)
	}
	// The reference accepts both schemes: SSRF is stopped by refusing the
	// address, not by requiring TLS, because a plain-HTTP receiver on a
	// private address is exactly what an operator allowlists for testing.
	switch parsed.Scheme {
	case "http", "https":
	default:
		return fmt.Errorf("%w: scheme %q is not accepted", storage.ErrWebhookURLNotAllowed, parsed.Scheme)
	}
	if parsed.Host == "" {
		return fmt.Errorf("%w: url has no host", storage.ErrWebhookURLNotAllowed)
	}
	// Credentials in the URL would be sent on every delivery and cannot be
	// rotated, so they are refused.
	if parsed.User != nil {
		return fmt.Errorf("%w: url must not carry credentials", storage.ErrWebhookURLNotAllowed)
	}

	host := parsed.Hostname()
	// An allowlist entry is an exemption from the private-address block, not a
	// whole-bank restriction: it names the hosts an operator has deliberately
	// permitted (a loopback receiver in a test, an internal endpoint).
	if g.hostAllowed(host) || g.addressAllowed(ipLiteral(host)) {
		return nil
	}
	if g.cfg.AllowPrivateHosts {
		return nil
	}
	// Registration is a syntactic check. A literal address in a blocked range
	// is refused here so the caller learns immediately; a DNS name is accepted
	// and vetted when it is resolved, which keeps the request path off the
	// network and is where a rebind is actually caught.
	if ip := ipLiteral(host); ip != nil {
		return g.refuseAddress(ip)
	}
	return nil
}

// ipLiteral parses a host that is a literal IP address, or nil for a DNS name.
// A legacy dotted-IPv4 shorthand the resolver folds onto an address is not
// recognised here and is treated as a name, which is vetted when it is dialed.
func ipLiteral(host string) net.IP {
	return net.ParseIP(strings.Trim(host, "[]"))
}

// addressAllowed reports whether an address is named by the allowlist, either
// as a literal or as CIDR membership.
func (g *Guard) addressAllowed(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, allowed := range g.cfg.AllowedHosts {
		entry := strings.TrimSpace(allowed)
		if entry == "" {
			continue
		}
		if parsed := net.ParseIP(entry); parsed != nil && parsed.Equal(ip) {
			return true
		}
		if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

// refuseAddress reports an address in a range that is unsafe for a
// caller-supplied fetch.
func (g *Guard) refuseAddress(ip net.IP) error {
	if isPrivateAddress(ip) {
		return fmt.Errorf("%w: address %s is not a public address", storage.ErrWebhookURLNotAllowed, ip)
	}
	return nil
}

// hostAllowed reports whether a host matches the allowlist exactly.
func (g *Guard) hostAllowed(host string) bool {
	for _, allowed := range g.cfg.AllowedHosts {
		if strings.EqualFold(strings.TrimSpace(allowed), host) {
			return true
		}
	}
	return false
}

// CheckResolvedAddress reports whether a delivery may connect to the given
// resolved address. Validate checks the name before the request; this checks
// the address the connection actually uses, so a name that resolves publicly
// when it is validated and privately when it is dialed (DNS rebinding) is still
// refused. The delivery transport installs it as the dialer's control, which
// runs after resolution and before the connect.
func (g *Guard) CheckResolvedAddress(address string) error {
	ip := net.ParseIP(address)
	if ip == nil {
		return fmt.Errorf("%w: %q is not an address", storage.ErrWebhookURLNotAllowed, address)
	}
	if g.cfg.AllowPrivateHosts || g.addressAllowed(ip) {
		return nil
	}
	if isPrivateAddress(ip) {
		return fmt.Errorf("%w: address %s is not a public address", storage.ErrWebhookURLNotAllowed, ip)
	}
	return nil
}

// isPrivateAddress reports whether an address is loopback, link-local,
// unspecified, multicast, or inside a private range.
func isPrivateAddress(ip net.IP) bool {
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip.IsPrivate() {
		return true
	}
	// The IPv4 and IPv6 metadata endpoints are link-local, but they are also
	// commonly reached at a fixed address that is not otherwise private.
	metadata := net.ParseIP("169.254.169.254")
	if metadata != nil && ip.Equal(metadata) {
		return true
	}
	return false
}

// ErrNotAllowed re-exports the storage sentinel so a caller can compare with
// errors.Is against either name.
var ErrNotAllowed = storage.ErrWebhookURLNotAllowed
