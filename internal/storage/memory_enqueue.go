package storage

import (
	"context"
	"encoding/json"
	"time"
)

// EnqueueOperation inserts an operation unless work for the same subject is
// already queued or running.
//
// The check and the insert happen under one lock, so two callers reaching the
// decision at the same moment cannot both act on it — which is the whole point,
// since the background loop that uses this runs in every process of a
// deployment with no leader election.
func (s *MemoryStore) EnqueueOperation(ctx context.Context, req EnqueueRequest) (EnqueueResult, error) {
	if err := ctx.Err(); err != nil {
		return EnqueueResult{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.writeErr != nil {
		return EnqueueResult{}, s.writeErr
	}

	op := req.Operation
	if op.ID == "" {
		op.ID = newID()
	}
	if op.Status == "" {
		op.Status = StatusPending
	}

	if subject := subjectValue(op.Payload, req.DedupeByPayloadKey); subject != "" {
		if existing, ok := s.operationForSubject(op, req.DedupeByPayloadKey, subject, req.DedupeIncludesProcessing); ok {
			return EnqueueResult{Operation: existing, Deduplicated: true}, nil
		}
	}

	now := time.Now().UTC()
	op.CreatedAt = now
	op.UpdatedAt = now
	if op.Run == 0 {
		op.Run = 1
	}
	s.operations[op.ID] = op
	return EnqueueResult{Operation: op}, nil
}

// subjectValue reads the payload key a dedup check compares, rendered as a
// string. A non-string value is rendered rather than skipped so a numeric
// identifier deduplicates too.
func subjectValue(payload map[string]any, key string) string {
	if key == "" || payload == nil {
		return ""
	}
	value, ok := payload[key]
	if !ok || value == nil {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	// A JSON round trip renders any remaining shape deterministically, which is
	// what the PostgreSQL side compares by, so both backends agree on equality.
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// operationForSubject returns an in-flight operation of the same type carrying
// the same subject value.
//
// A pending operation always matches. Whether a running one does is the
// caller's decision: an automatic trigger is content with a running refresh
// covering its intent, while an explicit request may need a run of its own
// because the running one could have read its inputs before the caller's edit.
func (s *MemoryStore) operationForSubject(op Operation, key, subject string, includeProcessing bool) (Operation, bool) {
	for _, existing := range s.operations {
		if existing.ID == op.ID || existing.OperationType != op.OperationType {
			continue
		}
		if existing.BankID != op.BankID {
			continue
		}
		switch existing.Status {
		case StatusPending:
		case StatusProcessing:
			if !includeProcessing {
				continue
			}
		default:
			continue
		}
		if subjectValue(existing.Payload, key) == subject {
			return existing, true
		}
	}
	return Operation{}, false
}
