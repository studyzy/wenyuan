// Package bankadmin implements the administrative bank lifecycle operations:
// renaming a bank across its bank-scoped rows and exporting/importing a whole
// bank as an archive. It is PostgreSQL-oriented and stdlib-only.
package bankadmin

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// RenamePlan describes the effect of a bank rename.
type RenamePlan struct {
	// Source is the identifier being renamed from.
	Source string
	// Target is the identifier being renamed to.
	Target string
	// Rows is the number of bank rows that would move.
	Rows int
	// Aliases is the number of alias rows that would move.
	Aliases int
	// DryRun reports whether the plan was evaluated without applying it.
	DryRun bool
	// Applied reports whether the rename was performed.
	Applied bool
}

// Renamer renames a bank and reports the plan. When dryRun is true the plan is
// computed and returned without changing anything; otherwise the rename is
// applied and the plan reports the rows moved.
type Renamer struct {
	Store storage.Store
}

// Rename moves a bank from source to target. The bank keeps its aliases.
func (r Renamer) Rename(ctx context.Context, source, target string, dryRun bool) (RenamePlan, error) {
	if _, err := r.Store.GetBank(ctx, source); err != nil {
		return RenamePlan{}, err
	}
	if !storage.ValidBankID(target) {
		return RenamePlan{}, storage.ErrInvalidBankID
	}
	aliases, err := r.Store.ListAliases(ctx, source)
	if err != nil {
		return RenamePlan{}, err
	}
	plan := RenamePlan{Source: source, Target: target, Rows: 1, Aliases: len(aliases), DryRun: dryRun}
	if dryRun {
		return plan, nil
	}

	bank, err := r.Store.GetBank(ctx, source)
	if err != nil {
		return RenamePlan{}, err
	}
	if _, err := r.Store.CreateBank(ctx, withID(bank, target)); err != nil {
		return RenamePlan{}, err
	}
	// Remove the source (which drops its alias rows) before re-attaching the
	// aliases to the target, so the alias uniqueness rule is not violated.
	if _, err := r.Store.DeleteBank(ctx, source); err != nil {
		return RenamePlan{}, err
	}
	for _, alias := range aliases {
		if _, err := r.Store.AddAlias(ctx, target, alias.Alias, alias.Primary); err != nil {
			return RenamePlan{}, err
		}
	}
	plan.Applied = true
	return plan, nil
}

func withID(bank storage.Bank, id string) storage.Bank {
	bank.BankID = id
	if bank.Name == "" {
		bank.Name = id
	}
	return bank
}
