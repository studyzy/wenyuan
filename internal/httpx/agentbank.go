package httpx

import (
	"net/url"
	"strings"
)

// agentBankHarness is the tenant name a Hindsight-compatible coding-agent
// client writes into its bank ids. The client calls its own account
// "coding-agent" by default and addresses a bank as "<account>::<project>", so
// the prefix names the harness, not a tenant of this service.
const agentBankHarness = "coding-agent"

// agentBankPrefix is the full prefix such a client puts in front of a bank id.
const agentBankPrefix = agentBankHarness + "::"

// agentBankPathPrefixes are the namespaces whose first path segment is a bank
// identifier: the bank-scoped REST routes and the single-bank MCP mount.
var agentBankPathPrefixes = []string{PrefixBanksV1 + "/", NamespaceMCP + "/"}

// rewriteAgentBankPrefix replaces the coding-agent harness prefix of the bank
// id a bank-scoped path addresses with the tenant the request's credential
// resolved to: "coding-agent::<project>" becomes "<tenant>::<project>". It
// reports whether the path changed.
//
// This is what lets a Hindsight-compatible coding-agent client keep using the
// bank id it was configured with. In a multi-tenant deployment a bank lives in
// exactly one tenant's schema and its id names that tenant, so the client's
// harness name names no bank here; the credential has just answered which
// tenant the caller is, and the project name after the prefix is the part the
// caller is actually choosing.
//
// The rewrite happens on the URL rather than on the matched path value because
// it runs before routing: the mux then captures the canonical id and every
// consumer below it — the handler, the audit trail, the bank-write gate — sees
// the bank the request really addresses. A single-tenant deployment has no
// tenant extension and never calls this, so its bank ids stay exactly the ones
// the client sent.
func rewriteAgentBankPrefix(u *url.URL, tenantID string) bool {
	if tenantID == "" {
		return false
	}
	start, ok := agentBankPathStart(u.Path)
	if !ok || !strings.HasPrefix(u.Path[start:], agentBankPrefix) {
		return false
	}
	u.Path = u.Path[:start] + tenantID + "::" + u.Path[start+len(agentBankPrefix):]
	// A raw path, when the request carried one, is what the mux matches on, so
	// it has to move with Path. The harness name and a tenant id are both
	// unescaped ASCII, which is what lets the name be replaced in place; the
	// "::" that follows may be escaped in either spelling and is left alone.
	// A RawPath this does not apply to is no longer a valid encoding of Path,
	// and EscapedPath falls back to escaping Path itself.
	if u.RawPath != "" {
		if rawStart, ok := agentBankPathStart(u.RawPath); ok && strings.HasPrefix(u.RawPath[rawStart:], agentBankHarness) {
			u.RawPath = u.RawPath[:rawStart] + tenantID + u.RawPath[rawStart+len(agentBankHarness):]
		}
	}
	return true
}

// agentBankPathStart returns the offset at which the bank-id segment of a
// bank-scoped path begins.
func agentBankPathStart(path string) (int, bool) {
	for _, prefix := range agentBankPathPrefixes {
		if strings.HasPrefix(path, prefix) {
			return len(prefix), true
		}
	}
	return 0, false
}
