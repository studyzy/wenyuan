package httpx

import (
	"context"
	"net/http"
	"strings"
)

// This file carries the deployment-facing tag-scope surface: the caller's
// credential (which a validator reads to resolve a scope), the optional write
// scope a caller is confined to, and the native implementation of the
// reference's system-test `UserTagScope` validator.

// credentialContextKey keys the caller's credential on the request context, so
// a validator that resolves a tag scope from the API key can read it without
// the middleware having to hand it through every call.
type credentialContextKey struct{}

// WithCredential records the caller's credential on the context.
func WithCredential(ctx context.Context, credential string) context.Context {
	return contextWithValue(ctx, credentialContextKey{}, credential)
}

// CredentialFrom returns the caller's credential, or "" when the request
// carried none.
func CredentialFrom(ctx context.Context) string {
	credential, _ := ctx.Value(credentialContextKey{}).(string)
	return credential
}

// TagWriteScopeResolver is the optional capability a validator implements when a
// deployment confines the tags a caller may WRITE, separately from what it may
// read: a caller may read a shared scope it cannot change. It reports the tags
// the caller may write and whether it is confined at all; nil and false mean
// unrestricted, the unextended behaviour.
type TagWriteScopeResolver interface {
	ResolveWriteTagScope(ctx context.Context, bankID string) (tags []string, confined bool)
}

// writeTagScope records the caller's resolved write scope and whether one
// applies.
type writeTagScope struct {
	tags     []string
	confined bool
}

// writeTagScopeKey keys the caller's resolved write scope on the request.
type writeTagScopeKey struct{}

// WithWriteTagScope records the write scope the caller is confined to.
func WithWriteTagScope(r *http.Request, tags []string) *http.Request {
	return r.WithContext(contextWithValue(r.Context(), writeTagScopeKey{}, writeTagScope{tags: tags, confined: true}))
}

// WriteTagScopeFrom returns the caller's resolved write scope and whether one
// confines them. With no scope the caller may write anything.
func WriteTagScopeFrom(r *http.Request) ([]string, bool) {
	scope, _ := r.Context().Value(writeTagScopeKey{}).(writeTagScope)
	return scope.tags, scope.confined
}

// TagsWritable reports whether every tag in tags is one the caller may write,
// given the write scope. A nil scope (not confined) allows every tag. A tag is
// writable when it equals one of the scope tags; the wildcard "key:*" and "*"
// match a whole namespace. An untagged item belongs to everyone, so a confined
// writer may not produce or change one (the reference's tags_writable,
// engine/search/tags.py:579-588: `bool(tags) and all(...)`).
func TagsWritable(tags, scope []string, confined bool) bool {
	if !confined {
		return true
	}
	if len(tags) == 0 {
		return false
	}
	for _, tag := range tags {
		if !tagWritable(tag, scope) {
			return false
		}
	}
	return true
}

// tagWritable reports whether one tag is inside the write scope.
func tagWritable(tag string, scope []string) bool {
	for _, allowed := range scope {
		if allowed == tag || allowed == "*" {
			return true
		}
		if strings.HasSuffix(allowed, ":*") {
			prefix := strings.TrimSuffix(allowed, "*")
			if strings.HasPrefix(tag, prefix) {
				return true
			}
		}
	}
	return false
}

// UserTagScopeValidator is the native implementation of the reference system-test
// extension `UserTagScope`. It confines each API key to its own `user:<key>` tag
// plus the shared `kind:rule` scope for reads, and to `user:<key>` for writes —
// except `kate`, who sets the team's rules and may also write `kind:rule`. A
// request without an API key, or with a `bank:<id>` key, is the bank's
// administrator and carries no tag scope.
type UserTagScopeValidator struct{}

// ValidateOperation accepts every operation: this validator carries tag and bank
// rules, not an operation allow-list.
func (UserTagScopeValidator) ValidateOperation(context.Context, OperationRequest) OperationDecision {
	return OperationDecision{Allowed: true}
}

// ResolveTagScope confines an ordinary key to `user:<key>` OR `kind:rule`.
func (UserTagScopeValidator) ResolveTagScope(ctx context.Context, _ string) []TagGroup {
	user := tagScopeUser(CredentialFrom(ctx))
	if user == "" {
		return nil
	}
	return []TagGroup{{Tags: []string{"user:" + user, "kind:rule"}, Match: "any_strict"}}
}

// ResolveWriteTagScope confines an ordinary key to `user:<key>`, and kate to
// `user:kate` plus `kind:rule`.
func (UserTagScopeValidator) ResolveWriteTagScope(ctx context.Context, _ string) ([]string, bool) {
	user := tagScopeUser(CredentialFrom(ctx))
	if user == "" {
		return nil, false
	}
	if user == "kate" {
		return []string{"user:kate", "kind:rule"}, true
	}
	return []string{"user:" + user}, true
}

// ValidateBankWrite refuses a bank-scoped key that names another bank, which is
// the reference's validate_bank_write rule.
func (UserTagScopeValidator) ValidateBankWrite(_ context.Context, bankID, apiKey string) OperationDecision {
	if apiKey == "" || !strings.HasPrefix(apiKey, BankKeyPrefix) {
		return OperationDecision{Allowed: true}
	}
	if apiKey == BankKeyPrefix+bankID {
		return OperationDecision{Allowed: true}
	}
	return OperationDecision{
		Allowed: false,
		Reason:  "key may not write to bank " + bankID,
		Status:  KindForbidden,
	}
}

// importQuotaMarker is the test deployment's quota marker: an archive holding
// a document that mentions it is refused at submission, the way a metering
// deployment refuses work it cannot pay for. The native UserTagScope mirrors
// the reference suite's extension, which is the only deployment that names it.
const importQuotaMarker = "OVER-QUOTA"

// ValidateImport is the import gate the same validator carries: an archive is
// refused before anything is written when any of its documents mentions the
// quota marker. It answers the reference extension's status and wording.
func (UserTagScopeValidator) ValidateImport(_ context.Context, _ string, documents []ImportDocument) OperationDecision {
	for _, document := range documents {
		if strings.Contains(document.Content, importQuotaMarker) {
			return OperationDecision{
				Allowed: false,
				Reason:  "import exceeds the quota",
				Status:  http.StatusPaymentRequired,
			}
		}
	}
	return OperationDecision{Allowed: true}
}

// tagScopeUser maps an API key to the user a tag scope applies to; a bank-scoped
// key and a keyless admin call get none.
func tagScopeUser(apiKey string) string {
	if apiKey == "" || strings.HasPrefix(apiKey, BankKeyPrefix) {
		return ""
	}
	return apiKey
}

// bankScopedBank recovers the bank a bank-scoped request addresses, whatever the
// method or sub-path, so the middleware can resolve a tag scope for every read
// whose handler might consume one.
func bankScopedBank(r *http.Request) (string, bool) {
	const prefix = PrefixBanksV1 + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return "", false
	}
	bankID, _, _ := strings.Cut(r.URL.Path[len(prefix):], "/")
	if bankID == "" {
		return "", false
	}
	return bankID, true
}

// RefuseBankWideIfScoped writes the reference's refusal for a caller confined to
// a tag scope attempting an operation that reaches the whole bank.
//
// Export, clone, import, bank-wide clears, deleting the bank, changing its
// config or aliases, and reads answered for the whole bank (statistics, the
// operations list, webhooks, the audit and LLM-request logs) reach every memory
// regardless of tags, so they cannot be narrowed to a scope and a confined
// caller may not run them at all. It reports whether it wrote a refusal; a
// request with neither a read nor a write scope is untouched, which is every
// request in a deployment with no scope validator.
func RefuseBankWideIfScoped(w http.ResponseWriter, r *http.Request, what string) bool {
	if len(TagScopeFrom(r)) == 0 {
		if _, confined := WriteTagScopeFrom(r); !confined {
			return false
		}
	}
	WriteErrorFor(w, r, NewHTTPError(KindForbidden, "A tag-scoped caller can't "+what))
	return true
}
