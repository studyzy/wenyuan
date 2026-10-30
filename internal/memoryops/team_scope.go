package memoryops

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TeamScopeResolver decides whether a bank-scoped read falls back to a team bank.
//
// It is an interface here rather than a concrete type so this package does not
// depend on the team registry, and so a handler set built without one keeps
// working: nil means "no fallback", which is exactly the single-tenant behavior.
type TeamScopeResolver interface {
	// Resolve returns the team bank to read instead, or nil to stay put.
	Resolve(ctx context.Context, callerTenantID, bankID string) (*storage.FallbackScope, error)
}

// teamFallback resolves the fallback scope for one bank-scoped read.
//
// A resolver failure is not a read failure: the personal bank answers exactly as
// it did before teams existed, so an unavailable registry degrades the read to its
// previous behavior instead of failing it.
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
