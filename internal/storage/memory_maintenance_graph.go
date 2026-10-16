package storage

import "context"

// The in-process store serves the graph-maintenance job, with empty queues.
var _ GraphMaintainer = (*MemoryStore)(nil)

// DrainGraphMaintenance claims queued graph-relink targets. The in-process
// store does not maintain a relink queue, so it has nothing to drain.
func (s *MemoryStore) DrainGraphMaintenance(context.Context, int) ([]MaintenanceTarget, error) {
	return nil, nil
}

// RelinkUnit recomputes a unit's semantic links from its embedding.
func (s *MemoryStore) RelinkUnit(ctx context.Context, bankID, unitID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.links = removeSemanticLinksFor(s.links, unitID)
	unit, ok := s.units[unitID]
	if !ok || unit.BankID != bankID || len(unit.Embedding) == 0 {
		return nil
	}
	candidates := []MemoryUnit{unit}
	added := 0
	for _, other := range s.units {
		if added >= semanticNeighbourWindow {
			break
		}
		if other.ID == unitID || other.BankID != bankID || len(other.Embedding) == 0 {
			continue
		}
		candidates = append(candidates, other)
		added++
	}
	s.links = append(s.links, semanticLinksFor(bankID, candidates, s.tuning.SemanticLinkMinSimilarity)...)
	return nil
}

// DrainEntityMaintenance claims queued entity-prune targets. The in-process
// store does not maintain a prune queue.
func (s *MemoryStore) DrainEntityMaintenance(context.Context, int) ([]MaintenanceTarget, error) {
	return nil, nil
}

// RequeueGraphMaintenance returns relink targets to the queue. The in-process
// store has no queue to return them to.
func (s *MemoryStore) RequeueGraphMaintenance(context.Context, []MaintenanceTarget) error {
	return nil
}

// RequeueEntityMaintenance returns entity-prune targets to the queue. The
// in-process store has no queue to return them to.
func (s *MemoryStore) RequeueEntityMaintenance(context.Context, []MaintenanceTarget) error {
	return nil
}

// PruneEntity removes an entity no unit mentions any more.
func (s *MemoryStore) PruneEntity(ctx context.Context, bankID, entityID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, entityIDs := range s.unitEntities {
		if containsString(entityIDs, entityID) {
			return nil
		}
	}
	if entity, ok := s.entities[entityID]; ok && entity.BankID == bankID {
		delete(s.entities, entityID)
	}
	return nil
}

// removeSemanticLinksFor drops the semantic links touching one unit, so a
// relink can rebuild them.
func removeSemanticLinksFor(links []MemoryLink, unitID string) []MemoryLink {
	kept := links[:0]
	for _, link := range links {
		if link.LinkType == LinkSemantic && (link.FromUnitID == unitID || link.ToUnitID == unitID) {
			continue
		}
		kept = append(kept, link)
	}
	return kept
}
