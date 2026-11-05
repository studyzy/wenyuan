package httpx

import (
	"net/http"
	"net/url"
	"strings"
)

// agentBankHarness is the tenant name a Hindsight-compatible coding-agent
// client writes into its bank ids. The client calls its own account
// "coding-agent" by default and addresses a bank as "<account>::<project>", so
// the prefix names the harness, not a tenant of this service. It is the one
// foreign prefix the rewrite re-homes rather than refuses.
const agentBankHarness = "coding-agent"

// agentBankPathPrefixes are the paths whose next segment is a bank identifier:
// the bank-scoped REST routes and the single-bank MCP mount.
//
// The REST entry is spelled with the version and the namespace segment the
// reference serves its bank routes under (/v1/default/banks/). A prefix without
// them matches no served route, so the rewrite would never fire and the harness
// id would reach the store unchanged.
var agentBankPathPrefixes = []string{BankRoutesPrefix, NamespaceMCP + "/"}

// tenantBankPrefix is the separator a bank id carries between the tenant that
// owns the bank and the bank's own name.
const tenantBankPrefix = "::"

// bankRehomeKind is what a bank-scoped request's id turned out to be. Only
// bankRehomeRehost changes the request, and bankRehomeUnverifiable may: the
// other answers are the reference's, unchanged.
type bankRehomeKind int

const (
	// bankRehomeUnscoped is a request that addresses no bank segment: the
	// collection, a path outside the bank routes.
	bankRehomeUnscoped bankRehomeKind = iota
	// bankRehomeCanonical is an id that already names the caller's tenant, or
	// one that names no bank at all. It is served as sent.
	bankRehomeCanonical
	// bankRehomeRehost is an id that names a prefix which is not a tenant —
	// "coding-agent::<project>" or a bare "xyz" — and moves under the
	// caller's tenant.
	bankRehomeRehost
	// bankRehomeUnverifiable is an id carrying a prefix that rests on the
	// registry's answer, which this middleware cannot consult: the legacy
	// lifecycle allowance decides, and otherwise the id is reported missing.
	bankRehomeUnverifiable
)

// bankRehomePlan is the decision the tenant middleware makes about the bank id
// a request addresses, and the id it is about.
type bankRehomePlan struct {
	kind bankRehomeKind
	// bankID is the id the request addresses as sent, for the kinds the
	// middleware reports on; the rehost answer carries none, because the
	// rewrite replaces the whole id.
	bankID string
}

// planBankRehoming decides where a bank-scoped request lands.
//
// The reference's bank ids name no tenant at all; here a bank lives in exactly
// one tenant's schema and its id has to name it. A client knows its API key and
// nothing else — the tenant is this service's bookkeeping — so the bank id it
// sends is a name of the caller's own and moves under the caller's tenant:
// "coding-agent::<project>" and a bare "xyz" alike.
//
// Three answers are served as sent. "<caller>::<name>" is the canonical form a
// console, a migration or a client that addresses banks canonically sends, and
// rewriting it would be a no-op at best. "<anything>::" names no bank. The
// harness's own word on its own ("coding-agent" without a project) is the
// client's account name, not a bank id, and "<tenant>::" would name no bank
// either.
//
// What remains is an id whose prefix is neither the caller's nor the harness's
// — most likely another tenant's bank, though a client may also just have made
// a prefix up. The prefix alone cannot tell, and the registry is not reachable
// here, so the id is reported unverifiable and the middleware answers it the
// way it answered every foreign-prefixed id before: the legacy lifecycle
// allowance, else missing.
func planBankRehoming(r *http.Request, tenantID string) bankRehomePlan {
	if tenantID == "" {
		return bankRehomePlan{kind: bankRehomeUnscoped}
	}
	raw, ok := bankSegmentOf(r.URL)
	if !ok || raw == "" {
		return bankRehomePlan{kind: bankRehomeUnscoped}
	}
	// The decision is made on the decoded spelling: an escaped separator is
	// the same bank the literal one names.
	segment, err := url.PathUnescape(raw)
	if err != nil {
		segment = raw
	}
	prefix, name, separated := strings.Cut(segment, tenantBankPrefix)
	switch {
	case !separated:
		// A bare name is the caller's own bank and moves under its tenant;
		// the harness's account name on its own is not a bank id at all.
		if segment == agentBankHarness {
			return bankRehomePlan{kind: bankRehomeCanonical}
		}
		return bankRehomePlan{kind: bankRehomeRehost}
	case name == "":
		// "<anything>::" names no bank; it is served as sent, as before.
		return bankRehomePlan{kind: bankRehomeCanonical}
	case prefix == tenantID:
		return bankRehomePlan{kind: bankRehomeCanonical}
	case prefix == agentBankHarness:
		return bankRehomePlan{kind: bankRehomeRehost}
	}
	return bankRehomePlan{kind: bankRehomeUnverifiable, bankID: segment}
}

// rewriteBankInURL replaces the bank id a bank-scoped URL addresses with the
// caller's tenant's form of it ("<tenant>::<name>"). It reports whether the URL
// changed.
//
// The rewrite happens on the URL rather than on the matched path value because
// it runs before routing: the mux then captures the canonical id and every
// consumer below it — the handler, the audit trail, the bank-write gate — sees
// the bank the request really addresses. The escaped spelling is not carried
// over: the canonical id is written decoded into Path and RawPath is dropped,
// so EscapedPath re-derives one from Path and the mux matches the canonical
// form. A percent-escaped separator is the same bank either way — the decoded
// segment is what decides and what is captured.
func rewriteBankInURL(u *url.URL, tenantID string) bool {
	if tenantID == "" {
		return false
	}
	start, ok := agentBankPathStart(u.Path)
	if !ok {
		return false
	}
	// A bank id is the whole segment after the prefix, so the separator search
	// stops at the next "/": a sub-resource is not part of the id. The id's
	// separator is consumed by the cut and goes back on with the tail.
	segment, tail, found := strings.Cut(u.Path[start:], "/")
	name, rehome := rehomedBankName(segment)
	if !rehome {
		return false
	}
	if found {
		tail = "/" + tail
	}
	u.Path = u.Path[:start] + tenantID + tenantBankPrefix + name + tail
	u.RawPath = ""
	return true
}

// rehomedBankName answers the bank's own name — what follows the separator —
// for a segment that has a name to re-home. A segment with no separator is its
// own name; "::<name>" and "<prefix>::" have no bank name and answer false.
func rehomedBankName(segment string) (string, bool) {
	prefix, name, separated := strings.Cut(segment, tenantBankPrefix)
	if !separated {
		return segment, true
	}
	if prefix == "" || name == "" {
		return "", false
	}
	return name, true
}

// bankSegmentOf answers the bank-id segment exactly as the client wrote it,
// preferring RawPath when the request carried one: the separator may be
// percent-escaped, and the escaped spelling is the same bank.
func bankSegmentOf(u *url.URL) (string, bool) {
	if u.RawPath != "" {
		if start, ok := agentBankPathStart(u.RawPath); ok {
			segment, _, _ := strings.Cut(u.RawPath[start:], "/")
			return segment, true
		}
	}
	start, ok := agentBankPathStart(u.Path)
	if !ok {
		return "", false
	}
	segment, _, _ := strings.Cut(u.Path[start:], "/")
	return segment, true
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
