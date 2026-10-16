package storage

import (
	"context"
	"fmt"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
)

// The store's recall-budget and diagnostic knobs default to the configuration
// table's own values, so a store built without the resolved tuning behaves like
// one the composition root configured: the fixed function, the reference's
// ratios and bounds, and the diagnostic accounting on for every recall. The two
// are written in two places because storage cannot import the configuration
// surface, so this holds them together.
func TestRecallBudgetTuningDefaultsMatchTheConfiguration(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	tuning := DefaultTuning()
	pairs := []struct {
		name     string
		store    any
		expected any
	}{
		{"budget function", tuning.RecallBudgetFunction, DefaultRecallBudgetFunction},
		{"function as the configuration resolves it", tuning.RecallBudgetFunction, cfg.ProcessDefaults.RecallBudgetFunction},
		{"adaptive low ratio", tuning.RecallBudgetAdaptiveLow, config.DefaultRecallBudgetAdaptiveLow},
		{"adaptive mid ratio", tuning.RecallBudgetAdaptiveMid, config.DefaultRecallBudgetAdaptiveMid},
		{"adaptive high ratio", tuning.RecallBudgetAdaptiveHigh, config.DefaultRecallBudgetAdaptiveHigh},
		{"budget floor", tuning.RecallBudgetMin, config.DefaultRecallBudgetMin},
		{"budget ceiling", tuning.RecallBudgetMax, config.DefaultRecallBudgetMax},
		{"diagnostic phases", tuning.DiagnosticPhases, cfg.RecallTuning.DiagnosticPhases},
		{"phase sample rate", tuning.PhaseSampleEvery, cfg.RecallTuning.PhaseSampleEvery},
	}
	for _, pair := range pairs {
		if pair.store != pair.expected {
			t.Errorf("%s: store default %v, configuration default %v", pair.name, pair.store, pair.expected)
		}
	}
	// The reference ships the diagnostic accounting on and every recall
	// reported, so an unset deployment gets the accounting rather than silence.
	if !tuning.DiagnosticPhases || tuning.PhaseSampleEvery != 1 {
		t.Fatalf("diagnostic defaults = on:%v every:%d, want on:true every:1",
			tuning.DiagnosticPhases, tuning.PhaseSampleEvery)
	}
}

// The fixed function is the default, and it ignores the request's token budget:
// an unset function leaves every level at the number the tuning carries.
func TestBudgetLimitForQueryFixedByDefault(t *testing.T) {
	tuning := DefaultTuning()
	if tuning.RecallBudgetFunction != RecallBudgetFixed {
		t.Fatalf("default budget function = %q, want %q", tuning.RecallBudgetFunction, RecallBudgetFixed)
	}
	tests := []struct {
		name   string
		budget string
		want   int
	}{
		{"low", BudgetLow, DefaultRecallBudgetLow},
		{"mid", BudgetMid, DefaultRecallBudgetMid},
		{"high", BudgetHigh, DefaultRecallBudgetHigh},
		{"absent falls back to mid", "", DefaultRecallBudgetMid},
		{"unrecognised falls back to mid", "enormous", DefaultRecallBudgetMid},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			// The token budget is one the adaptive function would scale, so
			// this fails if the fixed function ever starts reading it.
			got := tuning.BudgetLimitForQuery(RecallQuery{Budget: test.budget, MaxTokens: 100000})
			if got != test.want {
				t.Fatalf("fixed BudgetLimitForQuery(%q, 100000) = %d, want %d", test.budget, got, test.want)
			}
		})
	}

	unknown := DefaultTuning()
	unknown.RecallBudgetFunction = "exponential"
	if got := unknown.BudgetLimitForQuery(RecallQuery{Budget: BudgetLow, MaxTokens: 100000}); got != DefaultRecallBudgetLow {
		t.Fatalf("an unrecognised function bound = %d, want the fixed %d", got, DefaultRecallBudgetLow)
	}
}

// The adaptive function scales the request's token budget by the level's ratio
// and clamps the result, and a request that names no token budget scales the
// reference's default recall budget.
func TestBudgetLimitForQueryAdaptive(t *testing.T) {
	adaptive := DefaultTuning()
	adaptive.RecallBudgetFunction = RecallBudgetAdaptive

	tests := []struct {
		name      string
		budget    string
		maxTokens int
		want      int
	}{
		{"low is the low ratio of the token budget", BudgetLow, 4096, 102},
		{"mid is the mid ratio", BudgetMid, 4096, 307},
		{"high is the high ratio", BudgetHigh, 4096, 1024},
		{"an absent level uses the mid ratio", "", 4096, 307},
		{"an unrecognised level uses the mid ratio", "enormous", 4000, 300},
		{"a named budget scales with it", BudgetHigh, 8000, 2000},
		{"a small budget is raised to the minimum", BudgetLow, 100, DefaultRecallBudgetMin},
		{"a large budget is held to the maximum", BudgetHigh, 100000, DefaultRecallBudgetMax},
		{"no token budget scales the reference default", BudgetMid, 0, 307},
		{"a negative token budget scales the reference default", BudgetLow, -1, 102},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := adaptive.BudgetLimitForQuery(RecallQuery{Budget: test.budget, MaxTokens: test.maxTokens})
			if got != test.want {
				t.Fatalf("adaptive BudgetLimitForQuery(%q, %d) = %d, want %d",
					test.budget, test.maxTokens, got, test.want)
			}
		})
	}

	// The reference's shipped ratio applied to its shipped default scale is
	// what the fixed levels were derived from, so the two functions agree
	// closely: a smoke test that the adaptive numbers are in the same range.
	if low := adaptive.BudgetLimitForQuery(RecallQuery{Budget: BudgetLow, MaxTokens: DefaultRecallBudgetScaleTokens}); low <= 0 ||
		low > DefaultRecallBudgetLow*2 {
		t.Fatalf("adaptive low bound %d is not near the fixed %d", low, DefaultRecallBudgetLow)
	}

	// A bound of zero or below is unset rather than a clamp to zero, so a
	// store built without the resolved tuning still gets the ratio's value.
	unbounded := adaptive
	unbounded.RecallBudgetMin = 0
	unbounded.RecallBudgetMax = 0
	if got := unbounded.BudgetLimitForQuery(RecallQuery{Budget: BudgetLow, MaxTokens: 100}); got != 3 {
		t.Fatalf("an unset clamp bound = %d, want the unclamped 3", got)
	}
}

// A negative clamp would be nonsense input rather than a bound, and the ratio
// is what survives it.
func TestBudgetLimitForQueryAdaptiveDefaultsRatios(t *testing.T) {
	tuning := Tuning{RecallBudgetFunction: RecallBudgetAdaptive}
	tuning.RecallBudgetAdaptiveLow = DefaultRecallBudgetAdaptiveLow
	tuning.RecallBudgetAdaptiveMid = DefaultRecallBudgetAdaptiveMid
	tuning.RecallBudgetAdaptiveHigh = DefaultRecallBudgetAdaptiveHigh
	if got := tuning.BudgetLimitForQuery(RecallQuery{Budget: BudgetHigh, MaxTokens: 4096}); got != 1024 {
		t.Fatalf("high bound = %d, want 1024", got)
	}
}

// The adaptive bound is what the pipeline actually honours: a level's ratio of
// the request's token budget caps the returned candidates, where the fixed
// function would return everything the bank holds.
func TestRecallHonoursAdaptiveBudgetBound(t *testing.T) {
	store := NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), Bank{BankID: "b1", Name: "b1"}); err != nil {
		t.Fatal(err)
	}
	const written = 60
	items := make([]RetainItem, 0, written)
	for i := range written {
		items = append(items, RetainItem{
			Content:    fmt.Sprintf("fact number %d about the topic", i),
			DocumentID: fmt.Sprintf("doc-%d", i),
		})
	}
	if _, err := store.RetainWrite(context.TODO(), RetainRequest{BankID: "b1", Items: items}); err != nil {
		t.Fatal(err)
	}
	// 0.025 of an 800-token budget is the low bound, well under the 60 facts
	// written and well over what the token cap would keep.
	tuneStore(t, store, func(tune *Tuning) { tune.RecallBudgetFunction = RecallBudgetAdaptive })

	adaptive, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "fact topic", Budget: BudgetLow, MaxTokens: 800})
	if err != nil {
		t.Fatal(err)
	}
	if len(adaptive.Matches) != 20 {
		t.Fatalf("adaptive low returned %d matches, want the 20-candidate bound", len(adaptive.Matches))
	}
	if adaptive.Trace.Budget != 20 {
		t.Fatalf("trace budget = %d, want 20", adaptive.Trace.Budget)
	}

	// The same request under the fixed function is bounded by the fixed level
	// instead, which is above what the bank holds.
	tuneStore(t, store, func(tune *Tuning) { tune.RecallBudgetFunction = RecallBudgetFixed })
	fixed, err := store.Recall(context.TODO(), RecallQuery{BankID: "b1", Query: "fact topic", Budget: BudgetLow, MaxTokens: 800})
	if err != nil {
		t.Fatal(err)
	}
	if len(fixed.Matches) != written {
		t.Fatalf("fixed low returned %d matches, want all %d", len(fixed.Matches), written)
	}
}
