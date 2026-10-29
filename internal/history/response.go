package history

import (
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/timefmt"
)

// StoredEvent is one event as a backend holds it: the operation it was recorded
// on, and the event's JSON object. It is declared here rather than reusing the
// storage type so this package stays free of a storage import — the storage layer
// records history itself, and importing back would cycle.
type StoredEvent struct {
	OperationID string
	Members     map[string]any
}

// StoredPage is one page of a document's history as a backend returns it.
type StoredPage struct {
	DocumentID string
	Total      int
	Limit      int
	Offset     int
	Events     []StoredEvent
}

// Response is a document's change history, newest first. total, limit and
// offset count OPERATIONS: one operation can carry several events for the same
// document (an oversized retain in slices).
type Response struct {
	DocumentID string  `json:"document_id"`
	Total      int     `json:"total"`
	Limit      int     `json:"limit"`
	Offset     int     `json:"offset"`
	Items      []Event `json:"items"`
}

// Page builds the served response from one storage page, rendering each stored
// event through the model's own JSON shape with the operation it was recorded
// on filled in.
func Page(page StoredPage) Response {
	items := make([]Event, 0, len(page.Events))
	for _, stored := range page.Events {
		items = append(items, fromStored(stored))
	}
	return Response{
		DocumentID: page.DocumentID, Total: page.Total, Limit: page.Limit, Offset: page.Offset, Items: items,
	}
}

// fromStored rebuilds an event from its stored JSON object. A member the stored
// row does not carry reads as the model's default, so a row written by an older
// shape still renders the shape the contract declares.
func fromStored(stored StoredEvent) Event {
	members := stored.Members
	event := Event{
		DocumentID:          stringMember(members, "document_id"),
		Kind:                Kind(stringMember(members, "kind")),
		CreatedAt:           servedStamp(stringMember(members, "created_at")),
		OperationID:         operationRef(stored.OperationID),
		FactsAdded:          intMember(members, "facts_added"),
		FactsRemoved:        intMember(members, "facts_removed"),
		ObservationsCreated: sampleMember(members, "observations_created"),
		ObservationsUpdated: sampleMember(members, "observations_updated"),
		ObservationsRemoved: sampleMember(members, "observations_removed"),
	}
	if factID, ok := members["fact_id"].(string); ok && factID != "" {
		event.FactID = &factID
	}
	return event
}

// stringMember reads a string member, reporting "" for an absent or non-string
// value.
func stringMember(members map[string]any, key string) string {
	value, _ := members[key].(string)
	return value
}

// intMember reads an integer member — a stored count arrives as a JSON number,
// and a writer that rendered it as text still reads as its value.
func intMember(members map[string]any, key string) int {
	switch value := members[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case string:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0
		}
		return parsed
	default:
		return 0
	}
}

// sampleMember reads one observation sample, defaulting to an empty one.
func sampleMember(members map[string]any, key string) IdSample {
	sample := IdSample{IDs: []string{}}
	value, ok := members[key].(map[string]any)
	if !ok {
		return sample
	}
	sample.Count = intMember(value, "count")
	switch ids := value["ids"].(type) {
	case []any:
		for _, id := range ids {
			if text, ok := id.(string); ok {
				sample.IDs = append(sample.IDs, text)
			}
		}
	case []string:
		sample.IDs = append([]string{}, ids...)
	}
	return sample
}

// operationRef renders the operation an event was recorded on. An event read
// without one — a stored row the reader could not attribute — keeps the null the
// model declares rather than a empty string.
func operationRef(operationID string) *string {
	if operationID == "" {
		return nil
	}
	return &operationID
}

// servedStamp renders the stored created_at in the served form. The stored value
// is the reference's serialized datetime, which is already the served shape; a
// value written in another ISO form is read back and re-rendered so every event
// on a page carries one shape.
func servedStamp(value string) string {
	if value == "" {
		return ""
	}
	stamp := strings.Replace(value, "Z", "+00:00", 1)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05.999999-07"} {
		parsed, err := time.Parse(layout, stamp)
		if err != nil {
			continue
		}
		return timefmt.FormatTimestamp(parsed)
	}
	return value
}
