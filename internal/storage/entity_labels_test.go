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
