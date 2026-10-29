package storage

import (
	"context"
	"sort"
	"time"
)

// AppendDocumentEvents appends document change-history events to an operation's
// result metadata. The postgres backend documents the semantics; this is the
// same list append applied to the in-memory operation table. An operation that
// has since been pruned is not an error: the reference's own write tolerates a
// missing row, because history must never fail the change it describes.
func (s *MemoryStore) AppendDocumentEvents(ctx context.Context, opID string, events []map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if opID == "" || len(events) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return s.writeErr
	}
	op, ok := s.operations[opID]
	if !ok {
		return nil
	}
	op.ResultMetadata = AppendToMetadata(op.ResultMetadata, events)
	op.UpdatedAt = time.Now().UTC()
	s.operations[opID] = op
	return nil
}

// ListDocumentEvents reads a document's change history out of the bank's
// operations, newest first. total and the page bound the operations that carry
// an event for the document, ordered by when the operation was created, so a
// page is stable across calls while the list of events on the page is ordered by
// when each change happened.
func (s *MemoryStore) ListDocumentEvents(ctx context.Context, bankID, documentID string, limit, offset int) (DocumentEventPage, error) {
	if err := ctx.Err(); err != nil {
		return DocumentEventPage{}, err
	}
	s.mu.RLock()
	carrying := make([]Operation, 0)
	for _, op := range s.operations {
		if op.BankID != bankID {
			continue
		}
		if len(documentEventsFor(op.OperationID, op.ResultMetadata, documentID)) == 0 {
			continue
		}
		carrying = append(carrying, op)
	}
	s.mu.RUnlock()
	sort.SliceStable(carrying, func(i, j int) bool {
		if !carrying[i].CreatedAt.Equal(carrying[j].CreatedAt) {
			return carrying[i].CreatedAt.After(carrying[j].CreatedAt)
		}
		return carrying[i].OperationID > carrying[j].OperationID
	})
	events := make([]StoredDocumentEvent, 0)
	for index, op := range carrying {
		if index < offset {
			continue
		}
		if limit >= 0 && index >= offset+limit {
			break
		}
		events = append(events, documentEventsFor(op.OperationID, op.ResultMetadata, documentID)...)
	}
	// Events on one operation are not in time order across operations (a
	// consolidation operation carries the edit that queued it), so the page is
	// ordered by when each change happened.
	SortStoredDocumentEvents(events)
	return DocumentEventPage{
		DocumentID: documentID, Total: len(carrying), Limit: limit, Offset: offset, Events: events,
	}, nil
}

// DocumentFactSources returns fact id -> document id for the given facts. The
// in-memory units carry their document directly, so this is a map lookup; a fact
// that no longer exists, or that belongs to no document, is absent.
func (s *MemoryStore) DocumentFactSources(ctx context.Context, bankID string, factIDs []string) (map[string]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	documents := make(map[string]string, len(factIDs))
	for _, factID := range factIDs {
		unit, ok := s.units[factID]
		if !ok {
			// A fact curated to the invalidated state left the live view but has
			// not been deleted, so a consolidation that named it still resolves.
			unit, ok = s.invalidated[factID]
		}
		if ok && unit.BankID == bankID && unit.DocumentID != "" {
			documents[factID] = unit.DocumentID
		}
	}
	return documents, nil
}
