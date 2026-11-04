package mentalmodels

import "testing"

// A stored reflect_response reaches the wire in the reference's insertion
// order. The reference builds `{text, based_on, mental_models}` first and adds
// `reflect_skipped` only afterwards, so a refresh that read nothing lists
// `mental_models` BEFORE `reflect_skipped` (memory_engine.py:18439-18447).
// Observed against the live reference on 2026-11-03:
//
//	"reflect_response": {"text":"", "outcome":"content_preserved_no_new_facts",
//	  "based_on":{}, "mental_models":[], "reflect_skipped":"no_sources_in_scope"}
func TestReflectResponseMemberOrderFollowsTheReference(t *testing.T) {
	stored := map[string]any{
		"text":            "",
		"outcome":         "content_preserved_no_new_facts",
		"based_on":        map[string]any{},
		"mental_models":   []any{},
		"reflect_skipped": "no_sources_in_scope",
	}
	ordered, ok := ReorderReflectResponse(stored).(OrderedObject)
	if !ok {
		t.Fatalf("ReorderReflectResponse returned %T, want OrderedObject", ReorderReflectResponse(stored))
	}
	var keys []string
	for _, member := range ordered {
		keys = append(keys, member.Key)
	}
	want := []string{"text", "outcome", "based_on", "mental_models", "reflect_skipped"}
	if len(keys) != len(want) {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("key %d = %q, want %q (full order %v)", i, keys[i], want[i], keys)
		}
	}
}

// A member the order list does not name still reaches the wire, after the named
// ones — a stored payload from a newer writer must not lose fields on read.
func TestReflectResponseKeepsUnnamedMembers(t *testing.T) {
	stored := map[string]any{"text": "x", "delta_gate": map[string]any{"kept": 2}}
	ordered, ok := ReorderReflectResponse(stored).(OrderedObject)
	if !ok {
		t.Fatalf("ReorderReflectResponse returned %T, want OrderedObject", ReorderReflectResponse(stored))
	}
	last := ordered[len(ordered)-1]
	if last.Key != "delta_gate" {
		t.Fatalf("last member = %q, want delta_gate (order: %v)", last.Key, memberKeys(ordered))
	}
}

func memberKeys(object OrderedObject) []string {
	keys := make([]string, 0, len(object))
	for _, member := range object {
		keys = append(keys, member.Key)
	}
	return keys
}
