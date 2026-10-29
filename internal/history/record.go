package history

import (
	"context"
	"sort"
)

// DeletionEvents builds one fact_deleted event per document, given the deleted
// facts (fact id -> document id) and what the sweep removed: each document gets
// the observations that stood on its own facts. A fact with no document is not
// filed anywhere, and fact_id is named only when exactly one fact was deleted
// from that document.
func DeletionEvents(deleted map[string]string, changes *ObservationsChanges) []Event {
	byDocument := map[string][]string{}
	for factID, documentID := range deleted {
		if documentID == "" {
			continue
		}
		byDocument[documentID] = append(byDocument[documentID], factID)
	}
	documents := make([]string, 0, len(byDocument))
	for documentID := range byDocument {
		documents = append(documents, documentID)
	}
	sort.Strings(documents)
	events := make([]Event, 0, len(documents))
	for _, documentID := range documents {
		facts := uniqueSorted(byDocument[documentID])
		event := NewEvent(documentID, KindFactDeleted)
		event.FactsRemoved = len(facts)
		if len(facts) == 1 {
			event = event.WithFact(facts[0])
		}
		removed := make([]string, 0)
		if changes != nil {
			for observationID, sources := range changes.Removed {
				if intersects(sources, facts) {
					removed = append(removed, observationID)
				}
			}
		}
		event.ObservationsRemoved = Of(removed)
		events = append(events, event)
	}
	return events
}

// RetainEvent builds the event one retain lands on its own operation: its kind
// from the write's mode, the facts it added, the facts it retired and the
// observations the sweep dropped.
func RetainEvent(documentID string, updateMode string, forceReextract bool, factsAdded int, changes *ObservationsChanges) Event {
	kind := KindRetain
	switch {
	case forceReextract:
		kind = KindReprocess
	case updateMode == "append":
		kind = KindAppend
	}
	event := NewEvent(documentID, kind)
	event.FactsAdded = factsAdded
	if changes != nil {
		event.FactsRemoved = len(changes.SweptFacts)
		event.ObservationsRemoved = OfSourceFacts(changes.Removed)
	}
	return event
}

// CurationEvents builds the event a fact edit, invalidation or restore records
// on the consolidation it queues. A curation action that is not one of the three
// — or a fact with no document — records nothing, which is what the reference's
// `curation_action in _CURATION_EVENT_KINDS and curated.document_id` test does.
func CurationEvents(curationAction, documentID, factID string, changes *ObservationsChanges) []Event {
	kind, ok := CurationKinds[curationAction]
	if !ok || documentID == "" {
		return nil
	}
	event := NewEvent(documentID, kind).WithFact(factID)
	if changes != nil {
		event.ObservationsRemoved = OfSourceFacts(changes.Removed)
	}
	return []Event{event}
}

// ObservationsClearedEvents builds the event one memory's observation clear
// records on the consolidation it queues, naming the fact it cleared.
func ObservationsClearedEvents(documentID, factID string, changes *ObservationsChanges) []Event {
	if documentID == "" {
		return nil
	}
	event := NewEvent(documentID, KindObservationsCleared).WithFact(factID)
	if changes != nil {
		event.ObservationsRemoved = OfSourceFacts(changes.Removed)
	}
	return []Event{event}
}

// EventWriter is what recording an event needs from a backend: the one write
// that appends a list of events to an operation's result metadata. It is a narrow
// interface rather than storage.Store so a component that already holds a
// narrower storage view (the consolidation pass) can record history without
// widening what it depends on.
type EventWriter interface {
	AppendDocumentEvents(ctx context.Context, opID string, events []map[string]any) error
}

// DocumentLookup is what filing a consolidation's events needs beyond the
// append: the document each of a set of facts was extracted from.
type DocumentLookup interface {
	DocumentFactSources(ctx context.Context, bankID string, factIDs []string) (map[string]string, error)
}

// AppendOperationEvents adds events to the operation's
// result_metadata.document_events. Best-effort: a failure is logged, never
// raised, because the history must never fail the change it describes.
func AppendOperationEvents(ctx context.Context, store EventWriter, operationID string, events ...Event) {
	stored := storedEvents(events)
	if store == nil || operationID == "" || len(stored) == 0 {
		return
	}
	if err := store.AppendDocumentEvents(ctx, operationID, stored); err != nil {
		logHistoryFailure(operationID, err)
	}
}

// RecordConsolidationEvents files a consolidation job's observation writes under
// the documents their sources came from. A job that touched no fact, or an
// operation id the caller does not have, records nothing.
func RecordConsolidationEvents(ctx context.Context, store EventWriter, bankID string, changes *ObservationsChanges, operationID string) {
	if store == nil || operationID == "" || changes == nil {
		return
	}
	factIDs := changes.SourceFactIDs()
	if len(factIDs) == 0 {
		return
	}
	factDocument, err := resolveFactDocuments(ctx, store, bankID, factIDs)
	if err != nil {
		logHistoryFailure(operationID, err)
		return
	}
	byDocument := changes.ByDocument(factDocument)
	documents := make([]string, 0, len(byDocument))
	for documentID := range byDocument {
		documents = append(documents, documentID)
	}
	sort.Strings(documents)
	events := make([]Event, 0, len(documents))
	for _, documentID := range documents {
		events = append(events, byDocument[documentID])
	}
	AppendOperationEvents(ctx, store, operationID, events...)
}

// AppendRetagEvent builds the retag event a tag or observation-scope change
// records on the consolidation it queues, and appends it there.
//
// removed names the observations the change's sweep deleted. The caller passes
// their ids: this package does not depend on storage (the storage layer records
// history itself, so importing back would cycle).
func AppendRetagEvent(ctx context.Context, store EventWriter, operationID, documentID string, removed []string) {
	if operationID == "" || documentID == "" {
		return
	}
	AppendOperationEvents(ctx, store, operationID, NewEvent(documentID, KindRetag).WithRemoved(Of(removed)))
}

// resolveFactDocuments reads the document each fact belongs to. A fact that has
// since been deleted contributes nothing: its change is already recorded under
// whatever event removed it.
func resolveFactDocuments(ctx context.Context, store EventWriter, bankID string, factIDs []string) (map[string]string, error) {
	reader, ok := store.(DocumentLookup)
	if !ok {
		return map[string]string{}, nil
	}
	facts, err := reader.DocumentFactSources(ctx, bankID, factIDs)
	if err != nil {
		return nil, err
	}
	documents := make(map[string]string, len(facts))
	for factID, documentID := range facts {
		if documentID != "" {
			documents[factID] = documentID
		}
	}
	return documents, nil
}

// intersects reports whether the two id lists share a member.
func intersects(left, right []string) bool {
	if len(left) == 0 || len(right) == 0 {
		return false
	}
	seen := make(map[string]bool, len(left))
	for _, id := range left {
		seen[id] = true
	}
	for _, id := range right {
		if seen[id] {
			return true
		}
	}
	return false
}
