package storage

import "context"

// BankConfig returns a bank's configuration overrides.
//
// The in-process store answers from the same map every reader sees, so there is
// nothing to bypass; the method exists because the interface is shared with the
// database backend, where a cached row would otherwise serve a configuration
// another server has already changed.
func (s *MemoryStore) BankConfig(ctx context.Context, bankID string) (map[string]any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	bank, ok := s.banks[bankID]
	if !ok {
		return nil, ErrBankNotFound
	}
	return copyAnyMap(bank.Config), nil
}
