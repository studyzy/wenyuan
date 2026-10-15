package cli

// Bank lifecycle actions: list, create, update, name, mission and delete.

import (
	"context"
	"encoding/json"
	"fmt"
)

// registerBankCore wires the lifecycle commands onto the bank group.
func registerBankCore(bank *SubCmd) {
	registerBankList(bank)
	registerBankCreate(bank)
	registerBankUpdate(bank)

	name := bank.Sub("name", "set the bank display name")
	name.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank name", d.Args, 2); err != nil {
			return err
		}
		return bankName(ctx, d, d.Args[0], d.Args[1])
	})

	mission := bank.Sub("mission", "set the bank mission")
	mission.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank mission", d.Args, 2); err != nil {
			return err
		}
		return bankMission(ctx, d, d.Args[0], d.Args[1])
	})

	del := bank.Sub("delete", "delete a bank and all its data")
	delYes := del.Flags().Bool("yes", false, "skip the confirmation prompt")
	del.Flags().BoolVar(delYes, "y", false, "skip the confirmation prompt (short)")
	del.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank delete", d.Args, 1); err != nil {
			return err
		}
		return bankDelete(ctx, d, d.Args[0], *delYes)
	})
}

func registerBankList(bank *SubCmd) {
	list := bank.Sub("list", "list banks")
	list.Action(func(ctx context.Context, d *Deps) error {
		return bankList(ctx, d)
	})
}

func registerBankCreate(bank *SubCmd) {
	create := bank.Sub("create", "create or update a bank")
	wf := bindBankWriteFlags(create)
	create.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank create", d.Args, 1); err != nil {
			return err
		}
		wf.given = givenFlags(d.Flags)
		if err := wf.validate(); err != nil {
			return err
		}
		return bankCreate(ctx, d, d.Args[0], wf)
	})
}

func registerBankUpdate(bank *SubCmd) {
	update := bank.Sub("update", "update bank fields")
	wf := bindBankWriteFlags(update)
	update.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank update", d.Args, 1); err != nil {
			return err
		}
		wf.given = givenFlags(d.Flags)
		if !wf.anyGiven() {
			return NewCommandError("At least one field must be provided (--name, --mission, --skepticism, --literalism, --empathy)")
		}
		if err := wf.validate(); err != nil {
			return err
		}
		return bankUpdate(ctx, d, d.Args[0], wf)
	})
}

// bindBankWriteFlags registers the shared create/update flag set on cmd and
// returns the flag variables for the action to read.
func bindBankWriteFlags(cmd *SubCmd) *bankWriteFlags {
	f := cmd.Flags()
	name := f.String("name", "", "display name")
	f.StringVar(name, "n", "", "display name (short)")
	mission := f.String("mission", "", "mission statement")
	f.StringVar(mission, "m", "", "mission statement (short)")
	skepticism := f.Int("skepticism", 0, "skepticism trait (1-5)")
	literalism := f.Int("literalism", 0, "literalism trait (1-5)")
	empathy := f.Int("empathy", 0, "empathy trait (1-5)")
	return &bankWriteFlags{
		name:       name,
		mission:    mission,
		skepticism: skepticism,
		literalism: literalism,
		empathy:    empathy,
	}
}

// bankList walks the limit=100 pages — offset advancing by the fetched
// count — until the accumulated count reaches the reported total or a page
// comes back empty.
func bankList(ctx context.Context, d *Deps) error {
	var banks []bankListItem
	offset := 0
	for {
		var env bankListEnvelope
		path := fmt.Sprintf("%s?limit=100&offset=%d", batchDBase, offset)
		if err := d.Client.Do(ctx, "GET", path, nil, &env); err != nil {
			return err
		}
		if len(env.Banks) == 0 {
			break
		}
		banks = append(banks, env.Banks...)
		offset += len(env.Banks)
		if len(banks) >= env.Total {
			break
		}
	}
	if d.Format.IsMachine() {
		if banks == nil {
			banks = []bankListItem{}
		}
		return d.Format.Render(d.UI.Stdout, banks)
	}
	if len(banks) == 0 {
		d.UI.Warningf("No banks found")
		return nil
	}
	d.UI.Infof("Found %d bank(s)", len(banks))
	for _, b := range banks {
		d.UI.Plain("  - %s", b.BankID)
	}
	return nil
}

// bankCreate implements `bank create`, the create-or-update PUT.
func bankCreate(ctx context.Context, d *Deps, bankID string, wf *bankWriteFlags) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PUT", bankPath(bankID, ""), wf.body(), &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var profile bankProfileResponse
	if err := json.Unmarshal(raw, &profile); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	d.UI.Infof("Bank '%s' created successfully", bankID)
	d.UI.Plain("")
	renderDispositionView(d.UI, profileView(profile))
	return nil
}

// bankUpdate implements `bank update`, the field-update PATCH. The
// at-least-one-field rule is checked before any network traffic.
func bankUpdate(ctx context.Context, d *Deps, bankID string, wf *bankWriteFlags) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PATCH", bankPath(bankID, ""), wf.body(), &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var profile bankProfileResponse
	if err := json.Unmarshal(raw, &profile); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	d.UI.Infof("Bank '%s' updated successfully", bankID)
	d.UI.Plain("")
	renderDispositionView(d.UI, profileView(profile))
	return nil
}

// bankName implements `bank name`: a PUT whose body carries only the name,
// the remaining fields null.
func bankName(ctx context.Context, d *Deps, bankID, name string) error {
	body := map[string]any{"name": name, "mission": nil, "background": nil, "disposition": nil}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PUT", bankPath(bankID, ""), body, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var profile bankProfileResponse
	if err := json.Unmarshal(raw, &profile); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	shown := bankID
	if profile.Name != nil {
		shown = *profile.Name
	}
	d.UI.Infof("Bank name updated to '%s'", shown)
	return nil
}

// bankMission implements `bank mission`: a PATCH carrying only the mission.
func bankMission(ctx context.Context, d *Deps, bankID, mission string) error {
	body := map[string]any{"mission": mission}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PATCH", bankPath(bankID, ""), body, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var profile bankProfileResponse
	if err := json.Unmarshal(raw, &profile); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	d.UI.Infof("Mission updated successfully")
	d.UI.Plain("")
	text := ""
	if profile.Mission != nil {
		text = *profile.Mission
	}
	d.UI.Plain("%s", text)
	return nil
}

// bankDelete implements `bank delete`. A success=false body with HTTP 200
// prints the failure line on stderr but keeps the exit code 0.
func bankDelete(ctx context.Context, d *Deps, bankID string, yes bool) error {
	prompt := fmt.Sprintf("Are you sure you want to delete bank '%s' and ALL its data? This cannot be undone.", bankID)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "DELETE", bankPath(bankID, ""), nil, &raw); err != nil {
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
		d.UI.Errorf("Failed to delete bank")
		return nil
	}
	d.UI.Infof("Bank '%s' deleted successfully", bankID)
	if body.DeletedCount != nil {
		d.UI.Plain("  Items deleted: %d", *body.DeletedCount)
	}
	return nil
}
