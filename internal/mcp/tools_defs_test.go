package mcp

import (
	"strings"
	"testing"
)

// toolDef returns one tool's definition from the multi-bank roster.
func toolDef(t *testing.T, name string) tool {
	t.Helper()
	for _, def := range toolDefinitions(true) {
		if def.Name == name {
			return def
		}
	}
	t.Fatalf("roster has no tool %q", name)
	return tool{}
}

// toolProperties reads the schema's property map.
func toolProperties(t *testing.T, def tool) map[string]any {
	t.Helper()
	properties, ok := def.InputSchema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("tool %q schema carries no properties: %v", def.Name, def.InputSchema)
	}
	return properties
}

// The recall schema carries the six include members v1.1.0 declares, with the
// defaults its own declaration states, so a client reading tools/list learns
// the channels exist.
func TestRecallToolDefCarriesTheIncludeMembers(t *testing.T) {
	properties := toolProperties(t, toolDef(t, "recall"))
	want := map[string]any{
		"include_chunks":          false,
		"include_entities":        false,
		"include_source_facts":    false,
		"max_chunk_tokens":        8192,
		"max_entity_tokens":       500,
		"max_source_facts_tokens": 4096,
	}
	for member, wantDefault := range want {
		property, ok := properties[member].(map[string]any)
		if !ok {
			t.Fatalf("recall schema has no %q member", member)
		}
		if property["default"] != wantDefault {
			t.Fatalf("recall %q default = %v, want %v", member, property["default"], wantDefault)
		}
		wantType := "boolean"
		if _, ok := wantDefault.(bool); !ok {
			wantType = "integer"
		}
		if property["type"] != wantType {
			t.Fatalf("recall %q type = %v, want %q", member, property["type"], wantType)
		}
	}
}

// The recall and reflect tags_match members enumerate the five modes instead of
// accepting any string, which is how the reference rejects an unknown mode
// before the tool runs.
func TestRecallAndReflectTagsMatchEnumerateTheModes(t *testing.T) {
	wantModes := []any{"any", "all", "any_strict", "all_strict", "exact"}
	for _, name := range []string{"recall", "reflect"} {
		properties := toolProperties(t, toolDef(t, name))
		property, ok := properties["tags_match"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no tags_match member", name)
		}
		modes, ok := property["enum"].([]any)
		if !ok {
			t.Fatalf("%s tags_match carries no enum: %v", name, property)
		}
		if len(modes) != len(wantModes) {
			t.Fatalf("%s tags_match enum = %v, want %v", name, modes, wantModes)
		}
		for i, mode := range modes {
			if mode != wantModes[i] {
				t.Fatalf("%s tags_match enum = %v, want %v", name, modes, wantModes)
			}
		}
		if property["default"] != "any" {
			t.Fatalf("%s tags_match default = %v, want \"any\"", name, property["default"])
		}
	}
}

// The update_memory schema advertises mentioned_at, which the patch applies.
func TestUpdateMemoryToolDefCarriesMentionedAt(t *testing.T) {
	def := toolDef(t, "update_memory")
	properties := toolProperties(t, def)
	property, ok := properties["mentioned_at"].(map[string]any)
	if !ok {
		t.Fatal("update_memory schema has no mentioned_at member")
	}
	forms, ok := property["anyOf"].([]any)
	if !ok || len(forms) != 2 {
		t.Fatalf("update_memory mentioned_at = %v, want a nullable string", property)
	}
	if _, isString := forms[0].(map[string]any)["type"]; !isString {
		t.Fatalf("update_memory mentioned_at forms = %v, want string and null", forms)
	}
	if property["default"] != nil {
		t.Fatalf("update_memory mentioned_at default = %v, want nil", property["default"])
	}
	if !strings.Contains(def.Description, "occurred_end / mentioned_at /") {
		t.Fatalf("update_memory description does not name mentioned_at: %q", def.Description)
	}
}

// The description drifts the port missed: the bank configuration field list,
// the documents estimate cap, and the reflect tag-matching modes.
func TestToolDefDescriptionsCarryTheReferenceWording(t *testing.T) {
	updateBank := toolDef(t, "update_bank")
	if !strings.Contains(updateBank.Description,
		"- recall_prefer_observations: Default prefer_observations for recall requests that omit it.") {
		t.Fatalf("update_bank description misses recall_prefer_observations: %q", updateBank.Description)
	}
	listDocuments := toolDef(t, "list_documents")
	if !strings.Contains(listDocuments.Description,
		"when 'total_is_estimate' is true there are more\n        matches than 'total' — narrow 'q' to reach them.") {
		t.Fatalf("list_documents description misses the estimate cap: %q", listDocuments.Description)
	}
	reflectDef := toolDef(t, "reflect")
	if !strings.Contains(reflectDef.Description,
		"'any_strict' / 'all_strict' (same, untagged excluded) or 'exact' (exactly these tags)") {
		t.Fatalf("reflect description misses the tag-matching modes: %q", reflectDef.Description)
	}
}

// The trigger subschema of every mental model and knowledge tool carries
// full_refresh_cron, which the stored trigger already honours.
func TestTriggerDefsCarryFullRefreshCron(t *testing.T) {
	wantDescription := "UTC five-field cron schedule for a scheduled FULL refresh, e.g. '0 3 * * 0' for Sundays at 03:00 " +
		"UTC: the content is regenerated from scratch on this schedule whatever 'mode' says, to clear the " +
		"drift delta edits accumulate. Independent of refresh_cron and refresh_after_consolidation, and " +
		"it does not change 'mode'. Skipped when no refresh ran since the last full one. null = no schedule."
	for _, name := range []string{
		"create_mental_model", "update_mental_model", "create_knowledge_page", "update_knowledge_node",
	} {
		properties := toolProperties(t, toolDef(t, name))
		trigger, ok := properties["trigger"].(map[string]any)
		if !ok {
			t.Fatalf("%s schema has no trigger member", name)
		}
		forms, ok := trigger["anyOf"].([]any)
		if !ok || len(forms) == 0 {
			t.Fatalf("%s trigger carries no forms: %v", name, trigger)
		}
		triggerSchema, ok := forms[0].(map[string]any)
		if !ok {
			t.Fatalf("%s trigger form = %v", name, forms[0])
		}
		triggerProperties, ok := triggerSchema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s trigger form carries no properties: %v", name, triggerSchema)
		}
		property, ok := triggerProperties["full_refresh_cron"].(map[string]any)
		if !ok {
			t.Fatalf("%s trigger schema has no full_refresh_cron member", name)
		}
		if property["default"] != nil {
			t.Fatalf("%s full_refresh_cron default = %v, want nil", name, property["default"])
		}
		if property["description"] != wantDescription {
			t.Fatalf("%s full_refresh_cron description = %q", name, property["description"])
		}
	}
}
