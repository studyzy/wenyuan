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
