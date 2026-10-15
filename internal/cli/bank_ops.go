package cli

// Bank async-operation commands: consolidate (with --wait polling),
// clear-observations and consolidation-recover.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// registerBankOps wires the operation commands.
func registerBankOps(bank *SubCmd) {
	consolidate := bank.Sub("consolidate", "trigger memory consolidation")
	consolidateWait := consolidate.Flags().Bool("wait", false, "poll for completion")
	consolidateInterval := consolidate.Flags().Int("poll-interval", 10, "seconds between polls when --wait is given")
	consolidate.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank consolidate", d.Args, 1); err != nil {
			return err
		}
		return bankConsolidate(ctx, d, d.Args[0], *consolidateWait, *consolidateInterval)
	})

	clearObs := bank.Sub("clear-observations", "clear all observations of a bank")
	clearYes := clearObs.Flags().Bool("yes", false, "skip the confirmation prompt")
	clearObs.Flags().BoolVar(clearYes, "y", false, "skip the confirmation prompt (short)")
	clearObs.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank clear-observations", d.Args, 1); err != nil {
			return err
		}
		return bankClearObservations(ctx, d, d.Args[0], *clearYes)
	})

	recoverCmd := bank.Sub("consolidation-recover", "retry failed consolidation work")
	recoverCmd.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank consolidation-recover", d.Args, 1); err != nil {
			return err
		}
		return bankConsolidationRecover(ctx, d, d.Args[0])
	})
}

// consolidationResponse is the trigger answer, decoded for the pretty
// branches; machine output passes the raw payload through.
type consolidationResponse struct {
	OperationID  string `json:"operation_id"`
	Deduplicated bool   `json:"deduplicated"`
}

// bankConsolidate triggers consolidation and optionally polls it to
// completion. Machine formats print only the trigger response; the polling
// still runs but prints nothing.
func bankConsolidate(ctx context.Context, d *Deps, bankID string, wait bool, pollInterval int) error {
	body := map[string]any{"observation_scopes": nil}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "POST", bankPath(bankID, "/consolidate"), body, &raw); err != nil {
		return err
	}
	quiet := d.Format.IsMachine()
	if quiet {
		if err := renderRaw(d, raw); err != nil {
			return err
		}
		if wait {
			return pollConsolidation(ctx, d, bankID, responseOperationID(raw), pollInterval, quiet)
		}
		return nil
	}
	var resp consolidationResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Infof("Consolidation triggered")
	ui.Plain("  Operation ID: %s", resp.OperationID)
	if resp.Deduplicated {
		ui.Plain("  Note: Reusing existing pending consolidation task")
	}
	if !wait {
		ui.Plain("")
		ui.Plain("Use --wait to poll for completion, or 'wenyuan-admin operation get' to check status.")
		return nil
	}
	ui.Plain("")
	ui.Plain("Polling every %ds for completion...", pollInterval)
	return pollConsolidation(ctx, d, bankID, resp.OperationID, pollInterval, quiet)
}

// responseOperationID reads the operation_id field without a second decode
// pass over the whole payload.
func responseOperationID(raw json.RawMessage) string {
	var resp consolidationResponse
	_ = json.Unmarshal(raw, &resp)
	return resp.OperationID
}

// pollConsolidation polls the operation list until the operation completes,
// fails or disappears from the listing. Only completed counts as success; a
// failed operation is an error, and a vanished one stops with a warning.
// The sleep is fixed with no backoff and the loop is unbounded, matching
// the documented behavior.
func pollConsolidation(ctx context.Context, d *Deps, bankID, opID string, intervalSec int, quiet bool) error {
	start := time.Now()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(intervalSec) * time.Second):
		}
		var list opListOut
		if err := d.Client.Do(ctx, "GET", fmt.Sprintf("%s/%s/operations", batchDBase, bankID), nil, &list); err != nil {
			return err
		}
		var op *opEntry
		for i := range list.Operations {
			e := &list.Operations[i]
			if e.ID == opID || (e.OperationID != nil && *e.OperationID == opID) {
				op = e
				break
			}
		}
		elapsed := int(time.Since(start).Seconds())
		if op == nil {
			if !quiet {
				d.UI.Warningf("Operation %s not found in list", opID)
			}
			return nil
		}
		switch op.Status {
		case "completed":
			if !quiet {
				d.UI.Infof("Consolidation completed (%ds)", elapsed)
			}
			return nil
		case "failed":
			message := "Unknown error"
			if op.ErrorMessage != nil && *op.ErrorMessage != "" {
				message = *op.ErrorMessage
			}
			return fmt.Errorf("consolidation failed: %s", message)
		default:
			if !quiet {
				d.UI.Plain("  ⏳ %s (%ds elapsed)", op.Status, elapsed)
			}
		}
	}
}

// bankClearObservations implements `bank clear-observations`.
func bankClearObservations(ctx context.Context, d *Deps, bankID string, yes bool) error {
	prompt := fmt.Sprintf("Are you sure you want to clear all observations for bank '%s'? This cannot be undone.", bankID)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "DELETE", bankPath(bankID, "/observations"), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var body deleteResponse
	if err := json.Unmarshal(raw, &body); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	if !body.Success {
		d.UI.Errorf("Failed to clear observations")
		return nil
	}
	d.UI.Infof("Observations cleared for bank '%s'", bankID)
	if body.DeletedCount != nil {
		d.UI.Plain("  Observations deleted: %d", *body.DeletedCount)
	}
	return nil
}

// bankConsolidationRecover implements `bank consolidation-recover`. Pretty
// mode appends the response as two-space-indented pretty JSON.
func bankConsolidationRecover(ctx context.Context, d *Deps, bankID string) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "POST", bankPath(bankID, "/consolidation/recover"), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	d.UI.Infof("Consolidation recovered for bank '%s'", bankID)
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	for _, line := range strings.Split(buf.String(), "\n") {
		d.UI.Plain("  %s", line)
	}
	return nil
}
