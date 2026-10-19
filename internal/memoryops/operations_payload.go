package memoryops

import "github.com/studyzy/wenyuan/internal/storage"

// fullTaskPayload is the row's stored `task_payload`, verbatim.
//
// It is not synthesized: the reference selects the column and parses it
// (`engine/memory_engine.py:21220-21237`), and a real payload already carries
// its own `type`, `bank_id` and `operation_id` keys because the submitter wrote
// them there. Adding them here invented members the row does not hold -- a
// seeded or imported row whose payload lacks them grew three keys the
// reference never sends, which is what the live comparison caught.
//
// A retain whose payload column holds an empty object is the one shape the
// reference reports as null rather than `{}`: its task type is defined by the
// payload, so an empty one is not a retain task at all.
func fullTaskPayload(op storage.Operation) map[string]any {
	if op.Payload == nil {
		return nil
	}
	if op.OperationType == storage.AsyncRetainType && len(op.Payload) == 0 {
		return nil
	}
	payload := make(map[string]any, len(op.Payload))
	for key, value := range op.Payload {
		payload[key] = value
	}
	return payload
}
