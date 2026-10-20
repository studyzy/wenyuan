package storage

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

// candidateAt builds a candidate with the given per-arm ranks.
func candidateAt(id string, ranks map[string]int) *recallCandidate {
	return &recallCandidate{match: RecallMatch{ID: id}, armRanks: ranks, armScores: map[string]float64{}}
}

// Interleave fusion takes every arm's first hit, then every arm's second, in arm
// priority order — so a candidate that is best in one arm but absent from the
// others is not averaged down below the budget cutoff.
func TestInterleaveFusionRoundRobinsArms(t *testing.T) {
	arms := []string{ArmSemantic, ArmKeyword}
	semanticFirst := candidateAt("s1", map[string]int{ArmSemantic: 1})
	semanticSecond := candidateAt("s2", map[string]int{ArmSemantic: 2})
	keywordFirst := candidateAt("k1", map[string]int{ArmKeyword: 1, ArmSemantic: 9})

	ordered := interleaveFusion([]*recallCandidate{semanticSecond, keywordFirst, semanticFirst}, arms)
	want := []string{"s1", "k1", "s2"}
	if len(ordered) != len(want) {
		t.Fatalf("ordered %d candidates, want %d", len(ordered), len(want))
	}
	for i, id := range want {
		if ordered[i].match.ID != id {
			t.Fatalf("order = %v", []string{ordered[0].match.ID, ordered[1].match.ID, ordered[2].match.ID})
		}
	}
}

// A candidate no arm ranked is still returned, at the end rather than dropped.
func TestInterleaveFusionKeepsUnrankedCandidates(t *testing.T) {
	arms := []string{ArmSemantic}
	ranked := candidateAt("s1", map[string]int{ArmSemantic: 1})
	unranked := candidateAt("u1", nil)
	ordered := interleaveFusion([]*recallCandidate{unranked, ranked}, arms)
	if len(ordered) != 2 {
		t.Fatalf("ordered %d candidates, want both", len(ordered))
	}
	if ordered[0].match.ID != "s1" || ordered[1].match.ID != "u1" {
		t.Fatalf("order = %s, %s", ordered[0].match.ID, ordered[1].match.ID)
	}
}

// The per-source cap truncates one arm's list and leaves the others alone.
func TestCapArm(t *testing.T) {
	hits := []recallHit{{}, {}, {}}
	if got := capArm(2, hits); len(got) != 2 {
		t.Fatalf("cap 2 kept %d hits", len(got))
	}
	if got := capArm(0, hits); len(got) != 3 {
		t.Fatalf("a disabled cap kept %d hits", len(got))
	}
	if got := capArm(10, hits); len(got) != 3 {
		t.Fatalf("a cap above the length kept %d hits", len(got))
	}
}

// An arm's floor is applied before fusion, so a hit that fails it never enters
// the pool and cannot displace a result that clears every floor.
func TestPrepareArmAppliesFloorBeforeCap(t *testing.T) {
	hits := []recallHit{
		{score: 0.9, match: RecallMatch{ID: "a"}},
		{score: 0.1, match: RecallMatch{ID: "b"}},
		{score: 0.8, match: RecallMatch{ID: "c"}},
	}
	q := RecallQuery{MinScores: map[string]float64{ArmSemantic: 0.5}, PerSourceCap: 2}
	kept := prepareArm(q, ArmSemantic, hits)
	if len(kept) != 2 || kept[0].match.ID != "a" || kept[1].match.ID != "c" {
		t.Fatalf("kept %+v, want the two hits above the floor", kept)
	}
	// A floor on another arm does not affect this one.
	if got := prepareArm(RecallQuery{MinScores: map[string]float64{ArmKeyword: 0.9}}, ArmSemantic, hits); len(got) != 3 {
		t.Fatalf("another arm's floor removed %d hits", 3-len(got))
	}
	// A zero floor is "unset", not "keep nothing".
	if got := prepareArm(RecallQuery{MinScores: map[string]float64{ArmSemantic: 0}}, ArmSemantic, hits); len(got) != 3 {
		t.Fatalf("a zero floor kept %d hits", len(got))
	}
}

// The Postgres path renders compound tag groups into the predicate instead of
// silently ignoring them.
//
// Every comparison casts the column to text[]: the reference schema declares the
// tag columns `character varying[]`, and PostgreSQL has no operator for
// `character varying[] @> text[]`, so the uncast form fails at the server.
func TestRecallTagPredicateRendersGroups(t *testing.T) {
	// A flat any-match includes untagged rows, matching the in-process evaluator.
	if got := recallTagPredicate(RecallQuery{Tags: []string{"work"}}); got != "(cardinality(tags) = 0 OR tags::text[] && ARRAY['work']::text[])" {
		t.Fatalf("flat any = %s", got)
	}
	if got := recallTagPredicate(RecallQuery{Tags: []string{"work"}, TagsMatch: TagsMatchAllStrict}); got != "tags::text[] @> ARRAY['work']::text[]" {
		t.Fatalf("flat all_strict = %s", got)
	}
	if got := recallTagPredicate(RecallQuery{}); got != "TRUE" {
		t.Fatalf("no tag scoping = %s", got)
	}
}

// A compound group renders as nested clauses. Every comparison casts the column
// to text[]: the reference schema declares the tag columns
// `character varying[]`, so the uncast form fails at the server.
//
// A `resolve:"fuzzy"` leaf is not rendered here — recall resolves fuzzy leaves
// against the bank's vocabulary before this predicate is built, so only literal
// leaves reach it.
func TestTagGroupClauseRendersCompoundGroups(t *testing.T) {
	// A compound leaf carries no mode of its own, and a compound filter narrows:
	// the unset mode is overlap, which excludes untagged rows.
	andi := tagGroupClause(TagGroup{And: []TagGroup{{Tags: []string{"a"}}, {Tags: []string{"b"}}}})
	if andi != "(tags::text[] && ARRAY['a']::text[] AND tags::text[] && ARRAY['b']::text[])" {
		t.Fatalf("and = %s", andi)
	}
	ori := tagGroupClause(TagGroup{Or: []TagGroup{{Tags: []string{"a"}}, {Tags: []string{"b"}}}})
	if ori != "(tags::text[] && ARRAY['a']::text[] OR tags::text[] && ARRAY['b']::text[])" {
		t.Fatalf("or = %s", ori)
	}
	not := tagGroupClause(TagGroup{Not: &TagGroup{Tags: []string{"a"}}})
	if not != "NOT (tags::text[] && ARRAY['a']::text[])" {
		t.Fatalf("not = %s", not)
	}
	// The single-tag field is another way to write the same leaf.
	if got := tagGroupClause(TagGroup{Tag: "a"}); got != "tags::text[] && ARRAY['a']::text[]" {
		t.Fatalf("tag field = %s", got)
	}
	// An explicit mode is honoured: containment when the caller asks for it.
	if got := tagGroupClause(TagGroup{Tags: []string{"a"}, Match: TagsMatchAllStrict}); got != "tags::text[] @> ARRAY['a']::text[]" {
		t.Fatalf("all_strict leaf = %s", got)
	}
	// An empty leaf constrains nothing rather than matching nothing.
	if got := tagGroupClause(TagGroup{}); got != "TRUE" {
		t.Fatalf("empty leaf = %s", got)
	}
}

// Fuzzy resolution is capped: a bank whose tag vocabulary is huge, or a filter
// whose exact expansion would explode, is refused rather than scanned.
func TestValidateTagScopeCaps(t *testing.T) {
	fuzzy := RecallQuery{TagGroups: []TagGroup{{Resolve: TagResolveFuzzy, Tags: []string{"a"}}}}
	if err := ValidateTagScope(fuzzy, []string{"a"}); err != nil {
		t.Fatalf("a small vocabulary is accepted: %v", err)
	}
	big := make([]string, 0, MaxTagVocabulary+1)
	for i := 0; i <= MaxTagVocabulary; i++ {
		big = append(big, fmt.Sprintf("tag-%d", i))
	}
	if err := ValidateTagScope(fuzzy, big); !errors.Is(err, ErrTagVocabularyTooLarge) {
		t.Fatalf("err = %v, want the vocabulary cap", err)
	}
	// A query that never resolves fuzzily is always accepted, however many tags
	// the bank has.
	flat := RecallQuery{Tags: []string{"a"}}
	if err := ValidateTagScope(flat, big); err != nil {
		t.Fatalf("a non-fuzzy filter is not capped: %v", err)
	}
}

// any_strict means overlap, excluding untagged rows — not containment, which
// would require a row to carry every requested tag.
func TestFlatTagClauseMatchModes(t *testing.T) {
	cases := map[string]string{
		TagsMatchAny:       "(cardinality(tags) = 0 OR tags::text[] && ARRAY['a']::text[])",
		TagsMatchAll:       "(cardinality(tags) = 0 OR tags::text[] @> ARRAY['a']::text[])",
		TagsMatchAnyStrict: "tags::text[] && ARRAY['a']::text[]",
		TagsMatchAllStrict: "tags::text[] @> ARRAY['a']::text[]",
		TagsMatchExact:     "(tags::text[] <@ ARRAY['a']::text[] AND tags::text[] @> ARRAY['a']::text[])",
	}
	for mode, want := range cases {
		if got := flatTagClause([]string{"a"}, mode); got != want {
			t.Errorf("flatTagClause(%q) = %q, want %q", mode, got, want)
		}
	}
	// A group leaf with no mode narrows strictly, so it must render as overlap
	// rather than containment.
	if got := tagLeafClause(TagGroup{Tags: []string{"a"}}); got != "tags::text[] && ARRAY['a']::text[]" {
		t.Fatalf("a default leaf = %s, want the overlap form", got)
	}
}

// recallPredicates wraps the tag fragment into the one shared predicate, so one
// argument list serves every recall arm. The temporal window is deliberately
// absent: it belongs to the temporal arm, and filtering the dense/lexical/graph
// arms by it made a query that merely mentioned a date return nothing.
func TestRecallPredicatesWrapTagsWithoutFilteringTheWindow(t *testing.T) {
	got := recallPredicates(RecallQuery{Tags: []string{"work"}})
	if !strings.HasPrefix(got, "(") {
		t.Fatalf("the tag fragment must be parenthesised: %s", got)
	}
	if strings.Contains(got, "timestamptz") {
		t.Fatalf("the shared arm predicate must not carry a temporal window: %s", got)
	}
	if strings.Contains(got, "$3") {
		t.Fatalf("the shared arm predicate must only name $1 bank and $2 types: %s", got)
	}
	// A compound group tree takes precedence over the flat tag set, exactly as
	// the in-process evaluator does.
	grouped := recallPredicates(RecallQuery{
		Tags:      []string{"ignored"},
		TagGroups: []TagGroup{{Tags: []string{"a"}}, {Tags: []string{"b"}}},
	})
	if strings.Contains(grouped, "'ignored'") {
		t.Fatalf("the flat tags must be ignored when groups are present: %s", grouped)
	}
	if !strings.HasPrefix(grouped, "(tags::text[] && ARRAY['a']::text[] AND tags::text[] && ARRAY['b']::text[])") {
		t.Fatalf("grouped predicate = %s", grouped)
	}
}
