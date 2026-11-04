//go:build pgx

package storage

import (
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The circuit breaker's decisions, which are pure: a project either rests or gets
// one probe round, and the difference between the two is what stops a project
// whose answer never parses from spending a model call on every tick.

func breakerTestOpts() MergeRoundOptions {
	return MergeRoundOptions{FailThreshold: 5, RecoveryInterval: 15 * time.Minute}
}

// failingState is one member that has failed count rounds, last at attemptedAt.
func failingState(id string, count int, attemptedAt *time.Time) tenant.MergeState {
	state := tenant.MergeState{TenantID: id, FailCount: count}
	if attemptedAt != nil {
		value := *attemptedAt
		state.LastAttemptAt = &value
	}
	return state
}

func TestBreakerStaysClosedBelowTheThreshold(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	states := map[string]tenant.MergeState{
		"a": failingState("a", 4, &recent),
		"b": failingState("b", 4, &recent),
	}
	if breakerOpen(states, []string{"a", "b"}, breakerTestOpts(), now) {
		t.Fatal("the breaker opened before the threshold was reached")
	}
}

func TestBreakerOpensAtTheThreshold(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	states := map[string]tenant.MergeState{
		"a": failingState("a", 5, &recent),
		"b": failingState("b", 5, &recent),
	}
	if !breakerOpen(states, []string{"a", "b"}, breakerTestOpts(), now) {
		t.Fatal("the breaker stayed closed after the threshold")
	}
}

// TestBreakerLetsOneRoundThroughAfterTheRest is the automatic recovery: a halted
// project is retried on the recovery cadence rather than waiting for an operator.
func TestBreakerLetsOneRoundThroughAfterTheRest(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	opts := breakerTestOpts()

	resting := now.Add(-5 * time.Minute)
	states := map[string]tenant.MergeState{"a": failingState("a", 9, &resting)}
	if !breakerOpen(states, []string{"a"}, opts, now) {
		t.Fatal("the breaker let a round through before the rest was over")
	}

	rested := now.Add(-20 * time.Minute)
	states = map[string]tenant.MergeState{"a": failingState("a", 9, &rested)}
	if breakerOpen(states, []string{"a"}, opts, now) {
		t.Fatal("the breaker did not let a probe round through after the rest")
	}
}

// TestBreakerStaysOpenWithoutRecovery pins that an unset recovery interval means
// "halted until an operator resets it", not "retry immediately".
func TestBreakerStaysOpenWithoutRecovery(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	longAgo := now.Add(-100 * time.Hour)
	states := map[string]tenant.MergeState{"a": failingState("a", 9, &longAgo)}
	opts := MergeRoundOptions{FailThreshold: 5, RecoveryInterval: 0}
	if !breakerOpen(states, []string{"a"}, opts, now) {
		t.Fatal("the breaker let a round through with no recovery interval")
	}
}

// TestBreakerIsPerProjectNotPerMember pins the unit: a round writes the project,
// so one member still making progress keeps the project running.
func TestBreakerIsPerProjectNotPerMember(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	states := map[string]tenant.MergeState{
		"a": failingState("a", 9, &recent),
		"b": failingState("b", 0, nil),
	}
	if breakerOpen(states, []string{"a", "b"}, breakerTestOpts(), now) {
		t.Fatal("one healthy member must keep the project running")
	}
}

// TestBreakerNeverOpensWithoutALedgerRow pins onboarding: a project whose members
// have never merged has nothing to be halted on.
func TestBreakerNeverOpensWithoutALedgerRow(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if breakerOpen(map[string]tenant.MergeState{}, []string{"a"}, breakerTestOpts(), now) {
		t.Fatal("the breaker opened for a project with no ledger")
	}
}

// TestBreakerDoesNotStallAnUpgradedDeployment pins the migration path: a row that
// counts failures but carries no attempt stamp only exists on a deployment that
// predates this column. Stalling it forever would be worse than one more try.
func TestBreakerDoesNotStallAnUpgradedDeployment(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	states := map[string]tenant.MergeState{"a": failingState("a", 88, nil)}
	if breakerOpen(states, []string{"a"}, breakerTestOpts(), now) {
		t.Fatal("a failure record with no attempt stamp must not halt the project")
	}
}

// TestBreakerDisabledWhenThresholdIsZero pins the off switch.
func TestBreakerDisabledWhenThresholdIsZero(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Minute)
	states := map[string]tenant.MergeState{"a": failingState("a", 1000, &recent)}
	opts := MergeRoundOptions{FailThreshold: 0, RecoveryInterval: time.Hour}
	if breakerOpen(states, []string{"a"}, opts, now) {
		t.Fatal("a zero threshold must never open the breaker")
	}
}
