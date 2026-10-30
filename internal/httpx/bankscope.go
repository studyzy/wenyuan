package httpx

import (
	"context"
	"net/http"
	"strings"
)

// BankWriteGate is the optional capability a validator implements when a
// deployment confines API keys to particular banks. It is separate from
// OperationValidator because the question is different: that one gates an
// operation, this one gates a bank, and a deployment may answer one without the
// other.
type BankWriteGate interface {
	// ValidateBankWrite decides whether the calling credential may write to the
	// named bank.
	ValidateBankWrite(ctx context.Context, bankID, apiKey string) OperationDecision
}

// ImportDocument is one document of a transfer archive, as the import gate
// sees it: the archive's own document id and the text it was retained from.
// The reference lists exactly these two members for every document of the
// archive (engine/memory_engine.py:9182-9196).
type ImportDocument struct {
	DocumentID string
	Content    string
}

// ImportGate is the optional capability a validator implements when a
// deployment gates a transfer-archive import before it is queued: metering by
// document, a quota, a content policy. It is separate from OperationValidator
// because the question is decided per archive rather than per operation, and a
// deployment may answer it without carrying an operation allow-list.
//
// The gate runs at submission, with the archive parsed but nothing written, so
// a refusal leaves no bank, no stored archive and no queued operation
// (engine/memory_engine.py:9175-9196).
type ImportGate interface {
	// ValidateImport decides whether the named bank may import these documents.
	// The bank is the one the documents land in: the import's own bank, or the
	// restore's target.
	ValidateImport(ctx context.Context, bankID string, documents []ImportDocument) OperationDecision
}

// BankKeyPrefix marks a credential scoped to one bank, the reference's
// per-key isolation convention.
const BankKeyPrefix = "bank:"

// TagScopeKeyValidator implements the reference suite's tag-scope validator:
// tag-scoped reads for an ordinary key, and per-bank isolation for a
// "bank:<id>" key, which may write only its own bank and matches no tag scope.
//
// Only the bank rule is enforced here; the tag rules belong to the deployment's
// own validator. A keyless request is unrestricted, which is the bank's
// administrator.
type TagScopeKeyValidator struct{}

// ValidateOperation accepts every operation: the tag rules this validator
// carries are not part of the bank-write contract.
func (TagScopeKeyValidator) ValidateOperation(context.Context, OperationRequest) OperationDecision {
	return OperationDecision{Allowed: true}
}

// ResolveTagScope confines no caller: a bank-scoped key has no tag scope, and
// the tag rules are the deployment's own.
func (TagScopeKeyValidator) ResolveTagScope(context.Context, string) []TagGroup { return nil }

// ValidateBankWrite refuses a bank-scoped key that names another bank, which is
// the reference's validate_bank_write rule (issue #5137).
func (TagScopeKeyValidator) ValidateBankWrite(_ context.Context, bankID, apiKey string) OperationDecision {
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

// bankWritePath recovers the bank a bank-scoped write addresses. It reports
// false for a request that is not a bank-scoped write, so the gate is applied
// to exactly the operations the reference asks it about.
func bankWritePath(r *http.Request) (string, bool) {
	switch r.Method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
	default:
		return "", false
	}
	const prefix = PrefixBanksV1 + "/"
	if !strings.HasPrefix(r.URL.Path, prefix) {
		return "", false
	}
	rest := r.URL.Path[len(prefix):]
	bankID, _, _ := strings.Cut(rest, "/")
	if bankID == "" {
		return "", false
	}
	return bankID, true
}

// credentialFromRequest reads the caller's credential: the bearer token or the
// bare key, the two forms the reference accepts.
func credentialFromRequest(r *http.Request) string {
	if header := strings.TrimSpace(r.Header.Get("Authorization")); header != "" {
		const scheme = "Bearer "
		if len(header) >= len(scheme) && strings.EqualFold(header[:len(scheme)], scheme) {
			return strings.TrimSpace(header[len(scheme):])
		}
		return header
	}
	if key := strings.TrimSpace(r.Header.Get("X-API-Key")); key != "" {
		return key
	}
	return ""
}
