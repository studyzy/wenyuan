package memoryops

import (
	"strings"
	"testing"
)

// B1.17: consecutive small items that agree on everything but their text are
// folded into one extraction call.
func TestFoldRetainItemsFoldsSmallConsecutiveItems(t *testing.T) {
	items := []RetainItem{
		{Content: Content{Text: "first"}},
		{Content: Content{Text: "second"}},
		{Content: Content{Text: "third"}},
	}
	folded := FoldRetainItems(items)
	if len(folded) != 1 {
		t.Fatalf("folded items = %d, want 1", len(folded))
	}
	if folded[0].Content.Flatten() != "first\n\nsecond\n\nthird" {
		t.Fatalf("folded text = %q", folded[0].Content.Flatten())
	}
}

// B1.17: items that differ in tags are not folded, because merging them would
// attach one item's tags to another's text.
func TestFoldRetainItemsKeepsDifferingTagsApart(t *testing.T) {
	items := []RetainItem{
		{Content: Content{Text: "first"}, Tags: TagList{"a"}},
		{Content: Content{Text: "second"}, Tags: TagList{"b"}},
	}
	if folded := FoldRetainItems(items); len(folded) != 2 {
		t.Fatalf("folded items = %d, want 2", len(folded))
	}
}

// B1.17: a fold stops at the byte budget rather than merging an unbounded run,
// so no single extraction call exceeds the budget.
func TestFoldRetainItemsRespectsByteBudget(t *testing.T) {
	items := make([]RetainItem, 0, 20)
	for range 20 {
		items = append(items, RetainItem{Content: Content{Text: strings.Repeat("a", RetainFoldItemBytes)}})
	}
	folded := FoldRetainItems(items)
	if len(folded) < 2 {
		t.Fatalf("folded items = %d, want the run split at the byte budget", len(folded))
	}
	for _, item := range folded {
		if got := len(item.Content.Flatten()); got > RetainExtractionBudgetBytes {
			t.Fatalf("fold group = %d bytes, want <= %d", got, RetainExtractionBudgetBytes)
		}
	}
}

// An item larger than the extraction budget is left whole: its stored text is
// what a later reprocess re-reads, so the fold must not shorten it. The chunker
// bounds what one model call sees instead.
func TestFoldRetainItemsKeepsOversizedItemWhole(t *testing.T) {
	oversized := strings.Repeat("x", RetainExtractionBudgetBytes+500)
	item := RetainItem{Content: Content{Text: oversized}}
	folded := FoldRetainItems([]RetainItem{item})
	if len(folded) != 1 {
		t.Fatalf("folded items = %d, want 1", len(folded))
	}
	if got := folded[0].Content.Flatten(); got != oversized {
		t.Fatalf("item text = %d bytes, want it kept whole at %d", len(got), len(oversized))
	}
	if folded[0].Metadata["extraction_truncated"] == true {
		t.Fatalf("the item must not be marked truncated: %+v", folded[0].Metadata)
	}
}

// Two JSON-array items fold into one array, so a conversation stays one array
// rather than two arrays separated by a newline.
func TestFoldRetainItemsMergesJSONArrays(t *testing.T) {
	items := []RetainItem{
		{Content: Content{Text: `[{"role":"user","content":"one"}]`}},
		{Content: Content{Text: `[{"role":"user","content":"two"}]`}},
	}
	folded := FoldRetainItems(items)
	if len(folded) != 1 {
		t.Fatalf("folded items = %d, want 1", len(folded))
	}
	want := `[{"role":"user","content":"one"},{"role":"user","content":"two"}]`
	if got := folded[0].Content.Flatten(); got != want {
		t.Fatalf("folded JSON = %q, want %q", got, want)
	}
}

// B1.17: a fold unions the reference lists so no entity or attachment is lost.
func TestFoldRetainItemsUnionsReferences(t *testing.T) {
	items := []RetainItem{
		{Content: Content{Text: "first"}, Entities: EntityInputList{{Text: "Alice"}}, AttachmentIDs: []string{"aaaaaaaaaaaa"}},
		{Content: Content{Text: "second"}, Entities: EntityInputList{{Text: "Bob"}}, AttachmentIDs: []string{"bbbbbbbbbbbb"}},
	}
	folded := FoldRetainItems(items)
	if len(folded) != 1 {
		t.Fatalf("folded items = %d, want 1", len(folded))
	}
	if names := folded[0].Entities.Texts(); len(names) != 2 {
		t.Fatalf("entities = %v, want Alice and Bob", names)
	}
	if len(folded[0].AttachmentIDs) != 2 {
		t.Fatalf("attachment ids = %v, want both", folded[0].AttachmentIDs)
	}
}

// B1.17: the retain path applies the fold, so a batch of small items reaches
// storage as one extraction unit.
func TestRetainRouteFoldsSmallItems(t *testing.T) {
	req := RetainRequest{Items: []RetainItem{
		{Content: Content{Text: "one"}},
		{Content: Content{Text: "two"}},
	}}
	stored := toStorageRetain("b1", req)
	if len(stored.Items) != 1 {
		t.Fatalf("stored items = %d, want 1", len(stored.Items))
	}
	if stored.Items[0].Content != "one\n\ntwo" {
		t.Fatalf("stored text = %q", stored.Items[0].Content)
	}
}
