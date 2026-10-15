package storage

import "context"

// LiveMemoryIDs reports which of the given identifiers still exist as memory
// units of the bank.
func (s *MemoryStore) LiveMemoryIDs(ctx context.Context, bankID string, ids []string) (map[string]bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	live := make(map[string]bool, len(ids))
	for _, id := range ids {
		unit, ok := s.units[id]
		if ok && unit.BankID == bankID {
			live[id] = true
		}
	}
	return live, nil
}
