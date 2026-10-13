package storage

import (
	"context"
	"sort"
	"strings"
	"time"
)

// ListKnowledgeNodes returns every knowledge node of one bank, oldest first so
// a tree rebuilds in creation order.
func (s *MemoryStore) ListKnowledgeNodes(ctx context.Context, bankID string) ([]KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.readErr != nil {
		return nil, s.readErr
	}
	out := make([]KnowledgeNode, 0)
	for _, node := range s.knowledgeNodes {
		if node.BankID == bankID {
			out = append(out, cloneKnowledgeNode(node))
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].CreatedAt.Equal(out[j].CreatedAt) {
			return out[i].CreatedAt.Before(out[j].CreatedAt)
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

// GetKnowledgeNode returns one knowledge node by identifier.
func (s *MemoryStore) GetKnowledgeNode(ctx context.Context, bankID, nodeID string) (KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return KnowledgeNode{}, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	node, ok := s.knowledgeNodes[bankID+"\x00"+nodeID]
	if !ok {
		return KnowledgeNode{}, ErrKnowledgeNodeNotFound
	}
	return cloneKnowledgeNode(node), nil
}

// PutKnowledgeNode inserts or replaces a knowledge node.
func (s *MemoryStore) PutKnowledgeNode(ctx context.Context, node KnowledgeNode) (KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return KnowledgeNode{}, err
	}
	if node.ID == "" || node.BankID == "" {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	if node.Kind != KnowledgeKindFolder && node.Kind != KnowledgeKindPage {
		return KnowledgeNode{}, ErrInvalidOperationID
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return KnowledgeNode{}, s.writeErr
	}
	if s.knowledgeNodes == nil {
		s.knowledgeNodes = map[string]KnowledgeNode{}
	}
	key := node.BankID + "\x00" + node.ID
	now := time.Now().UTC()
	if existing, ok := s.knowledgeNodes[key]; ok {
		node.CreatedAt = existing.CreatedAt
	}
	if node.CreatedAt.IsZero() {
		node.CreatedAt = now
	}
	node.UpdatedAt = now
	s.knowledgeNodes[key] = cloneKnowledgeNode(node)
	return cloneKnowledgeNode(node), nil
}

// UpdateKnowledgeNode applies a partial update.
func (s *MemoryStore) UpdateKnowledgeNode(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch) (KnowledgeNode, error) {
	if err := ctx.Err(); err != nil {
		return KnowledgeNode{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return KnowledgeNode{}, s.writeErr
	}
	key := bankID + "\x00" + nodeID
	node, ok := s.knowledgeNodes[key]
	if !ok {
		return KnowledgeNode{}, ErrKnowledgeNodeNotFound
	}
	if patch.Title != nil {
		node.Title = *patch.Title
	}
	if patch.Text != nil {
		node.Text = *patch.Text
	}
	if patch.ParentID != nil {
		node.ParentID = *patch.ParentID
	}
	if patch.Tags != nil {
		node.Tags = append([]string(nil), (*patch.Tags)...)
	}
	node.UpdatedAt = time.Now().UTC()
	s.knowledgeNodes[key] = cloneKnowledgeNode(node)
	return cloneKnowledgeNode(node), nil
}

// DeleteKnowledgeNode removes a node and its descendants.
func (s *MemoryStore) DeleteKnowledgeNode(ctx context.Context, bankID, nodeID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	key := bankID + "\x00" + nodeID
	if _, ok := s.knowledgeNodes[key]; !ok {
		return ErrKnowledgeNodeNotFound
	}
	// A descendant whose parent is gone would be unreachable through the tree,
	// so it is removed with its ancestor.
	doomed := map[string]bool{nodeID: true}
	for changed := true; changed; {
		changed = false
		for _, node := range s.knowledgeNodes {
			if node.BankID != bankID || doomed[node.ID] {
				continue
			}
			if doomed[node.ParentID] {
				doomed[node.ID] = true
				changed = true
			}
		}
	}
	for id := range doomed {
		delete(s.knowledgeNodes, bankID+"\x00"+id)
	}
	return nil
}

// SearchKnowledgeNodes searches a bank's pages by title and text.
func (s *MemoryStore) SearchKnowledgeNodes(ctx context.Context, bankID, query string, limit int) ([]KnowledgeHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	term := strings.ToLower(strings.TrimSpace(query))
	hits := make([]KnowledgeHit, 0)
	for _, node := range s.knowledgeNodes {
		if node.BankID != bankID || node.Kind != KnowledgeKindPage {
			continue
		}
		score := knowledgeScore(node, term)
		if term != "" && score == 0 {
			continue
		}
		hits = append(hits, KnowledgeHit{Node: cloneKnowledgeNode(node), Score: score})
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].Score != hits[j].Score {
			return hits[i].Score > hits[j].Score
		}
		return hits[i].Node.ID < hits[j].Node.ID
	})
	if limit > 0 && len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// knowledgeScore ranks a page against a term: a title hit outranks a body hit.
func knowledgeScore(node KnowledgeNode, term string) float64 {
	if term == "" {
		return 1
	}
	if strings.Contains(strings.ToLower(node.Title), term) {
		return 1
	}
	if strings.Contains(strings.ToLower(node.Text), term) {
		return 0.5
	}
	return 0
}

// cloneKnowledgeNode copies the mutable fields.
func cloneKnowledgeNode(node KnowledgeNode) KnowledgeNode {
	out := node
	out.Tags = append([]string(nil), node.Tags...)
	return out
}
