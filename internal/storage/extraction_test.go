package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// stubExtraction is a scripted fact extractor for tests.
type stubExtraction struct {
	facts map[string][]ExtractedFact
	err   error
	calls []string
}

// Extract returns the facts scripted for the given text, or the scripted error.
func (s *stubExtraction) Extract(_ context.Context, text string) ([]ExtractedFact, error) {
	s.calls = append(s.calls, text)
	if s.err != nil {
		return nil, s.err
	}
	return s.facts[text], nil
}

// A write with an extractor configured stores one unit per extracted fact
// rather than one per segment of the item's text.
func TestRetainStoresOneUnitPerFact(t *testing.T) {
	store := NewMemoryStore("public")
	content := "Alice works at Google.\n\nGoogle is in California."
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "Alice works at Google.", Entities: []RetainEntity{{Text: "Alice"}, {Text: "Google"}}},
			{Text: "Google is in California.", Entities: []RetainEntity{{Text: "Google"}, {Text: "California"}}},
			{Text: "Alice is employed.", Confidence: 0.75},
		},
	}}
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(outcome.ItemUnitIDs[0]); got != 3 {
		t.Fatalf("stored %d units, want one per extracted fact (3)", got)
	}
	if len(extractor.calls) != 1 || extractor.calls[0] != content {
		t.Fatalf("extractor calls = %v, want the item content once", extractor.calls)
	}

	units := store.UnitsForTest("b1")
	if len(units) != 3 {
		t.Fatalf("stored %d units, want 3", len(units))
	}
	texts := make(map[string]MemoryUnit, len(units))
	for _, unit := range units {
		texts[unit.Text] = unit
	}
	if _, ok := texts["Alice is employed."]; !ok {
		t.Fatalf("extracted fact text was not stored: %+v", texts)
	}
	if confidence := texts["Alice is employed."].Confidence; confidence != 0.75 {
		t.Fatalf("confidence = %v, want the extracted 0.75", confidence)
	}
}

// An extracted fact carries its own dates, overriding the item's timestamp.
func TestRetainFactDatesOverrideItemTimestamp(t *testing.T) {
	store := NewMemoryStore("public")
	content := "The launch happened in March."
	itemTime := parseTime(t, "2020-01-01T00:00:00Z")
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {{
			Text:          "The launch happened in March.",
			OccurredStart: "2024-03-01T00:00:00Z",
			OccurredEnd:   "2024-03-31T00:00:00Z",
		}},
	}}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1", Timestamp: itemTime, HasTimestamp: true}},
		Extraction: extractor,
	}); err != nil {
		t.Fatal(err)
	}
	units := store.UnitsForTest("b1")
	if len(units) != 1 {
		t.Fatalf("stored %d units, want 1", len(units))
	}
	unit := units[0]
	if unit.OccurredStart == nil || !unit.OccurredStart.Equal(*parseTime(t, "2024-03-01T00:00:00Z")) {
		t.Fatalf("occurred start = %v, want the extracted date", unit.OccurredStart)
	}
	if unit.OccurredEnd == nil || !unit.OccurredEnd.Equal(*parseTime(t, "2024-03-31T00:00:00Z")) {
		t.Fatalf("occurred end = %v, want the extracted date", unit.OccurredEnd)
	}
}

// Facts extracted from one item are linked to each other, so the graph
// connects what was written together.
func TestRetainLinksFactsFromOneItem(t *testing.T) {
	store := NewMemoryStore("public")
	content := "first fact\n\nsecond fact\n\nthird fact"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {{Text: "first fact"}, {Text: "second fact"}, {Text: "third fact"}},
	}}
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	units := outcome.ItemUnitIDs[0]
	if len(units) != 3 {
		t.Fatalf("stored %d units, want 3", len(units))
	}
	if len(store.links) != 2 {
		t.Fatalf("created %d links, want one between each consecutive pair", len(store.links))
	}
	for i, link := range store.links {
		if link.FromID != units[i] || link.ToID != units[i+1] {
			t.Fatalf("link %d joins %s->%s, want %s->%s", i, link.FromID, link.ToID, units[i], units[i+1])
		}
		if link.LinkType != LinkSemantic {
			t.Fatalf("link %d type = %q, want semantic", i, link.LinkType)
		}
	}
}

// An extractor that finds nothing leaves the verbatim path in place, so the
// document is still stored rather than silently dropped.
func TestRetainFallsBackToVerbatimWhenExtractionFindsNothing(t *testing.T) {
	store := NewMemoryStore("public")
	content := "some prose the model found no facts in"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{}}
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemUnitIDs[0]) != 1 || len(store.chunks) != 1 {
		t.Fatalf("expected the verbatim path: units=%v chunks=%d", outcome.ItemUnitIDs, len(store.chunks))
	}
	if units := store.UnitsForTest("b1"); len(units) != 1 || units[0].Text != content {
		t.Fatalf("verbatim unit not stored: %+v", units)
	}
}

// A failed extraction fails the write, so the caller learns the write did not
// happen rather than discovering later that it was stored a second way.
func TestRetainFailsWhenExtractionFails(t *testing.T) {
	store := NewMemoryStore("public")
	extractor := &stubExtraction{err: errors.New("model unavailable")}
	_, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: "text", DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err == nil {
		t.Fatal("expected the write to fail when extraction fails")
	}
	if len(store.units) != 0 {
		t.Fatalf("a failed extraction must store nothing, got %d units", len(store.units))
	}
}

// Without an extractor the write stores the item's text verbatim, which is what
// a deployment with no model configured gets.
func TestRetainWithoutExtractorStoresVerbatim(t *testing.T) {
	store := NewMemoryStore("public")
	content := "first paragraph\n\nsecond paragraph"
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items:  []RetainItem{{Content: content, DocumentID: "doc-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemUnitIDs[0]) != 1 {
		t.Fatalf("expected one verbatim unit, got %v", outcome.ItemUnitIDs)
	}
}

// Facts and caller-supplied entities are both attached to the units a fact
// produced, so neither source of entity is lost.
func TestRetainFactUnitsCarryBothEntitySources(t *testing.T) {
	store := NewMemoryStore("public")
	content := "Alice met Bob."
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {{Text: "Alice met Bob.", Entities: []RetainEntity{{Text: "Bob"}}}},
	}}
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Items: []RetainItem{{
			Content:    content,
			DocumentID: "doc-1",
			Entities:   []RetainEntity{{Text: "Alice", Kind: "PERSON"}},
		}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	unitID := outcome.ItemUnitIDs[0][0]
	entityIDs := store.unitEntities[unitID]
	if len(entityIDs) != 2 {
		t.Fatalf("unit carries %d entities, want the supplied and the extracted one", len(entityIDs))
	}
	found := make(map[string]bool, 2)
	for _, id := range entityIDs {
		found[store.entities[id].Name] = true
	}
	for _, want := range []string{"Alice", "Bob"} {
		if !found[want] {
			t.Fatalf("entity %q missing from the unit: %+v", want, found)
		}
	}
}

// parseTime parses an RFC 3339 timestamp or fails the test.
func parseTime(t *testing.T, value string) *time.Time {
	t.Helper()
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		t.Fatalf("bad test timestamp %q: %v", value, err)
	}
	return &parsed
}

// A fact's declared cause becomes a caused_by link from the effect's unit to
// the causing unit, in that direction: the edge records what caused it.
func TestRetainPersistsCausalLinks(t *testing.T) {
	store := NewMemoryStore("public")
	content := "lost the job, then could not pay rent"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "They lost the job."},
			{Text: "They could not pay rent.", CausedByIndexes: []int{0}},
		},
	}}
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	units := outcome.ItemUnitIDs[0]
	if len(units) != 2 {
		t.Fatalf("stored %d units, want 2", len(units))
	}
	graph, err := store.Graph(context.TODO(), "b1", 100)
	if err != nil {
		t.Fatal(err)
	}
	var causal []GraphEdgeData
	for _, edge := range graph.Edges {
		if edge.Type == string(LinkCausedBy) {
			causal = append(causal, edge)
		}
	}
	if len(causal) != 1 {
		t.Fatalf("caused_by edges = %d, want 1: %+v", len(causal), graph.Edges)
	}
	if causal[0].From != units[1] || causal[0].To != units[0] {
		t.Fatalf("edge = %s -> %s, want effect %s -> cause %s", causal[0].From, causal[0].To, units[1], units[0])
	}
}

// A causal index that names no unit this write stored is dropped rather than
// persisted as a dangling edge.
func TestCausalLinkOutOfRangeIsDropped(t *testing.T) {
	store := NewMemoryStore("public")
	content := "standalone fact"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {{Text: "A fact with no cause.", CausedByIndexes: []int{4, -1}}},
	}}
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemUnitIDs[0]) != 1 {
		t.Fatalf("stored %d units, want 1", len(outcome.ItemUnitIDs[0]))
	}
	graph, err := store.Graph(context.TODO(), "b1", 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range graph.Edges {
		if edge.Type == string(LinkCausedBy) {
			t.Fatalf("a droppable causal index produced an edge: %+v", edge)
		}
	}
}
