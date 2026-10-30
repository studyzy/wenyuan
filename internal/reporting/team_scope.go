package reporting

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TeamScopeResolver decides whether a bank-scoped read falls back to a team bank.
//
// It mirrors the memory-operation handler set's interface rather than sharing one:
// the two packages do not import each other, and the contract is one method.
type TeamScopeResolver interface {
	// Resolve returns the team bank to read instead, or nil to stay put.
	Resolve(ctx context.Context, callerTenantID, bankID string) (*storage.FallbackScope, error)
}

// teamFallback resolves the fallback scope for one bank-scoped listing.
//
// A resolver failure degrades the read to its previous behavior rather than
// failing it: the personal bank answers exactly as it did before teams existed.
func (h *Handlers) teamFallback(ctx context.Context, bankID string) *storage.FallbackScope {
	if h.TeamScope == nil {
		return nil
	}
	caller, ok := storage.SchemaFrom(ctx)
	if !ok {
		return nil
	}
	scope, err := h.TeamScope.Resolve(ctx, caller, bankID)
	if err != nil {
		return nil
	}
	return scope
}
