package storage

import (
	"context"
	"encoding/json"
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
	// The three facts share a date, so the reference's within-batch temporal pass
	// links every ordered pair of them, not only the consecutive ones
	// (`memories/pg/links.py:85-146`: each unit links to its successors AND the
	// reverse edge is written).
	temporal := make([]MemoryLink, 0, len(store.links))
	for _, link := range store.links {
		if link.LinkType == LinkTemporal {
			temporal = append(temporal, link)
		}
	}
	forward := map[string]bool{}
	for _, link := range temporal {
		forward[link.FromUnitID+"->"+link.ToUnitID] = true
	}
	for i := range units {
		for j := range units {
			if i == j {
				continue
			}
			if !forward[units[i]+"->"+units[j]] {
				t.Fatalf("missing temporal link %s->%s", units[i], units[j])
			}
		}
	}
}

// A model was asked and found nothing: the item's text is stored on the
// document but yields no memory unit. Falling back to the raw text here would
// store prose the model explicitly judged factless — which is how a document
// that says nothing memorable ends up answering recalls with its own wording.
func TestRetainStoresNoUnitWhenExtractionFindsNothing(t *testing.T) {
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
	if len(outcome.ItemUnitIDs[0]) != 0 || len(store.chunks) != 1 {
		t.Fatalf("expected no units and one stored chunk: units=%v chunks=%d", outcome.ItemUnitIDs, len(store.chunks))
	}
	if units := store.UnitsForTest("b1"); len(units) != 0 {
		t.Fatalf("an empty extraction must store no unit: %+v", units)
	}
	doc, err := store.GetDocument(context.TODO(), "b1", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.OriginalText != content {
		t.Fatalf("the document text must still be stored: %q", doc.OriginalText)
	}
}

// A failed extraction fails the write, which is what makes the failure visible
// on the operation record. (The reference's
// HINDSIGHT_API_FAIL_ON_EXTRACTION_ERRORS escalates *batch-API* extraction
// errors; this build has no batch path, so the streaming path's failure is the
// one that reaches the operation.)
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
		found[store.entities[id].CanonicalName] = true
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
	graph, err := store.Graph(context.TODO(), GraphQuery{BankID: "b1", Limit: 100})
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
	graph, err := store.Graph(context.TODO(), GraphQuery{BankID: "b1", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range graph.Edges {
		if edge.Type == string(LinkCausedBy) {
			t.Fatalf("a droppable causal index produced an edge: %+v", edge)
		}
	}
}

// contextualExtraction records the options it was called with.
type contextualExtraction struct {
	stubExtraction
	options []FactExtractionOptions
}

func (c *contextualExtraction) ExtractWithOptions(ctx context.Context, text string, opts FactExtractionOptions) ([]ExtractedFact, error) {
	c.options = append(c.options, opts)
	return c.Extract(ctx, text)
}

// A request carrying retain configuration reaches the extractor that can use
// it, and a request carrying none takes the plain call.
func TestRetainPassesExtractionOptions(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	when := time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)
	extractor := &contextualExtraction{}
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID:                 "b1",
		Extraction:             extractor,
		ExtractionMission:      "track decisions",
		ExtractionInstructions: "prefer the user's wording",
		ExtractionMode:         "verbose",
		Items: []RetainItem{{
			Content: "Alice decided to ship on Friday.", DocumentID: "doc-1",
			Strategy: "concise", Timestamp: &when,
		}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(extractor.options) != 1 {
		t.Fatalf("contextual calls = %d, want one", len(extractor.options))
	}
	got := extractor.options[0]
	if got.Mission != "track decisions" || got.Instructions != "prefer the user's wording" ||
		got.Strategy != "concise" || got.Mode != "verbose" || !got.ReferenceTime.Equal(when) {
		t.Fatalf("options = %+v", got)
	}

	// An item that named no timestamp is still given the ingest clock as its
	// reference time, because that is the date the prompt states and the anchor
	// a relative phrase resolves against. Only an item that explicitly asked for
	// no timestamp leaves the options bare, so an extractor that ignores
	// configuration is never handed a call it cannot answer.
	plain := &contextualExtraction{}
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "b1", Extraction: plain,
		Items: []RetainItem{{Content: "Alice decided to ship.", DocumentID: "doc-2", HasTimestamp: true}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(plain.options) != 0 {
		t.Fatalf("a timeless item must not use the contextual call: %+v", plain.options)
	}
	if len(plain.calls) != 1 {
		t.Fatalf("plain calls = %v", plain.calls)
	}

	// The ordinary case — no timestamp given — does carry the ingest clock.
	dated := &contextualExtraction{}
	before := time.Now().UTC().Add(-time.Minute)
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "b1", Extraction: dated,
		Items: []RetainItem{{Content: "Alice decided to ship.", DocumentID: "doc-3"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(dated.options) != 1 {
		t.Fatalf("contextual calls = %d, want the ingest clock to reach the extractor", len(dated.options))
	}
	if ref := dated.options[0].ReferenceTime; ref.Before(before) || ref.After(time.Now().UTC()) {
		t.Fatalf("reference time = %v, want the write clock", ref)
	}
}

// The chunk boundaries a request asks for govern the stored segments.
func TestRetainHonoursConfiguredChunkSize(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	// Two sentences, so a small budget must split them and a large one must not.
	content := "Alice joined the team. Bob left the team."
	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "b1", ChunkSize: 24,
		Items: []RetainItem{{Content: content, DocumentID: "doc-small"}},
	}); err != nil {
		t.Fatal(err)
	}
	small, err := store.ListChunks(ctx, ChunkQuery{BankID: "b1", DocumentID: "doc-small", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if small.Total < 2 {
		t.Fatalf("a 24-character budget produced %d chunk(s)", small.Total)
	}

	if _, err := store.RetainWrite(ctx, RetainRequest{
		BankID: "b1", ChunkSize: 3000,
		Items: []RetainItem{{Content: content, DocumentID: "doc-large"}},
	}); err != nil {
		t.Fatal(err)
	}
	large, err := store.ListChunks(ctx, ChunkQuery{BankID: "b1", DocumentID: "doc-large", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if large.Total != 1 {
		t.Fatalf("the default budget produced %d chunk(s), want one", large.Total)
	}
}

// A retain that carries observation scopes writes them onto every unit its
// item produced through the fact path, so consolidation can fold at the scope
// the caller asked to keep separate.
func TestRetainCarriesObservationScopesThroughFactPath(t *testing.T) {
	store := NewMemoryStore("public")
	const content = "Alice moved to Berlin."
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {{Text: "Alice moved to Berlin."}, {Text: "Alice likes Berlin."}},
	}}
	if _, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1", ObservationScopes: json.RawMessage(`"per_tag"`)}},
		Extraction: extractor,
	}); err != nil {
		t.Fatal(err)
	}
	units := store.UnitsForTest("b1")
	if len(units) != 2 {
		t.Fatalf("units = %d, want one per fact (2)", len(units))
	}
	for _, unit := range units {
		if string(unit.ObservationScopes) != `"per_tag"` {
			t.Fatalf("unit %q observation scopes = %s, want the item's", unit.Text, unit.ObservationScopes)
		}
	}
}

// A retain that carries observation scopes writes them onto the verbatim unit
// too, so the segment path is scoped the same way the fact path is.
func TestRetainCarriesObservationScopesThroughVerbatimPath(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID: "b1",
		Items: []RetainItem{{
			Content: "A verbatim note.", DocumentID: "doc-1",
			ObservationScopes: json.RawMessage(`[["a"],["b"]]`),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	units := store.UnitsForTest("b1")
	if len(units) == 0 {
		t.Fatal("no unit was stored")
	}
	for _, unit := range units {
		if string(unit.ObservationScopes) != `[["a"],["b"]]` {
			t.Fatalf("unit observation scopes = %s, want the item's", unit.ObservationScopes)
		}
	}
}

// A fact the model marked "assistant" is stored as the agent-perspective type,
// not as an objective world fact.
func TestRetainStoresAssistantFactAsExperience(t *testing.T) {
	store := NewMemoryStore("public")
	const content = "I checked the deployment log."
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {{Text: "I checked the deployment log.", Type: "assistant"}},
	}}
	if _, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     "b1",
		Items:      []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction: extractor,
	}); err != nil {
		t.Fatal(err)
	}
	units := store.UnitsForTest("b1")
	if len(units) != 1 {
		t.Fatalf("units = %d, want 1", len(units))
	}
	if units[0].FactType != TypeExperience {
		t.Fatalf("assistant fact stored as %q, want %q", units[0].FactType, TypeExperience)
	}
}

// Chunks mode stores each segment of the item as its own unit and asks the
// model nothing, even when an extractor is configured — so a bank on the chunks
// mode keeps its raw segments rather than paying for an extraction it ignores.
func TestChunksModeStoresOneUnitPerChunkWithoutCallingTheModel(t *testing.T) {
	store := NewMemoryStore("public")
	content := "alpha bravo\n\ncharlie delta\n\necho foxtrot"
	extractor := &stubExtraction{}
	outcome, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:         "b1",
		Items:          []RetainItem{{Content: content, DocumentID: "doc-1"}},
		Extraction:     extractor,
		ExtractionMode: "chunks",
		ChunkSize:      5,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(extractor.calls) != 0 {
		t.Fatalf("chunks mode must not call the model: %v", extractor.calls)
	}
	want := len(SegmentWith(content, SegmentOptions{ChunkSize: 5}))
	if got := len(outcome.ItemUnitIDs[0]); got != want {
		t.Fatalf("units = %d, want one per chunk (%d)", got, want)
	}
	if units := store.UnitsForTest("b1"); len(units) != want {
		t.Fatalf("stored units = %d, want %d", len(units), want)
	}
}

// The deployment's causal-links switch reaches the extractor through the write:
// a store tuned with the switch off hands the extractor an explicitly disabled
// option, and a store on the default leaves the option unset so the reference
// default applies (and an unconfigured request keeps taking the plain call).
func TestRetainPassesCausalLinksSwitch(t *testing.T) {
	ctx := context.Background()

	off := NewMemoryStore("public")
	off.SetTuning(Tuning{ExtractCausalLinks: false})
	disabled := &contextualExtraction{}
	if _, err := off.RetainWrite(ctx, RetainRequest{
		BankID:     "b1",
		Extraction: disabled,
		Items:      []RetainItem{{Content: "Alice moved.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(disabled.options) != 1 {
		t.Fatalf("contextual calls = %d, want one", len(disabled.options))
	}
	if opt := disabled.options[0].ExtractCausalLinks; opt == nil || *opt {
		t.Fatalf("causal option = %v, want explicitly disabled", opt)
	}

	on := NewMemoryStore("public")
	defaulted := &contextualExtraction{}
	if _, err := on.RetainWrite(ctx, RetainRequest{
		BankID:     "b1",
		Extraction: defaulted,
		Items:      []RetainItem{{Content: "Alice moved.", DocumentID: "doc-1"}},
	}); err != nil {
		t.Fatal(err)
	}
	// The contextual call is taken here because the retain supplies the ingest
	// clock as the extraction's reference time; the causal switch itself stays
	// nil, which is the reference's default (on).
	if len(defaulted.options) != 1 {
		t.Fatalf("contextual calls = %d, want one", len(defaulted.options))
	}
	if opt := defaulted.options[0].ExtractCausalLinks; opt != nil {
		t.Fatalf("causal option = %v, want the shipped default left alone", *opt)
	}
}
