package mcp

import (
	"errors"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/mentalmodels"
)

// validateTagsMatchArgument checks the legacy flat `tags_match` argument the
// mental-model and knowledge-page tools still accept beside the trigger object.
// The reference runs the same check in `_validate_mental_model_inputs`, names the
// accepted members in the sentence, and refuses the call before it writes.
func validateTagsMatchArgument(args map[string]any) string {
	raw, present := args["tags_match"]
	if !present || raw == nil {
		return ""
	}
	accepted := mentalmodels.TriggerTagsMatchValues()
	if match, isString := raw.(string); isString {
		for _, value := range accepted {
			if match == value {
				return ""
			}
		}
	}
	return "tags_match must be one of " + strings.Join(accepted, ", ") + ", got " + pythonRepr(raw)
}

// pythonRepr renders a rejected value the way the reference's `{value!r}`
// interpolation does for the sentence it builds: a string is single-quoted.
func pythonRepr(value any) string {
	if text, ok := value.(string); ok {
		return "'" + text + "'"
	}
	return pythonJSONText(value)
}

// validatedTriggerPatch validates a tool call's trigger inputs and returns the
// members to store, or nil when the call supplied none.
//
// It is the MCP twin of the HTTP model: `mentalmodels.ValidateTriggerObject`
// runs exactly the checks the reference's `MentalModelTrigger` (and its MCP
// `MentalModelTriggerInput`, which declares the same members) runs, so a cron
// expression its parser refuses and a member of the wrong kind are refused here
// rather than stored. A member the model does not declare is refused separately,
// because the reference's tool input declares `extra="forbid"` while the HTTP
// model drops the unknown key.
func validatedTriggerPatch(args map[string]any) (map[string]any, error) {
	trigger := objectArgument(args, "trigger")
	if trigger != nil {
		if err := triggerRefusal(trigger); err != nil {
			return nil, err
		}
	}
	patch := triggerPatch(args)
	if patch == nil {
		return nil, nil
	}
	if trigger != nil {
		// The flat shorthands were folded in by `triggerPatch`, so the combined
		// patch is what the model would have validated.
		if err := triggerRefusal(patch); err != nil {
			return nil, err
		}
	}
	return patch, nil
}

// triggerRefusal validates one trigger object and renders its items as the
// single sentence a caller reads, in the model's declaration order.
func triggerRefusal(trigger map[string]any) error {
	members := make([]string, 0, len(trigger))
	for member := range trigger {
		members = append(members, member)
	}
	sort.Strings(members)
	for _, member := range members {
		if !mentalmodels.TriggerMember(member) {
			return errors.New("trigger." + member + ": Extra inputs are not permitted")
		}
	}
	failure := mentalmodels.ValidateTriggerObject(trigger)
	if failure == nil || len(failure.Errors) == 0 {
		return nil
	}
	messages := make([]string, 0, len(failure.Errors))
	for _, item := range failure.Errors {
		if item.Msg != "" {
			messages = append(messages, item.Msg)
		}
	}
	return errors.New(strings.Join(messages, "\n"))
}

// mergeTriggerPatch layers a validated trigger patch over the model's current
// trigger the way the reference's `_merge_trigger` does: every supplied member
// wins (an explicit null clears it), and supplying one of the two schedule
// members drops the other unless the patch states it too — "setting one drops
// an unstated other rather than producing a combination no request could have
// expressed" (memory_engine.py:19997-20002).
func mergeTriggerPatch(supplied, current map[string]any) map[string]any {
	merged := make(map[string]any, len(current)+len(supplied))
	for key, value := range current {
		merged[key] = value
	}
	for key, value := range supplied {
		merged[key] = value
	}
	if _, statedCron := supplied["refresh_cron"]; !statedCron {
		return merged
	}
	if cron, _ := supplied["refresh_cron"].(string); strings.TrimSpace(cron) != "" {
		delete(merged, "refresh_after_consolidation")
	}
	return merged
}
