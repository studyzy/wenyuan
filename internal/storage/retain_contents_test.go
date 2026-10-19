package storage

import (
	"reflect"
	"testing"
	"time"
)

// The reference's perspective mapping: "assistant" and "experience" both mean
// the agent acted, "world" is objective, and an unexpected value falls back to
// the fact kind before defaulting to world.
func TestFactTypeOrDefault(t *testing.T) {
	cases := []struct {
		factType string
		factKind string
		want     MemoryUnitType
	}{
		{"world", "", TypeWorld},
		{"assistant", "", TypeExperience},
		{"experience", "", TypeExperience},
		{"", "", TypeWorld},
		{"assistant", "", TypeExperience},
		{"unexpected", "assistant", TypeExperience},
		{"unexpected", "conversation", TypeWorld},
	}
	for _, tc := range cases {
		if got := factTypeOrDefault(tc.factType, tc.factKind); got != tc.want {
			t.Errorf("factTypeOrDefault(%q, %q) = %q, want %q", tc.factType, tc.factKind, got, tc.want)
		}
	}
}

// An attachment is named by its position in the item (from one) or by its
// identifier; an unresolvable entry is dropped rather than linked blindly.
func TestFactAttachmentIDs(t *testing.T) {
	itemIDs := []string{"att-a", "att-b"}
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"position", []string{"2"}, []string{"att-b"}},
		{"identifier", []string{"att-a"}, []string{"att-a"}},
		{"mixed and deduplicated", []string{"1", "att-a", "2"}, []string{"att-a", "att-b"}},
		{"out of range dropped", []string{"3"}, nil},
		{"unknown identifier dropped", []string{"att-z"}, nil},
		{"blank dropped", []string{" "}, nil},
		{"none", nil, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := factAttachmentIDs(itemIDs, tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("resolved %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("resolved %v, want %v", got, tc.want)
				}
			}
		})
	}
}

// A fact that names an attachment adds it to the unit without dropping the
// item's own attachments.
func TestFactContentsCarriesFactAttachments(t *testing.T) {
	contents := factContents("b1", "doc-1", map[int]string{0: "chunk-1"}, RetainItem{
		AttachmentIDs: []string{"att-a", "att-b"},
	}, []ExtractedFact{{
		Text: "the chart shows growth", Type: "world", FromAttachments: []string{"2"},
	}}, nil)
	if len(contents) != 1 {
		t.Fatalf("contents = %d, want one unit", len(contents))
	}
	ids := contents[0].unit.AttachmentIDs
	if len(ids) != 2 || ids[0] != "att-a" || ids[1] != "att-b" {
		t.Fatalf("attachment ids = %v, want both the item's and the fact's", ids)
	}
}

// The in-batch merge's similarity is pg_trgm's: names are compared word by
// word, so a name reordered into two words shares every trigram even when the
// whole string does not.
func TestIntrabatchSimilarityIgnoresWordOrder(t *testing.T) {
	if got := entityNameTrigramSimilarity("Smith John", "John Smith"); got != 1 {
		t.Fatalf("similarity = %v, want 1 for the same words reordered", got)
	}
	if got := entityNameTrigramSimilarity("Alice", "Bob"); got >= 0.5 {
		t.Fatalf("similarity = %v, want a low value for unrelated names", got)
	}
}

// The word-by-word gate keeps two names that share a surname but not a given
// name apart, and the number rule keeps two numbered things apart.
func TestEntityTokensAreCompatible(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want bool
	}{
		{"the same words reordered agree", "Smith John", "John Smith", true},
		{"a shared surname is not enough", "Dr John Richardson", "Dr Jane Richardson", false},
		{"numbers the other lacks are two things", "Room 101", "Room 102", false},
		{"a subset number keeps the word check", "Boeing 737 MAX", "Boeing 737 MAX 8", true},
		{"single words are exempt", "Nick", "Nicolas", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := entityTokensAreCompatible(tc.a, tc.b); got != tc.want {
				t.Fatalf("entityTokensAreCompatible(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// Two names in one batch that meet the similarity bar are mapped onto one
// spelling, and the plainest spelling wins.
func TestIntrabatchCanonicalNamesMergesSimilarNames(t *testing.T) {
	entities := []RetainEntity{{Text: "Smith John"}, {Text: "John Smith"}}
	canonical := intrabatchCanonicalNames(entities, true, DefaultEntityIntrabatchMergeSimilarity)
	if got := canonical["smith john"]; got != "John Smith" {
		t.Fatalf("canonical = %q, want John Smith", got)
	}
	if got := canonical["john smith"]; got != "John Smith" {
		t.Fatalf("canonical = %q, want John Smith", got)
	}
}

// Names the caller wrote literally are not merged: a caller who listed two
// names as two gets two entities.
func TestIntrabatchCanonicalNamesSkipsLiteralNames(t *testing.T) {
	entities := []RetainEntity{{Text: "Smith John"}, {Text: "John Smith"}}
	if canonical := intrabatchCanonicalNames(entities, false, DefaultEntityIntrabatchMergeSimilarity); canonical != nil {
		t.Fatalf("canonical = %v, want none when resolution is off", canonical)
	}
}

// A pair below the configured similarity is left alone, which is what makes the
// threshold observable.
func TestIntrabatchCanonicalNamesRespectsTheThreshold(t *testing.T) {
	entities := []RetainEntity{{Text: "Smith John"}, {Text: "John Smith"}}
	// The pair scores 1, so a threshold just above 1 leaves it unmerged.
	canonical := intrabatchCanonicalNames(entities, true, 1.0001)
	if canonical["smith john"] == "John Smith" {
		t.Fatalf("canonical = %v, want the pair left unmerged above the threshold", canonical)
	}
}

// applyIntrabatchCanonical rewrites a mention to its cluster's spelling and
// leaves an unclustered one untouched.
func TestApplyIntrabatchCanonical(t *testing.T) {
	canonical := map[string]string{"john smith": "John Smith"}
	rewritten := applyIntrabatchCanonical(RetainEntity{Text: "John  Smith"}, canonical)
	if rewritten.Text != "John Smith" {
		t.Fatalf("text = %q, want the canonical spelling", rewritten.Text)
	}
	untouched := applyIntrabatchCanonical(RetainEntity{Text: "Alice"}, canonical)
	if untouched.Text != "Alice" {
		t.Fatalf("text = %q, want it left alone", untouched.Text)
	}
}

// A fact date arrives in whatever shape the model wrote it, and the reference's
// own reader accepts all of them: a bare date, a zone-less time (read as UTC), a
// space in place of the "T", and any fractional seconds. Parsing only the fully
// zoned RFC 3339 form dropped every one of those, leaving occurred_start NULL
// where the reference stored a value.
func TestParseFactTimeAcceptsModelDateShapes(t *testing.T) {
	cases := []struct {
		value string
		want  string
	}{
		{"2015-07-01", "2015-07-01T00:00:00Z"},
		{"2015-07-01T00:00:00", "2015-07-01T00:00:00Z"},
		{"2015-07-01 00:00:00", "2015-07-01T00:00:00Z"},
		{"2026-10-16T23:59:59", "2026-10-16T23:59:59Z"},
		{"2026-10-16T00:00:00.01+00:00", "2026-10-16T00:00:00.01Z"},
		{"2026-10-16T00:00:00Z", "2026-10-16T00:00:00Z"},
		{"2015-07-01T10:30+02:00", "2015-07-01T08:30:00Z"},
		{"", ""},
		{"N/A", ""},
		{"sometime", ""},
	}
	for _, tc := range cases {
		got := parseFactTime(tc.value)
		if tc.want == "" {
			if got != nil {
				t.Fatalf("parseFactTime(%q) = %v, want nil", tc.value, got)
			}
			continue
		}
		if got == nil {
			t.Fatalf("parseFactTime(%q) = nil, want %s", tc.value, tc.want)
		}
		if formatted := got.Format(time.RFC3339Nano); formatted != tc.want {
			t.Fatalf("parseFactTime(%q) = %s, want %s", tc.value, formatted, tc.want)
		}
	}
}

// A date the model wrote as "2015-07-01" reaches the unit's columns rather than
// being dropped on the way, and the fact's own date wins over the item's ingest
// clock for event_date, as it does in the reference.
func TestApplyFactDatesKeepsADateOnlyOccurrence(t *testing.T) {
	unit := MemoryUnit{}
	applyFactDates(&unit, ExtractedFact{OccurredStart: "2015-07-01", OccurredEnd: "2015-07-01"})
	if unit.OccurredStart == nil || unit.OccurredStart.Format(time.RFC3339) != "2015-07-01T00:00:00Z" {
		t.Fatalf("occurred_start = %v, want 2015-07-01T00:00:00Z", unit.OccurredStart)
	}
	if unit.OccurredEnd == nil || unit.OccurredEnd.Format(time.RFC3339) != "2015-07-01T00:00:00Z" {
		t.Fatalf("occurred_end = %v, want 2015-07-01T00:00:00Z", unit.OccurredEnd)
	}
	if unit.EventDate == nil || unit.EventDate.Format(time.RFC3339) != "2015-07-01T00:00:00Z" {
		t.Fatalf("event_date = %v, want the fact's own date", unit.EventDate)
	}
}

// The names a caller gave a write's attachments are recorded on the document's
// replayable parameters, which is what lets a reprocess restate them: the
// document record is replaced by every write, so a name that is not recorded is
// a name the next write loses. Nothing is recorded when no name was given, so a
// document retained without named attachments carries no key.
func TestRetainParamsRecordAttachmentFilenames(t *testing.T) {
	named := retainParamsFor(RetainItem{
		AttachmentFilenames: map[string]string{"abc123": "plan.txt"},
	})
	if !reflect.DeepEqual(named["attachment_filenames"], map[string]string{"abc123": "plan.txt"}) {
		t.Fatalf("attachment_filenames = %#v, want the caller's names", named["attachment_filenames"])
	}
	if _, present := retainParamsFor(RetainItem{})["attachment_filenames"]; present {
		t.Fatal("a write with no named attachment must carry no attachment_filenames key")
	}
}
