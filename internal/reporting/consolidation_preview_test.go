package reporting

import "testing"

// fnmatchCase matches Python's fnmatchcase for the glob forms strategies use.
func TestFnmatchCase(t *testing.T) {
	cases := []struct {
		name, pattern string
		want          bool
	}{
		{"company:acme", "company:*", true},
		{"company:", "company:*", true},
		{"team:exec", "company:*", false},
		{"a", "?", true},
		{"ab", "?", false},
		{"a", "[abc]", true},
		{"d", "[abc]", false},
		{"d", "[!abc]", true},
		{"a", "[!abc]", false},
		{"literal", "literal", true},
	}
	for _, tc := range cases {
		if got := fnmatchCase(tc.name, tc.pattern); got != tc.want {
			t.Fatalf("fnmatchCase(%q, %q) = %v, want %v", tc.name, tc.pattern, got, tc.want)
		}
	}
}

// buildConsolidationPreview applies first-strategy-wins and reports inactive
// strategies in place.
func TestBuildConsolidationPreview(t *testing.T) {
	scopes := []scopePreview{
		{Tags: []string{"company:acme", "team:exec"}, Count: 5},
		{Tags: []string{"company:other"}, Count: 2},
		{Tags: []string{}, Count: 7},
	}
	mission := "keep it short"
	limit := 5
	strategies := []consolidationStrategySpec{
		{Scopes: []consolidationScopePattern{{Tags: []string{"company:*"}}}, MaxObservationsPerScope: &limit},
		{Scopes: []consolidationScopePattern{{Tags: []string{"company:*"}}}, Mission: &mission},
		{Scopes: []consolidationScopePattern{{Tags: []string{"company:acme", "team:exec"}, TagsMatch: "exact"}}, Mission: &mission},
		{Scopes: []consolidationScopePattern{{Tags: []string{}}}, Mission: &mission}, // inactive: no usable rule
	}
	preview := buildConsolidationPreview(strategies, scopes, 5, true)

	if preview.ScopesScanned != 3 || !preview.Complete {
		t.Fatalf("scanned/complete = %d/%v", preview.ScopesScanned, preview.Complete)
	}
	if len(preview.Strategies) != 4 {
		t.Fatalf("strategies = %d, want 4 (aligned)", len(preview.Strategies))
	}
	if !preview.Strategies[0].Active || preview.Strategies[3].Active {
		t.Fatalf("active flags = %v %v", preview.Strategies[0].Active, preview.Strategies[3].Active)
	}
	// The first strategy claims both company scopes; the second claims none.
	if preview.Strategies[0].ClaimedCount != 2 {
		t.Fatalf("strategy 0 claimed = %d, want 2", preview.Strategies[0].ClaimedCount)
	}
	if preview.Strategies[1].ClaimedCount != 0 {
		t.Fatalf("strategy 1 claimed = %d, want 0", preview.Strategies[1].ClaimedCount)
	}
	// The untagged scope is unclaimed and lands in Default.
	if preview.Default.MatchCount != 1 || preview.Default.ObservationCount != 7 {
		t.Fatalf("default = %+v", preview.Default)
	}
	// Strategy 1's rule matched two scopes but was taken by strategy 0.
	rule := preview.Strategies[1].Rules[0]
	if rule.MatchCount != 2 || rule.TakenCount != 2 {
		t.Fatalf("strategy 1 rule = %+v", rule)
	}
	if rule.ObservationCount != 7 {
		t.Fatalf("strategy 1 observation_count = %d, want 7", rule.ObservationCount)
	}
}

// A rule of an *inactive* strategy still reports what it matches, and every one
// of those scopes counts as taken away: the reference computes taken_count as
// `sum(1 for k in hits if winners[k] != i)` (consolidator.py:3603), so a scope
// no strategy claims — winners[k] is nil — is taken away from this rule too,
// since this strategy does not handle it. Reporting 0 there (counting only
// scopes an earlier strategy had already won) is the defect this pins.
func TestInactiveStrategyRulesReportScopesTheyDoNotHandle(t *testing.T) {
	scopes := []scopePreview{
		{Tags: []string{"topic:cars", "user:ann"}, Count: 1},
		{Tags: []string{"user:ann"}, Count: 1},
		{Tags: []string{"user:bid"}, Count: 1},
	}
	// No setting and one rule: the reference drops this strategy (inactive), so
	// it claims nothing even though its rule matches every scope.
	strategies := []consolidationStrategySpec{
		{Scopes: []consolidationScopePattern{{Tags: []string{"*"}}}},
	}
	preview := buildConsolidationPreview(strategies, scopes, 5, true)

	if len(preview.Strategies) != 1 || preview.Strategies[0].Active {
		t.Fatalf("strategy = %+v, want one inactive strategy", preview.Strategies)
	}
	rule := preview.Strategies[0].Rules[0]
	if rule.MatchCount != 3 {
		t.Fatalf("match_count = %d, want 3", rule.MatchCount)
	}
	if rule.TakenCount != 3 {
		t.Fatalf("taken_count = %d, want 3 (every match is unhandled by this strategy)", rule.TakenCount)
	}
	// The default section still owns all three, and nothing is claimed.
	if preview.Default.MatchCount != 3 || preview.Strategies[0].ClaimedCount != 0 {
		t.Fatalf("default = %d, claimed = %d; want 3 and 0",
			preview.Default.MatchCount, preview.Strategies[0].ClaimedCount)
	}

	// An *active* strategy's rule keeps counting only the scopes another
	// strategy took: its own wins are handled by it, so they are not taken away.
	limit := 1
	active := buildConsolidationPreview([]consolidationStrategySpec{
		{Scopes: []consolidationScopePattern{{Tags: []string{"user:*"}}}, MaxObservationsPerScope: &limit},
		{Scopes: []consolidationScopePattern{{Tags: []string{"user:*"}}}, MaxObservationsPerScope: &limit},
	}, scopes, 5, true)
	if got := active.Strategies[0].Rules[0].TakenCount; got != 0 {
		t.Fatalf("winner's taken_count = %d, want 0", got)
	}
	if got := active.Strategies[1].Rules[0].TakenCount; got != 3 {
		t.Fatalf("loser's taken_count = %d, want 3 (every match, all won by strategy 0)", got)
	}
}
