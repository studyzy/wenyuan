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

// Rename moves a bank from source to target. The bank keeps its aliases and
// every row scoped to it: the store moves them in one operation rather than
// recreating an empty bank under the new identifier.
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
	if err := r.Store.RenameBank(ctx, source, target); err != nil {
		return RenamePlan{}, err
	}
	plan.Applied = true
	return plan, nil
}
