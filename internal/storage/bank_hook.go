package storage

import (
	"context"
	"log/slog"
)

// BankCreateHook runs once after a bank row is newly created, so a
// process-level provisioning step — the configured default bank template — can
// reach every freshly-created bank from one place instead of at each of the
// many lazy-create call sites.
//
// It is best-effort: the backend logs a failure and still reports the
// creation, because a bank that exists with a partly-applied template is
// better than a write that fails on a server-level misconfiguration. A nil
// hook leaves creation unchanged.
type BankCreateHook func(ctx context.Context, bankID string) error

// SetBankCreateHook installs the freshly-created-bank hook. The composition
// root calls it once at startup; a store built without it invokes nothing.
func (s *MemoryStore) SetBankCreateHook(hook BankCreateHook) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bankCreateHook = hook
}

// SetBankCreateHook installs the freshly-created-bank hook. The composition
// root calls it once at startup; a store built without it invokes nothing.
func (s *PostgresStore) SetBankCreateHook(hook BankCreateHook) {
	s.bankCreateHook = hook
}

// bankCreateHookRef reads the hook the store was built with.
func (s *PostgresStore) bankCreateHookRef() BankCreateHook { return s.bankCreateHook }

// runBankCreateHook invokes a freshly-created-bank hook best-effort. A failed
// provisioning step is logged, not returned: the bank exists, and a
// server-level misconfiguration must not fail the write that created it.
func runBankCreateHook(ctx context.Context, hook BankCreateHook, bankID string) {
	if hook == nil {
		return
	}
	if err := hook(ctx, bankID); err != nil {
		slog.Default().Error("bank creation hook failed", "bank_id", bankID, "error", err)
	}
}
