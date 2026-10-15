package cli

import (
	"context"
	"fmt"
	"strings"
)

func mentalModelList(ctx context.Context, d *Deps, bank string) error {
	detail := "content"
	if d.Verbose {
		detail = "full"
	}
	var out mmListOut
	path := fmt.Sprintf("%s/%s/mental-models?detail=%s", batchDBase, bank, detail)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Mental Models: %s", bank)
	if len(out.Items) == 0 {
		ui.Infof("No mental models found.")
		return nil
	}
	for _, m := range out.Items {
		ui.Plain("  %s %s", m.ID, m.Name)
		if m.Content != "" {
			ui.Plain("    %s", Truncate(m.Content, 80))
		}
		ui.Plain("")
	}
	return nil
}

func mentalModelGet(ctx context.Context, d *Deps, bank, id string) error {
	var out mmModel
	if err := d.Client.Do(ctx, "GET", mentalModelPath(bank, id), nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	mentalModelDetail(d.UI, out)
	return nil
}

func mentalModelCreate(ctx context.Context, d *Deps, bank, name, sourceQuery string, cf mmCreateFlags) error {
	tagsMatch := ""
	if cf.tagsMatch != "" {
		parsed, err := parseTagsMatch(cf.tagsMatch)
		if err != nil {
			return err
		}
		tagsMatch = parsed
	}
	mode := ""
	if cf.mode != "" {
		parsed, err := parseTriggerMode(cf.mode)
		if err != nil {
			return err
		}
		mode = parsed
	}
	// The trigger is sent only when one of the three gating flags was
	// passed; unspecified members take the API defaults. A passed
	// --tags-match travels with it, exactly as the reference builds the
	// request.
	var trigger map[string]any
	if cf.consol || tagsMatch != "" || mode != "" {
		if mode == "" {
			mode = "full"
		}
		trigger = map[string]any{"mode": mode, "refresh_after_consolidation": cf.consol}
		if tagsMatch != "" {
			trigger["tags_match"] = tagsMatch
		}
	}
	body := mmCreateBody{
		ID:          cf.id,
		Name:        name,
		SourceQuery: sourceQuery,
		MaxTokens:   cf.maxTokens,
		Tags:        splitFlagList(cf.tags),
		Trigger:     trigger,
	}
	var out mmCreateOut
	path := fmt.Sprintf("%s/%s/mental-models", batchDBase, bank)
	if err := d.Client.Do(ctx, "POST", path, body, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	d.UI.Infof("Mental model created, operation_id: %s", out.OperationID)
	return nil
}

func mentalModelUpdate(ctx context.Context, d *Deps, bank, id string, f *mmUpdateFlags) error {
	if !f.any() {
		return NewCommandError("At least one of --name, --source-query, --max-tokens, --tags, or a --trigger-* flag must be provided")
	}
	body := mmUpdateBody{}
	if f.name != "" {
		body.Name = &f.name
	}
	if f.sourceQuery != "" {
		body.SourceQuery = &f.sourceQuery
	}
	if f.maxTokens.set {
		v := f.maxTokens.value
		body.MaxTokens = &v
	}
	if f.tags != "" {
		list := splitFlagList(f.tags)
		body.Tags = &list
	}
	if f.triggerAny() {
		trig, err := mentalModelMergedTrigger(ctx, d, bank, id, f)
		if err != nil {
			return err
		}
		body.Trigger = trig
	}
	var out mmModel
	if err := d.Client.Do(ctx, "PATCH", mentalModelPath(bank, id), body, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	d.UI.Infof("Mental model '%s' updated successfully", id)
	d.UI.Plain("")
	mentalModelDetail(d.UI, out)
	return nil
}

// mentalModelMergedTrigger reads the model back and builds the PATCH trigger:
// the stored trigger (or the API default) with the flags applied on top, then
// the consolidation/cron mutual exclusion resolved. The four members the
// request type serializes unconditionally are always present, so a
// trigger-bearing update never resets them to defaults.
func mentalModelMergedTrigger(ctx context.Context, d *Deps, bank, id string, f *mmUpdateFlags) (map[string]any, error) {
	mode := ""
	if f.mode != "" {
		parsed, err := parseTriggerMode(f.mode)
		if err != nil {
			return nil, err
		}
		mode = parsed
	}
	tagsMatch := ""
	if f.tagsMatch.set && strings.TrimSpace(f.tagsMatch.value) != "" {
		parsed, err := parseTagsMatch(f.tagsMatch.value)
		if err != nil {
			return nil, err
		}
		tagsMatch = parsed
	}
	var stored mmModel
	if err := d.Client.Do(ctx, "GET", mentalModelPath(bank, id), nil, &stored); err != nil {
		return nil, err
	}
	trig := map[string]any{}
	for key, value := range stored.Trigger {
		trig[key] = value
	}
	if _, ok := trig["mode"]; !ok {
		trig["mode"] = "full"
	}
	if _, ok := trig["refresh_after_consolidation"]; !ok {
		trig["refresh_after_consolidation"] = false
	}
	if _, ok := trig["exclude_mental_models"]; !ok {
		trig["exclude_mental_models"] = false
	}
	if _, ok := trig["keep_trace"]; !ok {
		trig["keep_trace"] = false
	}
	if mode != "" {
		trig["mode"] = mode
	}
	if f.consol.set {
		trig["refresh_after_consolidation"] = f.consol.value
	}
	if f.cron.set {
		if strings.TrimSpace(f.cron.value) == "" {
			trig["refresh_cron"] = nil
		} else {
			trig["refresh_cron"] = f.cron.value
		}
	}
	if f.minInterval.set {
		trig["min_refresh_interval_seconds"] = f.minInterval.value
	}
	if f.tagsMatch.set {
		if tagsMatch == "" {
			trig["tags_match"] = nil
		} else {
			trig["tags_match"] = tagsMatch
		}
	}
	if f.keepTrace.set {
		trig["keep_trace"] = f.keepTrace.value
	}
	if f.excludeMM.set {
		trig["exclude_mental_models"] = f.excludeMM.value
	}

	consolidation, _ := trig["refresh_after_consolidation"].(bool)
	cronSet := false
	if raw, ok := trig["refresh_cron"]; ok && raw != nil {
		if s, isStr := raw.(string); isStr {
			cronSet = strings.TrimSpace(s) != ""
		} else {
			cronSet = true
		}
	}
	if consolidation && cronSet {
		switch {
		case f.cron.set && !f.consol.set:
			trig["refresh_after_consolidation"] = false
		case f.consol.set && !f.cron.set:
			trig["refresh_cron"] = nil
		case f.cron.set && f.consol.set:
			return nil, NewCommandError("--trigger-refresh-after-consolidation and --trigger-refresh-cron are mutually exclusive")
		default:
			trig["refresh_after_consolidation"] = false
		}
	}
	return trig, nil
}

func mentalModelDelete(ctx context.Context, d *Deps, bank, id string, yes bool) error {
	prompt := fmt.Sprintf("Are you sure you want to delete mental model '%s'? This cannot be undone.", id)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	if err := d.Client.Do(ctx, "DELETE", mentalModelPath(bank, id), nil, nil); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		// The delete acknowledgement is synthesized client-side; the
		// server body is ignored.
		return printSyntheticSuccess(d.UI)
	}
	d.UI.Infof("Mental model '%s' deleted successfully", id)
	return nil
}

func mentalModelRefresh(ctx context.Context, d *Deps, bank, id string) error {
	var out mmSubmitOut
	path := mentalModelPath(bank, id) + "/refresh"
	if err := d.Client.Do(ctx, "POST", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Mental model refresh submitted. Operation ID: %s", out.OperationID)
	ui.Plain("  Status: %s", out.Status)
	ui.Plain("")
	ui.Plain("%s", ui.Dim("Use 'wenyuan operations get' to check the operation status."))
	return nil
}
