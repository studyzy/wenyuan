package storage

import (
	"context"
	"testing"
)

// recordingRunner captures the request it was handed, so a test can inspect the
// resolved turn bound without running a model.
type recordingRunner struct{ last AgenticReflectRequest }

func (r *recordingRunner) Run(_ context.Context, req AgenticReflectRequest) (ReflectOutcome, error) {
	r.last = req
	return ReflectOutcome{Answer: "ok"}, nil
}

// The iteration budget scales with the requested effort level: low 0.5x, mid 1x
// and high 2x of the configured bound, never below one turn.
func TestReflectIterationsScaleWithBudget(t *testing.T) {
	cases := []struct {
		budget string
		want   int
	}{
		{ReflectBudgetLow, 5},
		{ReflectBudgetMid, 10},
		{ReflectBudgetHigh, 20},
		{"", 5}, // an unset budget is the reference's low default
	}
	for _, tc := range cases {
		store := NewMemoryStore("public")
		store.SetTuning(Tuning{ReflectMaxIterations: 10})
		running := &recordingRunner{}
		if _, err := store.CreateBank(context.Background(), Bank{BankID: "b1", Name: "B"}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.Reflect(context.Background(), ReflectQuery{
			BankID:     "b1",
			Query:      "q",
			Budget:     tc.budget,
			Reflection: running,
		}); err != nil {
			t.Fatal(err)
		}
		if running.last.MaxIterations != tc.want {
			t.Fatalf("budget %q: iterations = %d, want %d", tc.budget, running.last.MaxIterations, tc.want)
		}
	}
}

// A base bound that halves below one still runs a single turn.
func TestReflectIterationsFloorAtOne(t *testing.T) {
	store := NewMemoryStore("public")
	store.SetTuning(Tuning{ReflectMaxIterations: 1})
	running := &recordingRunner{}
	if _, err := store.CreateBank(context.Background(), Bank{BankID: "b1", Name: "B"}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Reflect(context.Background(), ReflectQuery{
		BankID: "b1", Query: "q", Budget: ReflectBudgetLow, Reflection: running,
	}); err != nil {
		t.Fatal(err)
	}
	if running.last.MaxIterations != 1 {
		t.Fatalf("iterations = %d, want 1", running.last.MaxIterations)
	}
}
