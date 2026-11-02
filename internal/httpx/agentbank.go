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

// agentBankPathPrefixes are the paths whose next segment is a bank identifier:
// the bank-scoped REST routes and the single-bank MCP mount.
//
// The REST entry is spelled with the version and the namespace segment the
// reference serves its bank routes under (/v1/default/banks/). A prefix without
// them matches no served route, so the rewrite would never fire and the harness
// id would reach the store unchanged.
var agentBankPathPrefixes = []string{BankRoutesPrefix, NamespaceMCP + "/"}

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
	if !ok {
		return false
	}
	// A bank id is the whole segment after the prefix, so a client that sends the
	// harness name with no project at all — "coding-agent", no separator — names
	// the same non-tenant bank and is re-homed the same way. Anything longer is a
	// project name, and only the harness spelling of it is a bank of this
	// service's.
	segment := u.Path[start:]
	if !strings.HasPrefix(segment, agentBankHarness) {
		return false
	}
	rest := segment[len(agentBankHarness):]
	bare := rest == ""
	// A bank id is `<tenant>::<name>`, so the tenant always arrives with its
	// separator. A client that sent the harness name with no separator is naming
	// a bank with no project, which gets the same "::" and serves as the
	// tenant's empty-named bank rather than a bare id the contract has no
	// meaning for.
	replacement := tenantID
	if !bare && !strings.HasPrefix(rest, "::") {
		return false
	}
	if bare {
		rest = "::"
	}
	u.Path = u.Path[:start] + replacement + rest
	// A raw path, when the request carried one, is what the mux matches on, so
	// it has to move with Path. The harness name and a tenant id are both
	// unescaped ASCII, which is what lets the name be replaced in place; the
	// "::" that follows may be escaped in either spelling and is left alone.
	// A RawPath this does not apply to is no longer a valid encoding of Path,
	// and EscapedPath falls back to escaping Path itself.
	if u.RawPath != "" {
		if rawStart, ok := agentBankPathStart(u.RawPath); ok {
			rawSegment := u.RawPath[rawStart:]
			if strings.HasPrefix(rawSegment, agentBankHarness) && bareOrSeparated(rawSegment[len(agentBankHarness):]) {
				rawRest := rawSegment[len(agentBankHarness):]
				if rawRest == "" {
					rawRest = "::"
				}
				u.RawPath = u.RawPath[:rawStart] + replacement + rawRest
			}
		}
	}
	return true
}

// bareOrSeparated reports whether what follows the harness name in a path
// segment is the end of the segment or the "::" separator, in either spelling.
func bareOrSeparated(rest string) bool {
	return rest == "" || strings.HasPrefix(rest, "::") ||
		strings.HasPrefix(rest, "%3A%3A") || strings.HasPrefix(rest, "%3a%3a")
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
