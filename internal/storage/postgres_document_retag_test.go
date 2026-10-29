package storage

import "testing"

// retagCoSourceIDs picks the surviving co-sources of the observations a retag
// swept: every source the deleted observations carried other than a unit the
// update itself moved, deduplicated in first-seen order. The UPDATE that clears
// the marker guards the fact types, so the ids here need no filtering.
func TestRetagCoSourceIDs(t *testing.T) {
	removed := []RemovedObservation{
		{ObservationID: "obs-1", SourceFactIDs: []string{"moved-1", "co-1", "co-2"}},
		// The second observation shares co-1 and names co-1 again itself.
		{ObservationID: "obs-2", SourceFactIDs: []string{"co-1", "co-1", "co-3"}},
	}
	got := retagCoSourceIDs(removed, []string{"moved-1"})
	want := []string{"co-1", "co-2", "co-3"}
	if len(got) != len(want) {
		t.Fatalf("co-sources = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("co-sources = %v, want %v", got, want)
		}
	}
	// A source that is itself a moved unit is requeued by the moved-unit sweep,
	// not as a co-source.
	if got := retagCoSourceIDs(removed, []string{"moved-1", "co-2"}); len(got) != 2 || got[0] != "co-1" || got[1] != "co-3" {
		t.Fatalf("co-sources with a moved one excluded = %v, want [co-1 co-3]", got)
	}
}
