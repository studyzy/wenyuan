package storage

import "testing"

// A non-positive half-life makes the exponential branch neutral instead of
// dividing by it; the resolution layer fills a positive default for real
// recalls, so this is the direct-call boundary.
func TestRecencyDecayExponentialNonPositiveHalflifeIsNeutral(t *testing.T) {
	for _, halflife := range []float64{0, -5} {
		cfg := RecencyDecayConfig{Function: RecencyDecayExponential, HalflifeDays: halflife}
		if got := RecencyDecay(100, cfg); got != 0.5 {
			t.Fatalf("halflife %v, age 100 = %v, want neutral 0.5", halflife, got)
		}
		// The half-life check precedes the future-date clamp, so even a
		// future-dated memory stays neutral with a non-positive half-life.
		if got := RecencyDecay(-5, cfg); got != 0.5 {
			t.Fatalf("halflife %v, future date = %v, want neutral 0.5", halflife, got)
		}
	}
}

// A positive half-life still decays exactly as before.
func TestRecencyDecayExponentialPositiveHalflifeStillDecays(t *testing.T) {
	cfg := RecencyDecayConfig{Function: RecencyDecayExponential, HalflifeDays: 90}
	if got := RecencyDecay(90, cfg); got != 0.5 {
		t.Fatalf("at the half-life = %v, want 0.5", got)
	}
	if got := RecencyDecay(180, cfg); got != 0.25 {
		t.Fatalf("at two half-lives = %v, want 0.25", got)
	}
	if got := RecencyDecay(-5, cfg); got != 1 {
		t.Fatalf("a future date = %v, want 1", got)
	}
}
