package storage

import "testing"

// A positive fact budget that no match fits still answers with the top-ranked
// match, whole and over budget: the reference's selection keeps that floor so a
// matched query is never indistinguishable from one that matched nothing
// (engine/fact_budget.py:76-86).
func TestCapMatchesToTokensKeepsTopMatchOverBudget(t *testing.T) {
	long := RecallMatch{ID: "top", Text: "the deploy runbook is blue-green and the rollback is one command"}
	shorter := RecallMatch{ID: "next", Text: "the deploy runbook is canary"}
	cost := recallTokenCost(long.Text)
	if cost <= 1 {
		t.Fatalf("the fixture must cost more than one token, got %d", cost)
	}

	t.Run("nothing fits", func(t *testing.T) {
		got := capMatchesToTokens([]RecallMatch{long, shorter}, 1)
		if len(got) != 1 || got[0].ID != "top" {
			t.Fatalf("cap 1 over an oversized list = %+v, want the top match whole", got)
		}
	})

	t.Run("top fits exactly", func(t *testing.T) {
		got := capMatchesToTokens([]RecallMatch{long, shorter}, cost)
		if len(got) != 1 || got[0].ID != "top" {
			t.Fatalf("cap %d = %+v, want just the top match", cost, got)
		}
	})

	t.Run("short one fits beside an oversized top", func(t *testing.T) {
		// The oversized match skips only itself, so the shorter one behind it
		// still makes the answer and the floor never applies.
		got := capMatchesToTokens([]RecallMatch{long, shorter}, recallTokenCost(shorter.Text))
		if len(got) != 1 || got[0].ID != "next" {
			t.Fatalf("cap covering only the shorter match = %+v, want it alone", got)
		}
	})

	t.Run("non-positive budget is unbounded here", func(t *testing.T) {
		// The channel-level "facts off" reading of a non-positive budget is the
		// handler's (recall.go drops the results channel); at this layer the cap
		// simply does not apply.
		for _, budget := range []int{0, -1} {
			if got := capMatchesToTokens([]RecallMatch{long, shorter}, budget); len(got) != 2 {
				t.Fatalf("cap %d = %+v, want both matches untouched", budget, got)
			}
		}
	})

	t.Run("empty input stays empty", func(t *testing.T) {
		if got := capMatchesToTokens(nil, 1); len(got) != 0 {
			t.Fatalf("cap over an empty list = %+v", got)
		}
	})
}
