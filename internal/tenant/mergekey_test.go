package tenant

import "testing"

// TestNormalizeFoldsOnlyWhitespace pins what two facts must differ by to stay
// two facts. Case and punctuation are statement-level differences and are kept;
// spacing is not a difference at all.
func TestNormalizeFoldsOnlyWhitespace(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"trim", "  alpha beta  ", "alpha beta"},
		{"collapse internal", "alpha    beta", "alpha beta"},
		{"tabs and newlines", "alpha\tbeta\ngamma", "alpha beta gamma"},
		{"already normal", "alpha beta", "alpha beta"},
		{"empty", "   ", ""},
		{"case kept", "Alpha Beta", "Alpha Beta"},
		{"punctuation kept", "alpha, beta.", "alpha, beta."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Normalize(tc.in); got != tc.want {
				t.Fatalf("Normalize(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestMergeKeyIgnoresSpacingOnly pins the incremental-merge guarantee: a fact
// whose wording did not change keeps its row, and so its id and its
// observations, even if it was re-read with different whitespace.
func TestMergeKeyIgnoresSpacingOnly(t *testing.T) {
	base := MergeKey("The deploy uses port 15432.")
	for _, equivalent := range []string{
		"  The deploy uses port 15432.",
		"The deploy uses port 15432.  ",
		"The  deploy uses   port 15432.",
		"The deploy uses port 15432.\n",
	} {
		if got := MergeKey(equivalent); got != base {
			t.Fatalf("MergeKey(%q) = %q, want the same key as %q", equivalent, got, base)
		}
	}
	different := []string{
		"The deploy uses port 15433.",
		"the deploy uses port 15432.",
		"The deploy uses port 15432",
	}
	for _, other := range different {
		if got := MergeKey(other); got == base {
			t.Fatalf("MergeKey(%q) = MergeKey(%q); they are different facts", other, base)
		}
	}
}

func TestMergeKeyIsStableHex(t *testing.T) {
	key := MergeKey("alpha")
	if len(key) != 64 {
		t.Fatalf("MergeKey length = %d, want 64 hex characters", len(key))
	}
	// Pinned so a change to the normalization can never silently re-key every
	// stored team fact.
	const pinned = "8ed3f6ad685b959ead7022518e1af76cd816f8e8ec7ccdda1ed4018e8f2223f8"
	if key != pinned {
		t.Fatalf("MergeKey(%q) = %q, want %q", "alpha", key, pinned)
	}
	if MergeKey("alpha") != key {
		t.Fatal("MergeKey is not deterministic")
	}
}

func TestMergeKeyOfDroppedSentinelIsNotTheSentinel(t *testing.T) {
	// DroppedMergeKey is a marker, not a key of any text: a fact whose normalized
	// text happened to be the marker's spelling must not collide with it.
	if MergeKey(DroppedMergeKey) == DroppedMergeKey {
		t.Fatal("a real fact's key must never equal the dropped marker")
	}
}
