package cli

import (
	"context"
)

func mentalModelDryRun(ctx context.Context, d *Deps, bank, id string) error {
	var out mmDryRunOut
	path := mentalModelPath(bank, id) + "/dry-run-refresh"
	if err := d.Client.Do(ctx, "POST", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	if out.EffectiveMode != out.RequestedMode {
		ui.Plain("  Mode: %s → %s", out.RequestedMode, out.EffectiveMode)
	} else {
		ui.Plain("  Mode: %s", out.EffectiveMode)
	}
	if out.ModeFallbackReason != "" {
		ui.Plain("  Fell back: %s", out.ModeFallbackReason)
	}
	ui.Plain("  Outcome: %s", out.Outcome)
	ui.Plain("  Would save: %t", out.WouldPersist)
	ui.Plain("  Retrieved: %s", countSummary(out.Facts.Retrieved))
	ui.Plain("  Used: %s", countSummary(out.Facts.Used))
	if out.DeltaOperations != nil {
		ui.Plain("  Delta ops: %d applied, %d skipped",
			len(out.DeltaOperations.Applied), len(out.DeltaOperations.Skipped))
	}
	ui.Plain("  Duration: %d ms", out.DurationMS)
	for _, warning := range out.Warnings {
		ui.Warningf("%s", warning)
	}
	ui.Plain("")
	if out.Diff == "" {
		ui.Plain("No content change.")
	} else {
		ui.Plain("%s", out.Diff)
	}
	ui.Plain("")
	ui.Plain("%s", ui.Dim("Nothing was saved. Use 'mental-model refresh' to apply."))
	return nil
}

func mentalModelHistory(ctx context.Context, d *Deps, bank, id string) error {
	var out []mmHistoryEntry
	path := mentalModelPath(bank, id) + "/history"
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("History: %s", id)
	if len(out) == 0 {
		ui.Infof("No history entries found.")
		return nil
	}
	for i, entry := range out {
		if i > 0 {
			ui.Plain("")
		}
		ui.Plain("  Changed at: %s", entry.ChangedAt)
		previous := "(none)"
		if entry.PreviousContent != nil && *entry.PreviousContent != "" {
			previous = Truncate(*entry.PreviousContent, 80)
		}
		ui.Plain("  Previous: %s", previous)
	}
	return nil
}
