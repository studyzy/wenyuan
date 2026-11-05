package storage

import (
	"context"
	"reflect"
	"testing"
)

func TestParseCausalDescriptors(t *testing.T) {
	raw := []byte(`[
		{"from_unit_id":"u1","to_unit_id":"u2","link_type":"caused_by","weight":0.75},
		{"from_unit_id":"u3","to_unit_id":"u4","link_type":"causes"},
		{"from_unit_id":"","to_unit_id":"u5","link_type":"caused_by"},
		{"from_unit_id":"u6","to_unit_id":"u7","link_type":"temporal"},
		42,
		"text",
		null,
		{"from_unit_id":"u8","to_unit_id":"u9","link_type":"caused_by","weight":0}
	]`)
	want := []causalDescriptor{
		{FromUnitID: "u1", ToUnitID: "u2", LinkType: "caused_by", Weight: 0.75},
		{FromUnitID: "u3", ToUnitID: "u4", LinkType: "causes", Weight: 1.0},
		{FromUnitID: "u8", ToUnitID: "u9", LinkType: "caused_by", Weight: 1.0},
	}
	if got := parseCausalDescriptors(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("parsed = %+v, want %+v", got, want)
	}
}

func TestParseCausalDescriptorsRejectsMalformedInput(t *testing.T) {
	for _, raw := range [][]byte{nil, []byte("not json"), []byte("{}"), []byte(`[1, "x", null]`)} {
		if got := parseCausalDescriptors(raw); len(got) != 0 {
			t.Fatalf("raw %q produced %+v", raw, got)
		}
	}
}

func TestMarshalCausalDescriptorsRoundTrip(t *testing.T) {
	descriptors := []causalDescriptor{{FromUnitID: "u1", ToUnitID: "u2", LinkType: "caused_by", Weight: 0.5}}
	if got := parseCausalDescriptors(marshalCausalDescriptors(descriptors)); !reflect.DeepEqual(got, descriptors) {
		t.Fatalf("round trip = %+v, want %+v", got, descriptors)
	}
	if string(marshalCausalDescriptors(nil)) != "[]" {
		t.Fatal("an empty set must marshal to an empty array")
	}
}

// A curation move on the in-memory backend never removes causal edges, so a
// revert sees them again without rebuilding — the same outcome the PostgreSQL
// archive reaches from its descriptors.
func TestMemoryCausalLinksSurviveInvalidateRevert(t *testing.T) {
	store := NewMemoryStore("public")
	bank := "b1"
	content := "lost the job, then could not pay rent"
	extractor := &stubExtraction{facts: map[string][]ExtractedFact{
		content: {
			{Text: "They lost the job."},
			{Text: "They could not pay rent.", CausedByIndexes: []int{0}},
		},
	}}
	outcome, err := store.RetainWrite(context.Background(), RetainRequest{
		BankID:     bank,
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
	invalid := "invalidated"
	if _, err := store.UpdateMemory(context.Background(), bank, units[0], MemoryPatch{State: &invalid}); err != nil {
		t.Fatal(err)
	}
	valid := "valid"
	if _, err := store.UpdateMemory(context.Background(), bank, units[0], MemoryPatch{State: &valid}); err != nil {
		t.Fatal(err)
	}
	graph, err := store.Graph(context.Background(), GraphQuery{BankID: bank, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, edge := range graph.Edges {
		if edge.Type == string(LinkCausedBy) && edge.From == units[1] && edge.To == units[0] {
			return
		}
	}
	t.Fatalf("the causal edge did not survive the revert: %+v", graph.Edges)
}
