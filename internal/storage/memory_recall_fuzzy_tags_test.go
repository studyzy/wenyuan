package storage

import (
	"context"
	"testing"
)

// An exact-hit token is never widened to its similar neighbours: recall resolves
// the query's leaves against the bank's vocabulary first, and the token that is
// already a real tag resolves to just itself. Before that rewrite, the tag
// predicate scored each row's tags against the token, so a memory whose tag
// merely looked like the token came back too.
func TestRecallExactHitTokenDoesNotWiden(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1",
		RecallMatch{ID: "exact", Text: "alpha", Type: "world", Tags: []string{"name:alexandria"}},
		RecallMatch{ID: "near", Text: "alpha", Type: "world", Tags: []string{"name:alexandrio"}},
	)

	result, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		TagGroups: []TagGroup{{
			Resolve: TagResolveFuzzy, Match: TagsMatchAnyStrict,
			Tags: []string{"name:alexandria"},
		}},
	})
	if err != nil {
		t.Fatalf("recall: %v", err)
	}
	if ids := recallIDs(result); len(ids) != 1 || ids[0] != "exact" {
		t.Fatalf("recall = %v, want only the memory carrying the exact tag", ids)
	}
}

// A near-miss token resolves against the vocabulary, not against each row: with
// no vocabulary to fold against the token stays itself, so a memory tagged with
// a look-alike is not that scope. The old per-row reading scored similarity
// directly and matched the look-alike with no vocabulary involved at all.
func TestResolveRecallTagScopeEmptyVocabularyStaysUnsatisfiable(t *testing.T) {
	q := RecallQuery{TagGroups: []TagGroup{{
		Resolve: TagResolveFuzzy, Match: TagsMatchAnyStrict, Tags: []string{"typescropt"},
	}}}
	resolved, err := ResolveRecallTagScope(q, nil)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// The token expands to itself, so the rewritten leaf names the raw token and
	// stays satisfiable only by that literal tag.
	if !anyGroupMatches(resolved.TagGroups, []string{"typescropt"}) {
		t.Fatal("a token that resolves to nothing must come back as itself, not as an empty leaf")
	}
	if anyGroupMatches(resolved.TagGroups, []string{"typescript"}) {
		t.Fatal("a near-miss token must not match when the vocabulary is empty")
	}
}

// Fuzzy resolution keeps the caller's `all` semantics across the two backends:
// every token must find a spelling, and the lenient `all` still admits an
// untagged memory while the strict variant does not.
func TestResolveRecallTagScopeKeepsAllSemantics(t *testing.T) {
	vocabulary := []string{"user:alexandria", "user:alexandrio"}
	q := RecallQuery{TagGroups: []TagGroup{{
		Resolve: TagResolveFuzzy, Match: TagsMatchAllStrict,
		Tags: []string{"user:alexandria", "user:zzzz"},
	}}}
	resolved, err := ResolveRecallTagScope(q, vocabulary)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// "user:zzzz" resolves to itself, so no memory can carry one spelling of
	// every token.
	if anyGroupMatches(resolved.TagGroups, []string{"user:alexandria"}) {
		t.Fatal("an `all` leaf must not match when one token is unsatisfiable")
	}

	lenient := RecallQuery{TagGroups: []TagGroup{{
		Resolve: TagResolveFuzzy, Match: TagsMatchAll, Tags: []string{"user:alexandria"},
	}}}
	resolvedLenient, err := ResolveRecallTagScope(lenient, vocabulary)
	if err != nil {
		t.Fatalf("resolve lenient: %v", err)
	}
	if !anyGroupMatches(resolvedLenient.TagGroups, nil) {
		t.Fatal("the lenient `all` leaf must still admit an untagged memory")
	}
}
