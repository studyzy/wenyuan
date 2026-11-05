package mentalmodels

import "testing"

// The exclusivity clears gate on truthiness: an empty cron is a way to say "no
// schedule", not a request to switch off consolidation-triggered refresh.
func TestMergeTriggerTruthinessGatesExclusivity(t *testing.T) {
	base := map[string]any{"refresh_after_consolidation": true, "refresh_cron": "0 * * * *"}

	empty := MergeTrigger(map[string]any{"refresh_cron": ""}, base)
	if empty["refresh_after_consolidation"] != true {
		t.Fatalf("empty cron cleared auto-refresh: %v", empty)
	}

	falseFlag := MergeTrigger(map[string]any{"refresh_after_consolidation": false}, base)
	if falseFlag["refresh_cron"] != "0 * * * *" {
		t.Fatalf("false flag cleared cron: %v", falseFlag)
	}

	truthy := MergeTrigger(map[string]any{"refresh_cron": "0 1 * * *"}, base)
	if _, ok := truthy["refresh_after_consolidation"]; ok {
		t.Fatalf("a stated cron must clear auto-refresh: %v", truthy)
	}
}
