package storage

import "testing"

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
