package storage

import (
	"context"
	"math"
	"sort"
)

// The in-process store serves the graph-maintenance job from its own queue.
var _ GraphMaintainer = (*MemoryStore)(nil)

// DrainGraphMaintenance claims queued graph-relink targets, removing them from
// the queue so a unit is relinked exactly once.
func (s *MemoryStore) DrainGraphMaintenance(ctx context.Context, limit int) ([]MaintenanceTarget, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.relinkQueue) < limit {
		limit = len(s.relinkQueue)
	}
	targets := append([]MaintenanceTarget(nil), s.relinkQueue[:limit]...)
	s.relinkQueue = s.relinkQueue[limit:]
	return targets, nil
}

// RequeueGraphMaintenance puts relink targets back on the queue after a failed
// drain, so the work is retried rather than lost with the claimed target. A
// target already queued is left alone.
func (s *MemoryStore) RequeueGraphMaintenance(ctx context.Context, targets []MaintenanceTarget) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(targets) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.enqueueRelinkTargetsLocked(targets)
	return nil
}

// RelinkUnit re-derives the derived links one unit lost, from the same sources
// retain derives them from: its temporal edges are topped up from the units
// whose dates fall inside the window, and its semantic edges from the vector
// neighbours above the similarity floor. It only ever adds an edge, mirroring
// the reference's relink pass: deleting one here would destroy the incoming
// derived edges of a unit that was never told to relink.
func (s *MemoryStore) RelinkUnit(ctx context.Context, bankID, unitID string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	unit, ok := s.units[unitID]
	if !ok || unit.BankID != bankID {
		return nil
	}
	s.topUpTemporalLinksLocked(bankID, unit)
	if len(unit.Embedding) > 0 {
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
		s.links = appendMissingLinks(s.links, semanticLinksFrom(bankID, unit, candidates, s.tuning.SemanticLinkMinSimilarity))
	}
	return nil
}

// topUpTemporalLinksLocked adds the temporal edges one unit is missing within
// the window, mirroring the reference's relink top-up: it only runs for dated
// experience/world units still below the per-unit cap, links each of them to
// the nearest same-kind units inside the 24h window, and never touches an edge
// that is already there.
func (s *MemoryStore) topUpTemporalLinksLocked(bankID string, unit MemoryUnit) {
	if unit.EventDate == nil || !temporalEligibleFactType(unit.FactType) {
		return
	}
	if countOutgoingLinks(s.links, unit.ID, LinkTemporal) >= maxTemporalLinksPerUnit {
		return
	}
	type neighbour struct {
		id    string
		hours float64
	}
	neighbours := make([]neighbour, 0, len(s.units))
	for id, other := range s.units {
		if id == unit.ID || other.BankID != bankID || other.FactType != unit.FactType || other.EventDate == nil {
			continue
		}
		neighbours = append(neighbours, neighbour{id: id, hours: math.Abs(other.EventDate.Sub(*unit.EventDate).Hours())})
	}
	sort.Slice(neighbours, func(a, b int) bool {
		if neighbours[a].hours != neighbours[b].hours {
			return neighbours[a].hours < neighbours[b].hours
		}
		return neighbours[a].id < neighbours[b].id
	})
	if len(neighbours) > maxTemporalLinksPerUnit {
		neighbours = neighbours[:maxTemporalLinksPerUnit]
	}
	additions := make([]MemoryLink, 0, len(neighbours))
	for _, near := range neighbours {
		if near.hours > temporalLinkWindowHours || hasDirectedLink(s.links, unit.ID, near.id, LinkTemporal) {
			continue
		}
		additions = append(additions, MemoryLink{
			BankID:     bankID,
			FromUnitID: unit.ID,
			ToUnitID:   near.id,
			LinkType:   LinkTemporal,
			Weight:     temporalLinkWeight(near.hours),
		})
	}
	s.links = appendMissingLinks(s.links, additions)
}

// enqueueRelinkVictimsLocked queues, for a later top-up, the survivors whose
// outgoing temporal or semantic links point at any of unitIDs — plus unitIDs
// themselves when includeAffected is set, since an edit leaves the edited unit
// live while stripping its own derived adjacency. It mirrors the reference's
// enqueue_relink_victims, including its ordering rule: it must run before the
// links are removed, because once they are gone nothing points back at the
// survivors.
func (s *MemoryStore) enqueueRelinkVictimsLocked(bankID string, unitIDs []string, includeAffected bool) {
	if len(unitIDs) == 0 {
		return
	}
	affected := make(map[string]bool, len(unitIDs))
	for _, id := range unitIDs {
		affected[id] = true
	}
	seen := make(map[string]bool, len(unitIDs))
	targets := make([]MaintenanceTarget, 0)
	for _, link := range s.links {
		if link.BankID != bankID || !affected[link.ToUnitID] {
			continue
		}
		if link.LinkType != LinkTemporal && link.LinkType != LinkSemantic {
			continue
		}
		if affected[link.FromUnitID] || seen[link.FromUnitID] {
			continue
		}
		seen[link.FromUnitID] = true
		targets = append(targets, MaintenanceTarget{BankID: bankID, ID: link.FromUnitID})
	}
	if includeAffected {
		for _, id := range unitIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			targets = append(targets, MaintenanceTarget{BankID: bankID, ID: id})
		}
	}
	s.enqueueRelinkTargetsLocked(targets)
}

// enqueueRelinkTargetsLocked appends targets the queue does not already hold,
// so a unit queued twice is still relinked once.
func (s *MemoryStore) enqueueRelinkTargetsLocked(targets []MaintenanceTarget) {
	queued := make(map[string]bool, len(s.relinkQueue))
	for _, target := range s.relinkQueue {
		queued[target.BankID+"\x00"+target.ID] = true
	}
	for _, target := range targets {
		key := target.BankID + "\x00" + target.ID
		if queued[key] {
			continue
		}
		queued[key] = true
		s.relinkQueue = append(s.relinkQueue, target)
	}
}

// DrainEntityMaintenance claims queued entity-prune targets. The in-process
// store does not maintain a prune queue.
func (s *MemoryStore) DrainEntityMaintenance(context.Context, int) ([]MaintenanceTarget, error) {
	return nil, nil
}

// RequeueEntityMaintenance returns entity-prune targets to the queue. The in-process
// store has no queue to return them to.
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
