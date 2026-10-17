package storage

import (
	"context"
	"testing"

	"github.com/studyzy/wenyuan/internal/entitylabels"
)

// stubLabelExtractor returns fixed facts, implementing only the contextual
// extraction boundary so the request's vocabulary reaches it.
type stubLabelExtractor struct {
	facts []ExtractedFact
}

func (s stubLabelExtractor) Extract(context.Context, string) ([]ExtractedFact, error) {
	return s.facts, nil
}

func (s stubLabelExtractor) ExtractWithOptions(context.Context, string, FactExtractionOptions) ([]ExtractedFact, error) {
	return s.facts, nil
}

func labelVocabulary(t *testing.T) *entitylabels.Vocabulary {
	t.Helper()
	vocab, err := entitylabels.Parse([]any{
		map[string]any{"key": "topic", "type": "text", "tag": true},
		map[string]any{
			"key":    "matter",
			"type":   "value",
			"values": []any{map[string]any{"value": "day"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return vocab
}

// A label becomes an entity named key:value, and a group flagged tag:true also
// lands on the unit's tags, through the one entity path both backends share.
func TestRetainProjectsLabelsToEntitiesAndTags(t *testing.T) {
	store := NewMemoryStore("public")
	vocab := labelVocabulary(t)
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Vocabulary: vocab,
		Extraction: stubLabelExtractor{facts: []ExtractedFact{{
			Text:     "Alice studies algorithms on a given day.",
			Entities: []RetainEntity{{Text: "Alice"}},
			Labels:   []string{"topic:algorithms", "matter:day"},
		}}},
		Items: []RetainItem{{Content: "ignored by the stub", DocumentID: "doc-1"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(outcome.ItemUnitIDs) != 1 || len(outcome.ItemUnitIDs[0]) != 1 {
		t.Fatalf("unit ids = %+v, want one unit", outcome.ItemUnitIDs)
	}
	unitID := outcome.ItemUnitIDs[0][0]
	unit, err := store.GetMemory(context.TODO(), "b1", unitID)
	if err != nil {
		t.Fatal(err)
	}
	if !containsString(unit.Tags, "topic:algorithms") {
		t.Fatalf("tag group's label missing from tags: %v", unit.Tags)
	}
	if containsString(unit.Tags, "matter:day") {
		t.Fatalf("a non-tag group must not become a tag: %v", unit.Tags)
	}

	names := map[string]bool{}
	for _, entityID := range store.unitEntities[unitID] {
		names[store.entities[entityID].CanonicalName] = true
	}
	for _, want := range []string{"Alice", "topic:algorithms", "matter:day"} {
		if !names[want] {
			t.Fatalf("entity %q missing; got %v", want, names)
		}
	}
}

// Without a vocabulary the labels a fact may carry are not projected, so the
// stored tags are exactly the caller's.
func TestRetainWithoutVocabularyLeavesTagsAlone(t *testing.T) {
	store := NewMemoryStore("public")
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID: "b1",
		Extraction: stubLabelExtractor{facts: []ExtractedFact{{
			Text:   "A fact.",
			Labels: []string{"topic:algorithms"},
		}}},
		Items: []RetainItem{{Content: "ignored", DocumentID: "doc-1", Tags: []string{"caller-tag"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unit, err := store.GetMemory(context.TODO(), "b1", outcome.ItemUnitIDs[0][0])
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.Tags) != 1 || unit.Tags[0] != "caller-tag" {
		t.Fatalf("tags = %v, want only the caller's", unit.Tags)
	}
}

// A retag replaces only the document-derived tags: the label-derived tag a unit
// carries (from its own entities, at extraction) is re-appended, so a document
// tag rename neither drops it nor duplicates it.
func TestRetagKeepsLabelDerivedTags(t *testing.T) {
	store := NewMemoryStore("public")
	vocab := labelVocabulary(t)
	outcome, err := store.RetainWrite(context.TODO(), RetainRequest{
		BankID:     "b1",
		Vocabulary: vocab,
		Extraction: stubLabelExtractor{facts: []ExtractedFact{{
			Text:   "Alice studies algorithms on a given day.",
			Labels: []string{"topic:algorithms"},
		}}},
		Items: []RetainItem{{Content: "ignored", DocumentID: "doc-1", Tags: []string{"work"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	unitID := outcome.ItemUnitIDs[0][0]

	if _, err := store.RetagDocumentMemories(context.TODO(), "b1", "doc-1",
		[]string{"work"}, []string{"team-a"}, map[string]bool{"topic": true}); err != nil {
		t.Fatal(err)
	}
	unit, err := store.GetMemory(context.TODO(), "b1", unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.Tags) != 2 || unit.Tags[0] != "team-a" || unit.Tags[1] != "topic:algorithms" {
		t.Fatalf("tags after retag = %v, want [team-a topic:algorithms]", unit.Tags)
	}

	// With no label keys the retag replaces every tag, the pre-existing shape.
	if _, err := store.RetagDocumentMemories(context.TODO(), "b1", "doc-1",
		[]string{"team-a"}, []string{"work"}, nil); err != nil {
		t.Fatal(err)
	}
	unit, err = store.GetMemory(context.TODO(), "b1", unitID)
	if err != nil {
		t.Fatal(err)
	}
	if len(unit.Tags) != 1 || unit.Tags[0] != "work" {
		t.Fatalf("tags without label keys = %v, want [work]", unit.Tags)
	}
}

// The document read counts the units written from it, straight off the mirror
// rather than a listing window.
func TestGetDocumentCountsItsUnits(t *testing.T) {
	store := NewMemoryStore("public")
	ctx := context.Background()
	if _, err := store.CreateBank(ctx, Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutDocument(ctx, "b1", "doc-1", Document{OriginalText: "text"}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"u1", "u2"} {
		if _, err := store.UpsertUnit(ctx, MemoryUnit{
			ID: id, BankID: "b1", Text: "a fact", DocumentID: "doc-1", FactType: TypeWorld,
		}); err != nil {
			t.Fatal(err)
		}
	}
	doc, err := store.GetDocument(ctx, "b1", "doc-1")
	if err != nil {
		t.Fatal(err)
	}
	if doc.MemoryUnitCount != 2 {
		t.Fatalf("memory_unit_count = %d, want 2", doc.MemoryUnitCount)
	}
}
