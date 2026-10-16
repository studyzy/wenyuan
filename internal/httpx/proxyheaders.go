package httpx

import (
	"net"
	"net/http"
	"strings"
)

// Trusted proxies.
//
// A deployment behind a load balancer sees the balancer as its peer and the
// real client only in X-Forwarded-*. That header is caller-controlled, so it is
// honoured only when the immediate peer is one the operator trusts — the same
// rule, and the same allow-list syntax, as the reference's HTTP server.

// ProxyHeadersMiddleware rewrites a request's client information from the
// forwarded headers when the peer is trusted, and leaves it alone otherwise.
//
// It is only installed when the operator asked for it (--proxy-headers); a
// server with no proxy in front of it must not believe a header any caller can
// set.
func ProxyHeadersMiddleware(trusted []string) Middleware {
	if len(trusted) == 0 {
		// Nothing is trusted unless the operator said so: an unconfigured
		// server believes no header, which is the reference's default too.
		return func(next http.Handler) http.Handler { return next }
	}
	hosts := parseTrustedHosts(trusted)
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			peer := peerHost(r.RemoteAddr)
			if !hosts.trusts(peer) {
				next.ServeHTTP(w, r)
				return
			}
			if proto := strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")); isForwardedScheme(proto) {
				// The reference's server carries the scheme in its request
				// scope; Go spells the same fact as the request URL's scheme,
				// which is what URL building below this point reads.
				r.URL.Scheme = forwardedScheme(proto, websocketRequest(r))
			}
			if forwardedFor := r.Header.Get("X-Forwarded-For"); forwardedFor != "" {
				if client := hosts.clientHost(forwardedFor); client != "" {
					r.RemoteAddr = clientAddress(client)
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// websocketRequest reports whether the request is an upgrade, which the
// reference uses to translate a forwarded http scheme into a websocket one.
func websocketRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("Upgrade"), "websocket")
}

// clientAddress renders the originating client as the request's remote address.
// The header carries no port, and the reference reports the unknown one as 0; an
// entry that already names a port is kept as the proxy sent it.
func clientAddress(client string) string {
	if _, _, err := net.SplitHostPort(client); err == nil {
		return client
	}
	return net.JoinHostPort(client, "0")
}

// isForwardedScheme reports whether a proxy sent a scheme worth believing. The
// reference accepts exactly these four, and ignores anything else.
func isForwardedScheme(proto string) bool {
	switch strings.ToLower(proto) {
	case "http", "https", "ws", "wss":
		return true
	}
	return false
}

// forwardedScheme resolves the scheme a forwarded value names.
func forwardedScheme(proto string, websocket bool) string {
	proto = strings.ToLower(proto)
	if websocket {
		return strings.Replace(proto, "http", "ws", 1)
	}
	return proto
}

// peerHost reads the address a request arrived from, as the middleware compares
// it against the allow list.
func peerHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}

// trustedHosts is the resolved allow list: literal addresses, CIDR networks and
// arbitrary literals (a unix socket's peer, for instance), or everything.
type trustedHosts struct {
	always   bool
	ip       map[string]bool
	networks []*net.IPNet
	literals map[string]bool
}

// parseTrustedHosts resolves the allow-list entries the way the reference's
// server does: "*" trusts everything, an entry with a slash is a network, a
// parseable address is compared as an address, and anything else is compared
// literally.
func parseTrustedHosts(entries []string) trustedHosts {
	hosts := trustedHosts{ip: map[string]bool{}, literals: map[string]bool{}}
	if len(entries) == 1 && strings.TrimSpace(entries[0]) == "*" {
		hosts.always = true
		return hosts
	}
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if strings.Contains(entry, "/") {
			if _, network, err := net.ParseCIDR(entry); err == nil {
				hosts.networks = append(hosts.networks, network)
				continue
			}
			hosts.literals[entry] = true
			continue
		}
		if address := net.ParseIP(entry); address != nil {
			hosts.ip[address.String()] = true
			continue
		}
		hosts.literals[entry] = true
	}
	return hosts
}

// trusts reports whether the peer may have its forwarded headers believed.
func (h trustedHosts) trusts(host string) bool {
	if h.always {
		return true
	}
	if host == "" {
		return false
	}
	address := net.ParseIP(host)
	if address == nil {
		return h.literals[host]
	}
	if h.ip[address.String()] {
		return true
	}
	for _, network := range h.networks {
		if network.Contains(address) {
			return true
		}
	}
	return false
}

// clientHost reads the originating client out of X-Forwarded-For: the first
// entry that is not itself a trusted proxy, which is the address the caller's
// own proxy chain vouches for. When every entry is trusted the client was a
// proxy as well, and the left-most entry is the best available answer.
func (h trustedHosts) clientHost(forwardedFor string) string {
	entries := strings.Split(forwardedFor, ",")
	cleaned := make([]string, 0, len(entries))
	for _, entry := range entries {
		cleaned = append(cleaned, strings.TrimSpace(entry))
	}
	if h.always {
		if len(cleaned) == 0 {
			return ""
		}
		return cleaned[0]
	}
	for index := len(cleaned) - 1; index >= 0; index-- {
		if !h.trusts(peerHost(cleaned[index])) {
			return cleaned[index]
		}
	}
	if len(cleaned) == 0 {
		return ""
	}
	return cleaned[0]
}
