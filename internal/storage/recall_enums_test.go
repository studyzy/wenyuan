package storage

import "testing"

// ts-0004: the accepted budget levels round-trip under their exact tokens.
func TestRecallBudgetLevelTokens(t *testing.T) {
	want := []string{"low", "mid", "high"}
	if got := RecallBudgetLevels(); !equalTokens(got, want) {
		t.Fatalf("budget levels = %v, want %v", got, want)
	}
}

// ts-0011: the accepted tag-matching modes round-trip under their exact tokens.
func TestRecallTagMatchModeTokens(t *testing.T) {
	want := []string{"any", "all", "any_strict", "all_strict", "exact"}
	if got := RecallTagMatchModes(); !equalTokens(got, want) {
		t.Fatalf("tag-match modes = %v, want %v", got, want)
	}
}

// ts-0003: the fact-type tokens accepted in a selection are the final set.
func TestRecallFactTypeTokens(t *testing.T) {
	want := []string{"world", "experience", "observation"}
	got := make([]string, 0, len(FinalMemoryUnitTypes()))
	for _, t2 := range FinalMemoryUnitTypes() {
		got = append(got, string(t2))
	}
	if !equalTokens(got, want) {
		t.Fatalf("fact types = %v, want %v", got, want)
	}
}

// ts-0013: the temporal-window bound keys and the fuzzy resolution token are
// distinct surfaces from the compound tag-group node keys.
func TestRecallTagResolveTokens(t *testing.T) {
	if TagResolveFuzzy != "fuzzy" || TagResolveLiteral != "literal" {
		t.Fatalf("resolve tokens = %q, %q", TagResolveFuzzy, TagResolveLiteral)
	}
}

func equalTokens(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
