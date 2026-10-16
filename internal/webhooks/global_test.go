package webhooks

import "testing"

// A nil target and one with no URL are both disabled, so an unset key cannot
// deliver anywhere.
func TestGlobalTargetEnabled(t *testing.T) {
	var nilTarget *GlobalTarget
	if nilTarget.Enabled() {
		t.Fatal("a nil target must be disabled")
	}
	if (&GlobalTarget{}).Enabled() {
		t.Fatal("a target with no URL must be disabled")
	}
	if !(&GlobalTarget{URL: "https://global.test/hook"}).Enabled() {
		t.Fatal("a target with a URL must be enabled")
	}
}

// The event filter selects exactly the subscribed events; an unset filter uses
// the reference's default rather than matching everything.
func TestGlobalTargetSubscribes(t *testing.T) {
	var nilTarget *GlobalTarget
	if nilTarget.Subscribes("consolidation.completed") {
		t.Fatal("a nil target subscribes to nothing")
	}
	defaultTarget := &GlobalTarget{URL: "https://global.test/hook"}
	if !defaultTarget.Subscribes(DefaultGlobalEventType) {
		t.Fatalf("an unset filter must deliver the default event %q", DefaultGlobalEventType)
	}
	if defaultTarget.Subscribes("retain.completed") {
		t.Fatal("an unset filter must not deliver an event outside the default set")
	}
	filtered := &GlobalTarget{EventTypes: []string{"retain.completed"}}
	if filtered.Subscribes("consolidation.completed") {
		t.Fatal("an event outside the filter must not be delivered")
	}
	if !filtered.Subscribes("retain.completed") {
		t.Fatal("a subscribed event must be delivered")
	}
}

// The response-body exposure switch is read even when no URL is set, and a nil
// target never exposes.
func TestGlobalTargetExposesResponseBody(t *testing.T) {
	var nilTarget *GlobalTarget
	if nilTarget.ExposesResponseBody() {
		t.Fatal("a nil target must not expose a response body")
	}
	if (&GlobalTarget{ExposeResponseBody: true}).ExposesResponseBody() != true {
		t.Fatal("the switch must be read even without a URL")
	}
}

// The guard carries the target, and a nil guard is nil-safe.
func TestGuardCarriesGlobalTarget(t *testing.T) {
	var nilGuard *Guard
	if nilGuard.Global() != nil {
		t.Fatal("a nil guard carries no target")
	}
	target := &GlobalTarget{URL: "https://global.test/hook"}
	guard := NewGuard(GuardConfig{Global: target})
	if guard.Global() != target {
		t.Fatal("the guard must carry the configured target")
	}
}
