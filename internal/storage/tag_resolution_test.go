package storage

import (
	"errors"
	"strings"
	"testing"
)

// A token that is already a real tag is never widened to its neighbours: a
// vocabulary holding both the exact tag and a near one resolves the token only
// to the exact one.
func TestExpandTagTokenExactHitWinsOutright(t *testing.T) {
	got := expandTagToken("name:k8s", []string{"name:k8s", "name:k8"})
	if len(got) != 1 || got[0] != "name:k8s" {
		t.Fatalf("exact hit expansion = %v, want [name:k8s]", got)
	}
}

// A token with no vocabulary to resolve against comes back as itself, so the
// leaf stays unsatisfiable rather than dropping the filter.
func TestExpandTagTokenFallsBackToItself(t *testing.T) {
	if got := expandTagToken("name:the", nil); len(got) != 1 || got[0] != "name:the" {
		t.Fatalf("empty-vocabulary expansion = %v, want [name:the]", got)
	}
	if got := expandTagToken("name:zzzzzz", []string{"name:thee"}); len(got) != 1 || got[0] != "name:zzzzzz" {
		t.Fatalf("unresolvable expansion = %v, want [name:zzzzzz]", got)
	}
}

// Similarity is measured on the namespace-stripped value inside the same
// namespace, so a namespace's shared trigrams do not resolve every short tag to
// every other.
func TestExpandTagTokenStaysInItsNamespace(t *testing.T) {
	if got := expandTagToken("name:the", []string{"name:thee"}); len(got) != 1 || got[0] != "name:thee" {
		t.Fatalf("same-namespace near miss = %v, want [name:thee]", got)
	}
	if got := expandTagToken("name:the", []string{"run:thee"}); len(got) != 1 || got[0] != "name:the" {
		t.Fatalf("cross-namespace expansion = %v, want [name:the]", got)
	}
}

// Each match mode composes the expansion the way the reference does: `any` is
// one union, `all` is an AND of one union per token, and `exact` is an OR of
// the distinct-tag combinations the tokens admit.
func TestResolveTagGroupsComposesMatchModes(t *testing.T) {
	vocabulary := []string{"run:deply", "run:prod"}

	any, err := ResolveTagGroups([]TagGroup{{Resolve: TagResolveFuzzy, Match: TagsMatchAnyStrict, Tags: []string{"run:deply", "run:prod"}}}, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	if len(any) != 1 || any[0].Resolve != "" || any[0].Match != TagsMatchAnyStrict {
		t.Fatalf("any leaf = %+v", any)
	}
	if len(any[0].Tags) != 2 {
		t.Fatalf("any leaf is one union of both tokens: %+v", any[0])
	}

	all, err := ResolveTagGroups([]TagGroup{{Resolve: TagResolveFuzzy, Match: TagsMatchAllStrict, Tags: []string{"run:deply", "run:prod"}}}, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 1 || len(all[0].And) != 2 {
		t.Fatalf("all leaf must be an AND of one union per token: %+v", all)
	}
	for _, conjunct := range all[0].And {
		if conjunct.Match != TagsMatchAnyStrict || len(conjunct.Tags) != 1 {
			t.Fatalf("all-leaf conjunct = %+v", conjunct)
		}
	}

	exact, err := ResolveTagGroups([]TagGroup{{Resolve: TagResolveFuzzy, Match: TagsMatchExact, Tags: []string{"run:deply", "run:prod"}}}, vocabulary)
	if err != nil {
		t.Fatal(err)
	}
	// Both tokens resolve to exactly one tag each, so the expansion is a single
	// combination and the leaf stays flat rather than an OR of one branch.
	if len(exact) != 1 || exact[0].Match != TagsMatchExact || len(exact[0].Tags) != 2 {
		t.Fatalf("exact leaf = %+v", exact)
	}
}

// A fuzzy `exact` leaf whose expansion exceeds the branch ceiling is refused
// before it is enumerated, rather than truncating the scope silently.
func TestResolveTagGroupsRefusesOversizedExactExpansion(t *testing.T) {
	// Each token resolves to both near tags, so six tokens fan out to 2^6 = 64
	// combinations, past the ceiling of 32.
	vocabulary := []string{"name:alexandria", "name:alexandrio"}
	tokens := make([]string, 6)
	for i := range tokens {
		tokens[i] = "name:alexandri"
	}
	_, err := ResolveTagGroups([]TagGroup{{Resolve: TagResolveFuzzy, Match: TagsMatchExact, Tags: tokens}}, vocabulary)
	if err == nil || !strings.Contains(err.Error(), "candidate scopes") {
		t.Fatalf("oversized exact expansion error = %v", err)
	}
}

// A filter with no fuzzy leaf is returned untouched, so an exact request reads
// no vocabulary.
func TestResolveTagGroupsLeavesExactFiltersAlone(t *testing.T) {
	in := []TagGroup{{Tags: []string{"team:blue"}, Match: TagsMatchAllStrict}}
	out, err := ResolveTagGroups(in, []string{"team:blue"})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) != 1 || out[0].Match != TagsMatchAllStrict || len(out[0].Tags) != 1 {
		t.Fatalf("exact filter changed: %+v", out)
	}
	if NeedsTagResolution(in) {
		t.Fatal("an exact filter must not need resolution")
	}
	if !NeedsTagResolution([]TagGroup{{Resolve: TagResolveFuzzy, Tags: []string{"x"}}}) {
		t.Fatal("a fuzzy leaf must need resolution")
	}
}

// The vocabulary cap is enforced by the caller against the read vocabulary; the
// resolver itself only reports the exact-combination cap, and never as a bare
// generic error it would be mistaken for a server fault.
func TestResolveTagGroupsErrorIsDistinct(t *testing.T) {
	if _, err := ResolveTagGroups([]TagGroup{{Resolve: TagResolveFuzzy, Match: TagsMatchExact, Tags: []string{"a"}}}, nil); err != nil {
		if errors.Is(err, ErrTagVocabularyTooLarge) {
			t.Fatalf("resolver must not raise the vocabulary cap: %v", err)
		}
	}
}
