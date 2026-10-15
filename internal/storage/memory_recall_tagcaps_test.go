package storage

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// A bank whose distinct tag count exceeds the cap is refused for a fuzzy
// request, before any expansion work: matching against a truncated vocabulary
// would silently change which memories match.
func TestValidateTagScopeVocabularyCap(t *testing.T) {
	vocab := make([]string, 0, MaxTagVocabulary+1)
	for i := 0; i <= MaxTagVocabulary; i++ {
		vocab = append(vocab, fmt.Sprintf("tag-%d", i))
	}
	q := RecallQuery{TagGroups: []TagGroup{{Tag: "alpha", Resolve: TagResolveFuzzy}}}
	err := ValidateTagScope(q, vocab)
	if !errors.Is(err, ErrTagVocabularyTooLarge) {
		t.Fatalf("oversized vocabulary error = %v, want ErrTagVocabularyTooLarge", err)
	}
	// The message is the reference's, so recall answers with the same text the
	// knowledge path does.
	if !strings.Contains(err.Error(), "distinct tags, above the 5000 that fuzzy tag matching can resolve against. Use resolve='exact'.") {
		t.Fatalf("vocabulary-cap message = %q, want the reference's wording", err.Error())
	}
	// At the cap, the same request is accepted.
	if err := ValidateTagScope(q, vocab[:MaxTagVocabulary]); err != nil {
		t.Fatalf("a vocabulary at the cap must be accepted: %v", err)
	}
	// An exact request never pays for the vocabulary read.
	if err := ValidateTagScope(RecallQuery{TagGroups: []TagGroup{{Tag: "alpha", Resolve: TagResolveExact}}}, vocab); err != nil {
		t.Fatalf("a non-fuzzy request must be accepted: %v", err)
	}
}

// An `exact` fuzzy leaf whose per-token expansions multiply past the branch
// ceiling is refused; the other match modes grow additively and are not capped.
func TestValidateTagScopeExactCombinationCap(t *testing.T) {
	// Each of the two tokens resolves to all seven vocabulary tags, so the
	// product is 7*7 = 49 branches, above the 32 ceiling. The values are long
	// enough that they stay similar once the "user:" namespace is stripped.
	vocab := []string{"user:alexandria", "user:alexandrio", "user:alexandrie", "user:alexandriu", "user:alexandrea", "user:alexandre", "user:alexandro"}
	leaf := TagGroup{Resolve: TagResolveFuzzy, Match: TagsMatchExact, Tags: []string{"user:alexandria", "user:alexandrio"}}
	err := ValidateTagScope(RecallQuery{TagGroups: []TagGroup{leaf}}, vocab)
	if !errors.Is(err, ErrTagExactCombinationsTooLarge) {
		t.Fatalf("oversized exact expansion error = %v, want ErrTagExactCombinationsTooLarge", err)
	}
	if !strings.Contains(err.Error(), "Narrow the tags, or use resolve='exact'.") {
		t.Fatalf("combination-cap message = %q, want the reference's wording", err.Error())
	}
	// A single token expands to seven branches and is accepted.
	single := TagGroup{Resolve: TagResolveFuzzy, Match: TagsMatchExact, Tags: []string{"user:alexandria"}}
	if err := ValidateTagScope(RecallQuery{TagGroups: []TagGroup{single}}, vocab); err != nil {
		t.Fatalf("a single-token exact leaf must be accepted: %v", err)
	}
	// The `any` mode unions the expansions instead of multiplying them.
	anyLeaf := leaf
	anyLeaf.Match = TagsMatchAny
	if err := ValidateTagScope(RecallQuery{TagGroups: []TagGroup{anyLeaf}}, vocab); err != nil {
		t.Fatalf("an `any` leaf must not be capped: %v", err)
	}
	// The cap is enforced inside nested groups too.
	nested := RecallQuery{TagGroups: []TagGroup{{Or: []TagGroup{leaf}}}}
	if err := ValidateTagScope(nested, vocab); !errors.Is(err, ErrTagExactCombinationsTooLarge) {
		t.Fatalf("nested oversized expansion error = %v, want ErrTagExactCombinationsTooLarge", err)
	}
}

// The in-process recall refuses an oversized fuzzy scope with the sentinel error
// rather than returning partial results.
func TestMemoryRecallRefusesOversizedTagScope(t *testing.T) {
	s := NewMemoryStore("public")
	tags := make([]string, 0, MaxTagVocabulary+1)
	for i := 0; i <= MaxTagVocabulary; i++ {
		tags = append(tags, fmt.Sprintf("tag-%d", i))
	}
	seedBank(t, s, "b1", RecallMatch{ID: "m1", Text: "alpha", Type: "world", Tags: tags})

	_, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		TagGroups: []TagGroup{{Tag: "alpha", Resolve: TagResolveFuzzy, Match: TagsMatchAny}},
	})
	if !errors.Is(err, ErrTagVocabularyTooLarge) {
		t.Fatalf("recall error = %v, want ErrTagVocabularyTooLarge", err)
	}
	// The same request without fuzzy resolution is unaffected.
	if _, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		TagGroups: []TagGroup{{Tag: "tag-1", Resolve: TagResolveExact, Match: TagsMatchAny}},
	}); err != nil {
		t.Fatalf("an exact request must still be answered: %v", err)
	}
}

// The in-process recall refuses an exact fuzzy leaf that expands past the branch
// ceiling.
func TestMemoryRecallRefusesCombinationExplosion(t *testing.T) {
	s := NewMemoryStore("public")
	seedBank(t, s, "b1", RecallMatch{
		ID: "m1", Text: "alpha", Type: "world",
		Tags: []string{"user:alexandria", "user:alexandrio", "user:alexandrie", "user:alexandriu", "user:alexandrea", "user:alexandre", "user:alexandro"},
	})
	_, err := s.Recall(context.Background(), RecallQuery{
		BankID: "b1", Query: "alpha",
		TagGroups: []TagGroup{{
			Resolve: TagResolveFuzzy, Match: TagsMatchExact,
			Tags: []string{"user:alexandria", "user:alexandrio"},
		}},
	})
	if !errors.Is(err, ErrTagExactCombinationsTooLarge) {
		t.Fatalf("recall error = %v, want ErrTagExactCombinationsTooLarge", err)
	}
}
