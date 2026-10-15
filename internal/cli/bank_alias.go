package cli

// The `bank alias` sub-group: list, add, primary and remove.

import (
	"context"
	"encoding/json"
	"fmt"
)

// registerBankAlias wires the alias sub-group.
func registerBankAlias(bank *SubCmd) {
	alias := bank.Sub("alias", "manage bank aliases")

	list := alias.Sub("list", "list the aliases that reach a bank")
	list.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank alias list", d.Args, 1); err != nil {
			return err
		}
		return bankAliasList(ctx, d, d.Args[0])
	})

	add := alias.Sub("add", "add an alias that reaches a bank")
	add.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank alias add", d.Args, 2); err != nil {
			return err
		}
		return bankAliasAdd(ctx, d, d.Args[0], d.Args[1])
	})

	primary := alias.Sub("primary", "make an alias the displayed name")
	primaryClear := primary.Flags().Bool("clear", false, "show the bank under its own id again")
	primary.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank alias primary", d.Args, 2); err != nil {
			return err
		}
		return bankAliasPrimary(ctx, d, d.Args[0], d.Args[1], *primaryClear)
	})

	remove := alias.Sub("remove", "remove an alias")
	removeYes := remove.Flags().Bool("yes", false, "skip the confirmation prompt")
	remove.Flags().BoolVar(removeYes, "y", false, "skip the confirmation prompt (short)")
	remove.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank alias remove", d.Args, 2); err != nil {
			return err
		}
		return bankAliasRemove(ctx, d, d.Args[0], d.Args[1], *removeYes)
	})
}

// bankAliasesResponse is the alias listing, decoded for the pretty branches;
// machine output passes the raw payload through. All four alias routes
// answer with it.
type bankAliasesResponse struct {
	BankID  string           `json:"bank_id"`
	Aliases []bankAliasEntry `json:"aliases"`
}

type bankAliasEntry struct {
	Alias   string `json:"alias"`
	Primary bool   `json:"primary"`
}

// bankAliasList implements `bank alias list`. The display uses the bank_id
// from the response, not the argument.
func bankAliasList(ctx context.Context, d *Deps, bankID string) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/aliases"), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var resp bankAliasesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	if len(resp.Aliases) == 0 {
		d.UI.Infof("Bank '%s' has no aliases; it is reachable only by its own id", resp.BankID)
		return nil
	}
	d.UI.Infof("Aliases that also reach '%s':", resp.BankID)
	for _, a := range resp.Aliases {
		if a.Primary {
			d.UI.Plain("  %s  (shown)", a.Alias)
		} else {
			d.UI.Plain("  %s", a.Alias)
		}
	}
	return nil
}

// bankAliasAdd implements `bank alias add`: adding never changes the
// displayed id, so primary is always false.
func bankAliasAdd(ctx context.Context, d *Deps, bankID, alias string) error {
	body := map[string]any{"alias": alias, "primary": false}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "POST", bankPath(bankID, "/aliases"), body, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var resp bankAliasesResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	d.UI.Infof("'%s' now reaches bank '%s'", alias, resp.BankID)
	return nil
}

// bankAliasPrimary implements `bank alias primary`. The --clear flag
// inverts: without it the alias becomes primary, with it the bank goes back
// to being shown under its own id.
func bankAliasPrimary(ctx context.Context, d *Deps, bankID, alias string, clear bool) error {
	primary := !clear
	body := map[string]any{"primary": primary}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "PATCH", bankPath(bankID, "/aliases/"+alias), body, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	if primary {
		d.UI.Infof("Bank '%s' is now shown as '%s' (its id is still '%s')", bankID, alias, bankID)
	} else {
		d.UI.Infof("Bank '%s' is shown under its own id again", bankID)
	}
	return nil
}

// bankAliasRemove implements `bank alias remove`.
func bankAliasRemove(ctx context.Context, d *Deps, bankID, alias string, yes bool) error {
	prompt := fmt.Sprintf("Remove alias '%s'? Every client still calling it will stop reaching bank '%s'.", alias, bankID)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "DELETE", bankPath(bankID, "/aliases/"+alias), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	d.UI.Infof("'%s' no longer reaches bank '%s'", alias, bankID)
	return nil
}
