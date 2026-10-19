package storage

import (
	"math"
	"testing"
)

// Fuzzy tag matching scores a token against the bank's vocabulary with pg_trgm's
// `similarity()`, which the reference computes in memory
// (`engine/trigram.py: trigram_similarity`) and shares with entity resolution.
// The scores below are the ones the reference's `tag_resolution.py` module
// docstring states outright, so they pin this package to the reference's own
// published numbers rather than to a reimplementation's intuition.
//
// The measure has to be pg_trgm's per-word one. pg_trgm splits a string into
// maximal alphanumeric runs and pads every word separately, so a space or a
// punctuation mark between two words costs no grams; padding the whole string
// instead spends grams on the separators, which lowers every score that has one
// and pushes tags below the 0.45 floor the reference resolves at.
func TestTrigramJaccardMatchesReferencePgTrgmScores(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want float64
	}{
		// The reference's own docstring numbers.
		{"one typo in a long tag", "typescropt", "typescript", 0.57},
		{"one typo in a short tag", "mongp", "mongo", 0.50},
		{"one typo in a very short tag", "moongoose", "mongoose", 0.73},
		// The reference quotes `user:alice`/`user:alicia` at 0.64 as the whole
		// namespaced pair. Resolution deliberately measures the value alone
		// (`tag_resolution._split_namespace`), and that is what scores 0.444 —
		// below the floor, so a `user:` token never resolves to a neighbour it
		// would have reached had the namespace's shared grams been counted.
		{"whole namespaced pair", "user:alice", "user:alicia", 0.64},
		// The pairs the floor is calibrated to keep apart.
		{"too short to survive one edit", "kakfa", "kafka", 0.20},
		{"one letter apart", "mango", "mongo", 0.33},
		{"digits are not letters", "k9s", "k8s", 0.14},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := trigramJaccard(tc.a, tc.b)
			// The reference quotes two decimal places, so compare at that
			// precision: pg_trgm's exact rational is compared the same way.
			if math.Abs(got-tc.want) > 0.005 {
				t.Fatalf("trigramJaccard(%q, %q) = %.4f, want %.2f", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

// A separator between words costs nothing under pg_trgm, which pads each word
// separately. A whole-string measure instead charges for the space and reports
// well under 1.0 for two tags that are the same words joined differently — so a
// vocabulary holding `team-alpha` would not resolve the token `team alpha` even
// though the reference resolves it at the ceiling.
func TestTrigramJaccardIgnoresSeparatorsBetweenWords(t *testing.T) {
	for _, pair := range [][2]string{
		{"team alpha", "team-alpha"},
		{"feature work", "feature_work"},
		{"nightly build", "nightly.build"},
		{"hello world", "hello/world"},
	} {
		if got := trigramJaccard(pair[0], pair[1]); got != 1.0 {
			t.Fatalf("trigramJaccard(%q, %q) = %v, want 1.0 (separators cost no grams)",
				pair[0], pair[1], got)
		}
	}
}

// The floor is what decides whether a token resolves at all, so the two cases
// that straddle it are pinned by *similarTag*'s verdict, not only by the score:
// `mongp` resolves to `mongo` the way the reference documents, while `mango`
// stays apart from `mongo`.
func TestSimilarTagResolvesAtReferenceFloor(t *testing.T) {
	cases := []struct {
		token, tag string
		want       bool
	}{
		{"mongp", "mongo", true},
		{"typescropt", "typescript", true},
		{"moongoose", "mongoose", true},
		{"mango", "mongo", false},
		{"kakfa", "kafka", false},
		{"k9s", "k8s", false},
		// Namespaces never match across each other even at similarity 1.0.
		{"name:k8s", "scope:k8s", false},
	}
	for _, tc := range cases {
		if got := similarTag(tc.token, tc.tag); got != tc.want {
			t.Fatalf("similarTag(%q, %q) = %v, want %v", tc.token, tc.tag, got, tc.want)
		}
	}
}

// Resolution end to end: a vocabulary holding the hyphenated spelling answers a
// token written with a space, which is the case a whole-string measure drops.
func TestResolveTagGroupsMatchesSpacedTokenAgainstHyphenatedTag(t *testing.T) {
	resolved, err := ResolveTagGroups([]TagGroup{{
		Tags: []string{"team alpha"}, Match: TagsMatchAnyStrict, Resolve: TagResolveFuzzy,
	}}, []string{"team-alpha", "unrelated"})
	if err != nil {
		t.Fatalf("ResolveTagGroups: %v", err)
	}
	if len(resolved) != 1 {
		t.Fatalf("resolved %d groups, want 1", len(resolved))
	}
	if got := resolved[0].Tags; len(got) != 1 || got[0] != "team-alpha" {
		t.Fatalf("resolved tags = %v, want [team-alpha]", got)
	}
}
