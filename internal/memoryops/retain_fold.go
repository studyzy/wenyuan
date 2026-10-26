package memoryops

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
)

// RetainExtractionBudgetBytes bounds the text one fold reaches extraction with.
// Consecutive items are folded only while their combined text stays within it,
// so one execution's prompt does not grow unbounded. Each item is still chunked
// before it reaches the model, and an item's own text is never truncated: the
// stored document body is what a later reprocess re-reads, so shortening it
// would silently change what re-extraction can find.
const RetainExtractionBudgetBytes = 32 * 1024

// RetainFoldItemBytes is the largest item text that may join a fold. Only small
// consecutive items are folded, so a fold never merges two documents that each
// wanted their own extraction call.
const RetainFoldItemBytes = 2 * 1024

// foldSeparator joins the texts of folded items that are not JSON arrays. A
// blank line is the chunker's preferred boundary, so a fold keeps the items'
// prose in separate paragraphs.
const foldSeparator = "\n\n"

// FoldRetainItems bounds the text that reaches extraction: consecutive items
// small enough to share one extraction call are folded into a single item.
// Items are only folded when everything but their content and references is
// identical, so a fold never merges items with different tags, timestamps or
// document identity — those differences are exactly what makes them separate
// memories. Item text is never truncated; folding only joins what fits.
func FoldRetainItems(items []RetainItem) []RetainItem {
	out := make([]RetainItem, 0, len(items))
	for _, item := range items {
		if n := len(out); n > 0 && foldable(out[n-1], item) {
			out[n-1] = mergeFoldedItems(out[n-1], item)
			continue
		}
		out = append(out, item)
	}
	return out
}

// foldable reports whether two items may share one extraction call. The content
// and the reference lists are allowed to differ; everything that would make the
// merged item lie about one of its halves is not.
func foldable(prev, next RetainItem) bool {
	// An item carrying inline images keeps its own extraction call: folding
	// would merge two documents' pictures into one prompt with no way to tell
	// which placeholder belonged to which.
	if len(prev.Attachments) > 0 || len(next.Attachments) > 0 {
		return false
	}
	prevText, nextText := foldText(prev), foldText(next)
	if prevText == "" || nextText == "" {
		return false
	}
	if len(prevText) > RetainFoldItemBytes || len(nextText) > RetainFoldItemBytes {
		return false
	}
	if len(prevText)+len(foldSeparator)+len(nextText) > RetainExtractionBudgetBytes {
		return false
	}
	if prev.DocumentID != next.DocumentID || prev.Context != next.Context {
		return false
	}
	if prev.Strategy != next.Strategy || prev.UpdateMode != next.UpdateMode {
		return false
	}
	if !reflect.DeepEqual(prev.Metadata, next.Metadata) {
		return false
	}
	if !reflect.DeepEqual(prev.Timestamp, next.Timestamp) {
		return false
	}
	if !reflect.DeepEqual(prev.ResolveEntities, next.ResolveEntities) {
		return false
	}
	if !bytes.Equal(prev.ObservationScopes.RawJSON(), next.ObservationScopes.RawJSON()) {
		return false
	}
	return sameTags(prev.Tags, next.Tags)
}

// mergeFoldedItems appends next's text to a fold and unions the reference
// lists, so no entity or attachment a caller supplied is lost in the fold.
//
// Content is joined the one way a document's parts are joined: two JSON arrays
// merge into a single array, so a folded conversation stays one array rather
// than becoming two arrays separated by a newline. Anything else is joined by a
// blank line, the chunker's preferred prose boundary.
func mergeFoldedItems(prev, next RetainItem) RetainItem {
	prevText, nextText := foldText(prev), foldText(next)
	merged := ""
	if combined, ok := mergeJSONArrayParts(prevText, nextText); ok {
		merged = combined
	} else {
		merged = prevText + foldSeparator + nextText
	}
	// The blocks stay identifiable so each is still cut and numbered on its own
	// boundaries: the join only decides the document body.
	prev.Parts = append(foldParts(prev), nextText)
	prev.Content = Content{Text: merged}
	prev.Entities = mergeEntities(prev.Entities, next.Entities)
	prev.AttachmentIDs = mergeStrings(prev.AttachmentIDs, next.AttachmentIDs)
	return prev
}

// foldParts returns the content blocks a fold has accumulated, so appending
// another item's text keeps each block's own boundaries. An item that carries
// no Parts is one block: its canonical text.
func foldParts(item RetainItem) []string {
	if len(item.Parts) > 0 {
		return item.Parts
	}
	return []string{foldText(item)}
}

// foldText is the item's text as a fold measures and joins it. A text this build
// assembled itself — an upload's converted document — is already canonical, so it
// is taken as it stands; canonicalizing it here would apply the placeholder
// scrub to a reference the upload minted for its own attachment.
func foldText(item RetainItem) string {
	if item.textIsCanonical && !item.Content.IsList {
		return item.Content.Text
	}
	return item.Content.Flatten()
}

// mergeJSONArrayParts concatenates JSON array bodies into one array, preserving
// each element. It reports false unless both parts are a JSON array, so a fold
// that is not array-to-array keeps its prose join. It mirrors the storage
// layer's document-body join, since a fold's text becomes the document's body.
func mergeJSONArrayParts(prev, next string) (string, bool) {
	var prevArray, nextArray []json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(prev)), &prevArray); err != nil {
		return "", false
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(next)), &nextArray); err != nil {
		return "", false
	}
	combined := append(append(make([]json.RawMessage, 0, len(prevArray)+len(nextArray)), prevArray...), nextArray...)
	encoded, err := json.Marshal(combined)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

// sameTags reports whether two tag lists carry the same set of tags, so a fold
// does not attach one item's tags to another's text.
func sameTags(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	remaining := make(map[string]int, len(a))
	for _, tag := range a {
		remaining[tag]++
	}
	for _, tag := range b {
		remaining[tag]--
		if remaining[tag] < 0 {
			return false
		}
	}
	return true
}

// mergeEntities unions two entity lists, preserving order and dropping repeats
// by name and kind.
func mergeEntities(base, extra []EntityInput) []EntityInput {
	out := append([]EntityInput(nil), base...)
	for _, entity := range extra {
		seen := false
		for _, existing := range out {
			if existing.Text == entity.Text && existing.Type == entity.Type {
				seen = true
				break
			}
		}
		if !seen {
			out = append(out, entity)
		}
	}
	return out
}

// mergeStrings unions two string lists, preserving order and dropping repeats.
func mergeStrings(base, extra []string) []string {
	out := append([]string(nil), base...)
	for _, value := range extra {
		if !containsString(out, value) {
			out = append(out, value)
		}
	}
	return out
}

// containsString reports whether a string list carries the value.
func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
