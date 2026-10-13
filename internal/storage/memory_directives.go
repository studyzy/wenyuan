package storage

import (
	"context"
	"sort"
	"time"
)

// ListDirectives returns the directives of one bank, newest first.
func (s *MemoryStore) ListDirectives(ctx context.Context, bankID string) ([]Directive, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Directive, 0)
	for _, directive := range s.directiveRecords {
		if directive.BankID != bankID {
			continue
		}
		out = append(out, cloneDirective(directive))
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.After(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetDirective returns one directive by identifier within a bank.
func (s *MemoryStore) GetDirective(ctx context.Context, bankID, id string) (Directive, error) {
	if err := ctx.Err(); err != nil {
		return Directive{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	directive, ok := s.directiveRecords[bankID+"\x00"+id]
	if !ok {
		return Directive{}, ErrDirectiveNotFound
	}
	return cloneDirective(directive), nil
}

// PutDirective inserts or replaces a directive.
func (s *MemoryStore) PutDirective(ctx context.Context, directive Directive) (Directive, error) {
	if err := ctx.Err(); err != nil {
		return Directive{}, err
	}
	if directive.ID == "" || directive.BankID == "" {
		return Directive{}, ErrInvalidOperationID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Directive{}, s.writeErr
	}
	if s.directiveRecords == nil {
		s.directiveRecords = map[string]Directive{}
	}
	now := time.Now().UTC()
	key := directive.BankID + "\x00" + directive.ID
	if existing, ok := s.directiveRecords[key]; ok {
		directive.CreatedAt = existing.CreatedAt
	}
	if directive.CreatedAt.IsZero() {
		directive.CreatedAt = now
	}
	directive.UpdatedAt = now
	s.directiveRecords[key] = cloneDirective(directive)
	return cloneDirective(directive), nil
}

// UpdateDirective applies a partial update and returns the stored value.
func (s *MemoryStore) UpdateDirective(ctx context.Context, bankID, id string, patch DirectivePatch) (Directive, error) {
	if err := ctx.Err(); err != nil {
		return Directive{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return Directive{}, s.writeErr
	}
	key := bankID + "\x00" + id
	directive, ok := s.directiveRecords[key]
	if !ok {
		return Directive{}, ErrDirectiveNotFound
	}
	if patch.Name != nil {
		directive.Name = *patch.Name
	}
	if patch.Content != nil {
		directive.Content = *patch.Content
	}
	if patch.Priority != nil {
		directive.Priority = *patch.Priority
	}
	if patch.IsActive != nil {
		directive.IsActive = *patch.IsActive
	}
	if patch.Tags != nil {
		directive.Tags = append([]string(nil), (*patch.Tags)...)
	}
	directive.UpdatedAt = time.Now().UTC()
	s.directiveRecords[key] = cloneDirective(directive)
	return cloneDirective(directive), nil
}

// DeleteDirective removes a directive.
func (s *MemoryStore) DeleteDirective(ctx context.Context, bankID, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	key := bankID + "\x00" + id
	if _, ok := s.directiveRecords[key]; !ok {
		return ErrDirectiveNotFound
	}
	delete(s.directiveRecords, key)
	return nil
}

// cloneDirective copies the mutable fields.
func cloneDirective(directive Directive) Directive {
	out := directive
	out.Tags = append([]string(nil), directive.Tags...)
	return out
}
