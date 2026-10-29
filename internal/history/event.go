// Package history is the per-document change history (#5638).
//
// Observations are derived data, rebuilt whenever a fact under them changes. The
// rebuild deletes the old ones outright, so without this nothing records that
// they ever existed, and a rebuild that merged two unrelated events cannot be
// found afterwards.
//
// No table of its own: each change is written onto the async operation that
// carried it, as a `document_events` list in `result_metadata` — ids and counts
// only, never text, every id list capped at maxIDs. A retain or append lands on
// its own operation, a consolidation run on its own, and a fact edit,
// invalidation or retag on the consolidation operation it queues (the one that
// rebuilds what it removed). The history of a document is then a read over the
// bank's operations, which bounds it by design: it reaches back as far as
// operations are kept, and a change that ran without an operation — a
// synchronous retain, or an edit with auto-consolidation off — is not in it.
//
// Recording has two halves. The write sites deep in the pipeline (the
// stale-observation sweep, and consolidation's create/update/delete) note what
// they touched on the ObservationsChanges bound by TrackChanges; a no-op when
// nothing is bound. The operation that bound it knows which document it was
// about and appends the events with AppendOperationEvents, after it commits and
// best-effort: history must never fail the change it describes.
package history

import (
	"sort"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/timefmt"
)

// maxIDs is the number of ids kept per list on one event. The count is always
// exact; past this only the first ids are kept.
const maxIDs = 100

// Kind is what a change was. A named type rather than a bare string: the values
// are the wire contract's enum, and a writer that misspells one would otherwise
// land an event no reader recognises.
type Kind string

// The change kinds, in the reference's declaration order.
const (
	KindRetain              Kind = "retain"
	KindAppend              Kind = "append"
	KindReprocess           Kind = "reprocess"
	KindRetag               Kind = "retag"
	KindFactEdited          Kind = "fact_edited"
	KindFactInvalidated     Kind = "fact_invalidated"
	KindFactRestored        Kind = "fact_restored"
	KindFactDeleted         Kind = "fact_deleted"
	KindObservationsCleared Kind = "observations_cleared"
	KindConsolidation       Kind = "consolidation"
)

// CurationKinds maps a curation action to the change-history kind it records:
// an edit, an invalidation and a revert are the three curation actions that
// change a fact.
var CurationKinds = map[string]Kind{
	"edit":       KindFactEdited,
	"invalidate": KindFactInvalidated,
	"revert":     KindFactRestored,
}

// IdSample is how many observations an event touched, and (up to maxIDs) which.
// It always renders as {"count": n, "ids": [...]}, the shape the reference's
// model serializes, so a reader can select the ids unconditionally.
type IdSample struct {
	Count int      `json:"count"`
	IDs   []string `json:"ids"`
}

// Of builds a sample from the ids a write site touched: sorted, de-duplicated,
// and capped at maxIDs while the count keeps the exact number.
func Of(ids []string) IdSample {
	unique := uniqueSorted(ids)
	sample := IdSample{Count: len(unique), IDs: unique}
	if len(sample.IDs) > maxIDs {
		sample.IDs = sample.IDs[:maxIDs]
	}
	if sample.IDs == nil {
		sample.IDs = []string{}
	}
	return sample
}

// OfSourceFacts builds a sample from observations keyed by their source facts:
// every key is one observation the event touched.
func OfSourceFacts[V any](observations map[string]V) IdSample {
	ids := make([]string, 0, len(observations))
	for id := range observations {
		ids = append(ids, id)
	}
	return Of(ids)
}

// Event is one change to a document, and its effect on the facts and
// observations built from it.
//
// OperationID is deliberately absent from the stored form: the operation an
// event was recorded on is known by the row it lives on, and is filled in when
// the history is read.
type Event struct {
	DocumentID string `json:"document_id"`
	Kind       Kind   `json:"kind"`
	// CreatedAt is served in the reference's isoformat form (+00:00 offset), the
	// same shape its model dumps and every other served timestamp carries.
	CreatedAt string `json:"created_at"`
	// OperationID is the operation this event was recorded on. It is not part of
	// the stored form; the read fills it in from the row the event lives on.
	OperationID *string `json:"operation_id"`
	// FactID is the fact a fact_* event changed.
	FactID     *string `json:"fact_id"`
	FactsAdded int     `json:"facts_added"`
	// FactsRemoved is the number of facts the change retired.
	FactsRemoved        int      `json:"facts_removed"`
	ObservationsCreated IdSample `json:"observations_created"`
	ObservationsUpdated IdSample `json:"observations_updated"`
	ObservationsRemoved IdSample `json:"observations_removed"`
}

// NewEvent builds an event of the given kind, stamped now, with every sample
// present as the reference's model default serializes it.
func NewEvent(documentID string, kind Kind) Event {
	return Event{
		DocumentID:          documentID,
		Kind:                kind,
		CreatedAt:           timefmt.FormatTimestamp(time.Now().UTC()),
		ObservationsCreated: Of(nil),
		ObservationsUpdated: Of(nil),
		ObservationsRemoved: Of(nil),
	}
}

// WithFact sets the fact a fact_* event changed.
func (e Event) WithFact(factID string) Event {
	if factID == "" {
		return e
	}
	e.FactID = &factID
	return e
}

// WithRemoved sets the observations the change removed.
func (e Event) WithRemoved(sample IdSample) Event {
	e.ObservationsRemoved = sample
	return e
}

// stored renders the event as the JSON object an operation's list carries: the
// whole model except operation_id, with created_at in the reference's
// serialized form (an ISO-8601 instant in UTC with microseconds).
func (e Event) stored() map[string]any {
	entry := map[string]any{
		"document_id":          e.DocumentID,
		"kind":                 string(e.Kind),
		"created_at":           e.CreatedAt,
		"fact_id":              nil,
		"facts_added":          e.FactsAdded,
		"facts_removed":        e.FactsRemoved,
		"observations_created": sampleObject(e.ObservationsCreated),
		"observations_updated": sampleObject(e.ObservationsUpdated),
		"observations_removed": sampleObject(e.ObservationsRemoved),
	}
	if e.FactID != nil {
		entry["fact_id"] = *e.FactID
	}
	return entry
}

// sampleObject renders a sample as the stored JSON object.
func sampleObject(sample IdSample) map[string]any {
	ids := sample.IDs
	if ids == nil {
		ids = []string{}
	}
	return map[string]any{"count": sample.Count, "ids": ids}
}

// storedEvents renders a list of events for one append.
func storedEvents(events []Event) []map[string]any {
	out := make([]map[string]any, 0, len(events))
	for _, event := range events {
		if event.DocumentID == "" {
			continue
		}
		out = append(out, event.stored())
	}
	return out
}

// ObservationsChanges is what the write sites did while an operation was
// tracked: an observation id, and the facts it stood on.
type ObservationsChanges struct {
	Created map[string][]string
	Updated map[string][]string
	Removed map[string][]string
	// SweptFacts are the facts handed to the stale-observation sweep: for a
	// retain, the facts it retired.
	SweptFacts map[string]struct{}
}

// newObservationsChanges builds an empty record of a tracked operation's
// observation writes.
func newObservationsChanges() *ObservationsChanges {
	return &ObservationsChanges{
		Created: map[string][]string{}, Updated: map[string][]string{}, Removed: map[string][]string{},
		SweptFacts: map[string]struct{}{},
	}
}

// SourceFactIDs returns every fact any tracked observation stood on — what a
// consolidation attributes its changes to a document through.
func (c *ObservationsChanges) SourceFactIDs() []string {
	seen := map[string]bool{}
	out := make([]string, 0)
	for _, observations := range []map[string][]string{c.Created, c.Updated, c.Removed} {
		for _, sources := range observations {
			for _, source := range sources {
				if source == "" || seen[source] {
					continue
				}
				seen[source] = true
				out = append(out, source)
			}
		}
	}
	sort.Strings(out)
	return out
}

// ByDocument files a consolidation job's observation writes under the documents
// their sources came from: one consolidation event per document an observation
// stands on, given the fact id -> document id map of the tracked facts.
func (c *ObservationsChanges) ByDocument(factDocument map[string]string) map[string]Event {
	perDocument := map[string]*ObservationsChanges{}
	collect := func(observations map[string][]string, pick func(*ObservationsChanges) map[string][]string) {
		for observationID, sources := range observations {
			documents := map[string]bool{}
			for _, source := range sources {
				if documentID, ok := factDocument[source]; ok && documentID != "" {
					documents[documentID] = true
				}
			}
			for documentID := range documents {
				changes, ok := perDocument[documentID]
				if !ok {
					changes = newObservationsChanges()
					perDocument[documentID] = changes
				}
				pick(changes)[observationID] = sources
			}
		}
	}
	collect(c.Created, func(changes *ObservationsChanges) map[string][]string { return changes.Created })
	collect(c.Updated, func(changes *ObservationsChanges) map[string][]string { return changes.Updated })
	collect(c.Removed, func(changes *ObservationsChanges) map[string][]string { return changes.Removed })
	events := make(map[string]Event, len(perDocument))
	for documentID, changes := range perDocument {
		event := NewEvent(documentID, KindConsolidation)
		event.ObservationsCreated = OfSourceFacts(changes.Created)
		event.ObservationsUpdated = OfSourceFacts(changes.Updated)
		event.ObservationsRemoved = OfSourceFacts(changes.Removed)
		events[documentID] = event
	}
	return events
}

// uniqueSorted sorts and de-duplicates the ids, dropping empty ones.
func uniqueSorted(ids []string) []string {
	unique := make([]string, 0, len(ids))
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		unique = append(unique, id)
	}
	sort.Strings(unique)
	return unique
}
