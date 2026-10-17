// Package webhooks serves the webhook route family, including the guard that
// keeps a registered URL from reaching the service's own network.
package webhooks

import (
	"fmt"
	"net"
	"net/url"
	"strconv"
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
	// Global is the deployment-level webhook target delivered alongside every
	// bank's own registrations. Nil (or an empty URL) means the feature is off.
	// It rides with the guard so the one policy object both the registration
	// route and the delivery worker already share also carries the deployment's
	// own receiver and its response-body setting.
	Global *GlobalTarget
}

// Guard validates a webhook target URL.
//
// A registered URL is fetched by the server, so an unchecked one is a
// server-side request forgery: it could reach the metadata service, an internal
// admin API, or a loopback port that is not otherwise exposed. The guard
// therefore refuses anything but an absolute HTTP(S) URL whose host resolves
// only to public addresses.
type Guard struct{ cfg GuardConfig }

// NewGuard builds a guard from the configuration.
func NewGuard(cfg GuardConfig) *Guard { return &Guard{cfg: cfg} }

// Global returns the deployment-level webhook target the guard carries, or nil
// when none is configured. It is nil-safe, so a caller never needs to check the
// guard itself before asking.
func (g *Guard) Global() *GlobalTarget {
	if g == nil {
		return nil
	}
	return g.cfg.Global
}

// URLNotAllowed reports a webhook destination the guard refuses. It carries the
// reference's own detail text — the message the API returns verbatim — while
// still matching the storage sentinel so delivery-path callers can branch on it.
type URLNotAllowed struct{ msg string }

func (e *URLNotAllowed) Error() string { return e.msg }

// Is reports the storage sentinel as a match without changing the visible text.
func (e *URLNotAllowed) Is(target error) bool { return target == storage.ErrWebhookURLNotAllowed }

func refuse(format string, args ...any) error {
	return &URLNotAllowed{msg: fmt.Sprintf(format, args...)}
}

// Validate reports whether a URL may be stored as a webhook target.
func (g *Guard) Validate(raw string) error {
	parsed, err := g.parseTarget(raw)
	if err != nil {
		return err
	}
	return g.validateHostLiteral(parsed.Hostname())
}

// parseTarget checks the URL's structure — no control characters, a valid port,
// an HTTP(S) scheme and a host — matching the reference's syntactic check.
func (g *Guard) parseTarget(raw string) (*url.URL, error) {
	for _, r := range raw {
		if r < 0x20 || r == 0x7F {
			return nil, refuse("Invalid webhook URL: contains non-printable characters")
		}
	}
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		if strings.Contains(err.Error(), "port") {
			return nil, refuse("Invalid webhook URL: Invalid URL: port can't be converted to integer")
		}
		return nil, refuse("Invalid webhook URL: %s", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		scheme := parsed.Scheme
		if scheme == "" {
			scheme = "empty"
		}
		return nil, refuse("Webhook URL scheme must be http or https, got '%s'", scheme)
	}
	if parsed.Host == "" || parsed.Hostname() == "" {
		return nil, refuse("Webhook URL must include a host")
	}
	if port := parsed.Port(); port != "" {
		number, convErr := strconv.Atoi(port)
		if convErr != nil {
			return nil, refuse("Invalid webhook URL: Invalid URL: port can't be converted to integer")
		}
		if number < 0 || number > 65535 {
			return nil, refuse("Invalid webhook URL: Port out of range 0-65535")
		}
	}
	return parsed, nil
}

// validateHostLiteral rejects an IP-literal host that points at a blocked range.
// A host that is a DNS name is accepted here and vetted when it is resolved,
// which keeps the request path off the network and is where a rebind is caught.
func (g *Guard) validateHostLiteral(host string) error {
	if g.hostAllowed(host) {
		return nil
	}
	if g.cfg.AllowPrivateHosts {
		return nil
	}
	literal := ipLiteral(host)
	if literal == nil {
		// The legacy numeric spellings the OS resolver folds onto an address
		// (127.1, 2130706433, 0x7f.1) are caught here so they cannot smuggle a
		// loopback or private target past the check.
		literal = legacyIPv4(host)
	}
	if literal == nil {
		return nil
	}
	if g.addressAllowed(literal) {
		return nil
	}
	if ipIsBlocked(literal) {
		return refuse("Webhook URL host '%s' targets a private/loopback/link-local address, "+
			"which is not an allowed destination", host)
	}
	return nil
}

// ipLiteral parses a host that is a literal IP address, or nil for a DNS name.
func ipLiteral(host string) net.IP {
	return net.ParseIP(strings.Trim(host, "[]"))
}

// legacyIPv4 folds the numeric IPv4 spellings socket.inet_aton accepts but
// net.ParseIP does not onto their 32-bit address, or nil when the host is not
// one of them (a DNS name, or a value out of range).
func legacyIPv4(host string) net.IP {
	parts := strings.Split(host, ".")
	if len(parts) == 0 || len(parts) > 4 {
		return nil
	}
	values := make([]uint64, 0, len(parts))
	for _, part := range parts {
		if part == "" {
			return nil
		}
		value, ok := parseAtoui(part)
		if !ok {
			return nil
		}
		values = append(values, value)
	}
	var address uint64
	switch len(values) {
	case 1:
		if values[0] > 0xFFFFFFFF {
			return nil
		}
		address = values[0]
	case 2:
		if values[0] > 0xFF || values[1] > 0xFFFFFF {
			return nil
		}
		address = values[0]<<24 | values[1]
	case 3:
		if values[0] > 0xFF || values[1] > 0xFF || values[2] > 0xFFFF {
			return nil
		}
		address = values[0]<<24 | values[1]<<16 | values[2]
	default:
		for _, value := range values {
			if value > 0xFF {
				return nil
			}
		}
		address = values[0]<<24 | values[1]<<16 | values[2]<<8 | values[3]
	}
	return net.IPv4(byte(address>>24), byte(address>>16), byte(address>>8), byte(address))
}

// parseAtoui reads one inet_aton component: hexadecimal with a 0x prefix,
// octal with a leading zero, or decimal.
func parseAtoui(value string) (uint64, bool) {
	base := 10
	switch {
	case strings.HasPrefix(value, "0x") || strings.HasPrefix(value, "0X"):
		base = 16
		value = value[2:]
		if value == "" {
			return 0, false
		}
	case len(value) > 1 && value[0] == '0':
		base = 8
	}
	parsed, err := strconv.ParseUint(value, base, 64)
	if err != nil {
		return 0, false
	}
	return parsed, true
}

// reservedNetworks are the ranges Python's ipaddress flags as reserved but Go
// does not classify: the special-purpose address blocks that are not otherwise
// private, loopback, link-local or multicast.
var reservedNetworks = parseNetworks(
	"0.0.0.0/8", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15",
	"198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "255.255.255.255/32",
	"100::/64", "2001:db8::/32",
)

// carrierGradeNAT is RFC 6598 shared address space, which Python only counts as
// private from 3.13 on; it is listed explicitly so the block is version-stable.
var carrierGradeNAT = parseNetworks("100.64.0.0/10")

func parseNetworks(cidrs ...string) []*net.IPNet {
	out := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		if _, network, err := net.ParseCIDR(cidr); err == nil {
			out = append(out, network)
		}
	}
	return out
}

// ipIsBlocked reports whether an address is in a range unsafe for a
// caller-supplied fetch: loopback, link-local, private, reserved, multicast, or
// unspecified, plus the carrier-grade NAT block.
func ipIsBlocked(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// An IPv4-mapped IPv6 address (::ffff:127.0.0.1) is judged on the embedded
	// v4 address so the loopback and private checks see it.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() || ip.IsMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsPrivate() {
		return true
	}
	for _, network := range carrierGradeNAT {
		if network.Contains(ip) {
			return true
		}
	}
	for _, network := range reservedNetworks {
		if network.Contains(ip) {
			return true
		}
	}
	return false
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
		return &URLNotAllowed{msg: fmt.Sprintf("%s: %q is not an address", storage.ErrWebhookURLNotAllowed, address)}
	}
	if g.cfg.AllowPrivateHosts || g.addressAllowed(ip) {
		return nil
	}
	if ipIsBlocked(ip) {
		return &URLNotAllowed{msg: fmt.Sprintf("%s: address %s is not a public address", storage.ErrWebhookURLNotAllowed, ip)}
	}
	return nil
}

// ErrNotAllowed re-exports the storage sentinel so a caller can compare with
// errors.Is against either name.
var ErrNotAllowed = storage.ErrWebhookURLNotAllowed
