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
	switch parsed.Scheme {
	case "https":
	case "http":
		if !g.cfg.AllowInsecure {
			return fmt.Errorf("%w: only https is accepted", storage.ErrWebhookURLNotAllowed)
		}
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
	if len(g.cfg.AllowedHosts) > 0 && !g.hostAllowed(host) {
		return fmt.Errorf("%w: host %q is not on the allowlist", storage.ErrWebhookURLNotAllowed, host)
	}
	if g.cfg.AllowPrivateHosts {
		return nil
	}
	return g.refusePrivateAddresses(host)
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

// refusePrivateAddresses resolves a host and refuses it when any address it
// resolves to is not a public one.
//
// Every resolved address is checked rather than only the first, because a
// hostile name can return a public address first and a private one second.
func (g *Guard) refusePrivateAddresses(host string) error {
	// A literal address needs no resolution.
	if ip := net.ParseIP(host); ip != nil {
		if isPrivateAddress(ip) {
			return fmt.Errorf("%w: address %s is not a public address", storage.ErrWebhookURLNotAllowed, host)
		}
		return nil
	}
	addresses, err := net.LookupIP(host)
	if err != nil {
		return fmt.Errorf("%w: host %q does not resolve", storage.ErrWebhookURLNotAllowed, host)
	}
	if len(addresses) == 0 {
		return fmt.Errorf("%w: host %q does not resolve", storage.ErrWebhookURLNotAllowed, host)
	}
	for _, address := range addresses {
		if isPrivateAddress(address) {
			return fmt.Errorf("%w: host %q resolves to a non-public address", storage.ErrWebhookURLNotAllowed, host)
		}
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
