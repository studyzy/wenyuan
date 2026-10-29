package storage

import "sort"

// Document change history (#5638) is stored, not tabulated: every change appends
// a `document_events` list to the `result_metadata` of the async operation that
// carried it, and a document's history is a read over the bank's operations.
// These are the types that cross the storage boundary; the event vocabulary
// itself lives in internal/history, so this package only persists and pages the
// JSON list the reference defines.

// maxDocumentEventIDs is the reference's MAX_IDS: an event's count is always
// exact, but only the first ids are kept.
const maxDocumentEventIDs = 100

// DocumentEventKey names the result-metadata key the events are kept under.
const DocumentEventKey = "document_events"

// DocumentEventPage is one page of a document's change history.
type DocumentEventPage struct {
	DocumentID string
	Total      int
	Limit      int
	Offset     int
	// Events are the flattened events of the paged operations, in the order the
	// reference returns them: by each event's own created_at, newest first.
	Events []StoredDocumentEvent
}

// StoredDocumentEvent is one event as it was stored, with the operation it was
// recorded on resolved. The event's own members are kept as they were written
// (they are the reference's JSON shape, served back verbatim), so a reader
// projects them by key through the history package's model.
type StoredDocumentEvent struct {
	OperationID string
	// Members are the event's JSON object, without operation_id.
	Members map[string]any
}

// AppendToMetadata returns metadata with the events appended to its
// document_events list, creating the list when it is absent. It is exported so
// both backends share one definition of "append to the list".
func AppendToMetadata(metadata map[string]any, events []map[string]any) map[string]any {
	if len(events) == 0 {
		return metadata
	}
	merged := make(map[string]any, len(metadata)+1)
	for key, value := range metadata {
		merged[key] = value
	}
	existing, _ := merged[DocumentEventKey].([]any)
	list := make([]any, 0, len(existing)+len(events))
	list = append(list, existing...)
	for _, event := range events {
		list = append(list, event)
	}
	merged[DocumentEventKey] = list
	return merged
}

// DocumentEventsOf returns the events a stored operation carries, as the JSON
// objects they were written as. A row written by an older build carries none.
func DocumentEventsOf(metadata map[string]any) []map[string]any {
	values, ok := metadata[DocumentEventKey].([]any)
	if !ok {
		return nil
	}
	events := make([]map[string]any, 0, len(values))
	for _, value := range values {
		if event, ok := value.(map[string]any); ok {
			events = append(events, event)
		}
	}
	return events
}

// documentEventsFor picks the events one stored operation carries for a
// document and stamps each with the operation it was recorded on, the way the
// reference fills operation_id on read.
func documentEventsFor(operationID string, metadata map[string]any, documentID string) []StoredDocumentEvent {
	stored := make([]StoredDocumentEvent, 0)
	for _, event := range DocumentEventsOf(metadata) {
		if id, _ := event["document_id"].(string); id != documentID {
			continue
		}
		members := make(map[string]any, len(event))
		for key, value := range event {
			members[key] = value
		}
		members["operation_id"] = operationID
		stored = append(stored, StoredDocumentEvent{OperationID: operationID, Members: members})
	}
	return stored
}

// SortStoredDocumentEvents orders events by their own created_at, newest first.
// Two events stamped at the same instant keep the order they were read in, which
// is a stable sort.
func SortStoredDocumentEvents(events []StoredDocumentEvent) {
	sort.SliceStable(events, func(i, j int) bool {
		return createdOrderKey(events[i]) > createdOrderKey(events[j])
	})
}

// createdOrderKey renders the instant an event was created at, in a form that
// sorts lexicographically: the reference stores created_at as an ISO-8601 string
// with a fixed shape, so the string order is the time order.
func createdOrderKey(event StoredDocumentEvent) string {
	created, _ := event.Members["created_at"].(string)
	return created
}

// CappedDocumentEventIDs sorts and dedupes the ids of one observation sample and
// caps the list at MAX_IDS. The caller reports the full count beside it.
func CappedDocumentEventIDs(ids []string) []string {
	if len(ids) == 0 {
		return []string{}
	}
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	sort.Strings(unique)
	if len(unique) > maxDocumentEventIDs {
		unique = unique[:maxDocumentEventIDs]
	}
	return unique
}
