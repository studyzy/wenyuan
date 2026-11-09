package bankadmin

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// BankRenamer is the store capability a bulk bank re-key needs. The PostgreSQL
// backend applies the batch in one transaction; the in-process backend applies
// it bank by bank.
type BankRenamer interface {
	RenameBanks(ctx context.Context, renames map[string]string) error
}

// NormalizePlan is what a normalization would change.
type NormalizePlan struct {
	// Tenant is the identifier every bank is moved under.
	Tenant string
	// Renames maps each current bank identifier to its normalized one.
	Renames map[string]string
	// Kept lists the banks already spelled the normalized way.
	Kept []string
	// Applied reports whether the renames were performed.
	Applied bool
}

// NormalizedBankID spells a bank the way a deployment requires: every bank
// belongs to a tenant and is named "<tenant>::<name>".
//
// A bank imported from a reference implementation carries that instance's own
// prefix — most often `coding-agent::<repo>` — and some banks carry no prefix
// at all. Both are moved under the deployment's tenant; a bank already under it
// keeps its name, so running this twice changes nothing.
func NormalizedBankID(tenant, bankID string) string {
	tenant = strings.TrimSpace(tenant)
	prefix := tenant + "::"
	if strings.HasPrefix(bankID, prefix) {
		return bankID
	}
	if index := strings.Index(bankID, "::"); index >= 0 {
		return prefix + bankID[index+2:]
	}
	return prefix + bankID
}

// PlanNormalizeBanks computes the renames a normalization would apply to the
// banks a store holds.
func PlanNormalizeBanks(ctx context.Context, store storage.Store, tenant string) (NormalizePlan, error) {
	if strings.TrimSpace(tenant) == "" {
		return NormalizePlan{}, NewUsageError("a tenant identifier is required")
	}
	banks, err := listAllBanks(ctx, store)
	if err != nil {
		return NormalizePlan{}, err
	}
	plan := NormalizePlan{Tenant: tenant, Renames: map[string]string{}}
	seen := map[string]bool{}
	for _, bank := range banks {
		target := NormalizedBankID(tenant, bank.BankID)
		if target == bank.BankID {
			plan.Kept = append(plan.Kept, bank.BankID)
			continue
		}
		if seen[target] {
			return NormalizePlan{}, fmt.Errorf("bank %q would take the identifier %q, which another bank already normalizes to", bank.BankID, target)
		}
		seen[target] = true
		plan.Renames[bank.BankID] = target
	}
	sort.Strings(plan.Kept)
	return plan, nil
}

// listAllBanks reads every bank, one page at a time: a deployment can hold more
// banks than one list response carries, and a normalization must cover them
// all.
func listAllBanks(ctx context.Context, store storage.Store) ([]storage.Bank, error) {
	const pageSize = 500
	all := make([]storage.Bank, 0, pageSize)
	for offset := 0; ; offset += pageSize {
		page, err := store.ListBanks(ctx, storage.BankQuery{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		all = append(all, page.Banks...)
		if len(page.Banks) < pageSize || len(all) >= page.Total {
			return all, nil
		}
	}
}

// RunNormalizeBanks rewrites every bank identifier under the tenant prefix.
//
// A normalized identifier can be longer than the bank-id limit when the imported
// name was already long; that bank is reported rather than half-moved, because
// the store refuses an invalid identifier in the middle of the batch.
func RunNormalizeBanks(ctx context.Context, store storage.Store, tenant string, dryRun bool, out io.Writer) (NormalizePlan, error) {
	plan, err := PlanNormalizeBanks(ctx, store, tenant)
	if err != nil {
		return NormalizePlan{}, err
	}
	for source, target := range plan.Renames {
		if !storage.ValidBankID(target) {
			return NormalizePlan{}, fmt.Errorf("bank %q cannot be normalized: %q is not a valid bank identifier", source, target)
		}
	}
	if dryRun {
		reportNormalize(out, plan, "would rename")
		return plan, nil
	}
	if len(plan.Renames) == 0 {
		reportNormalize(out, plan, "nothing to rename; already")
		return plan, nil
	}
	renamer, ok := store.(BankRenamer)
	if !ok {
		return NormalizePlan{}, fmt.Errorf("this backend cannot rename banks in bulk")
	}
	if err := renamer.RenameBanks(ctx, plan.Renames); err != nil {
		return NormalizePlan{}, err
	}
	plan.Applied = true
	reportNormalize(out, plan, "renamed")
	return plan, nil
}

// reportNormalize prints the plan in a stable order: one line per bank moved,
// then the counts.
func reportNormalize(out io.Writer, plan NormalizePlan, verb string) {
	sources := make([]string, 0, len(plan.Renames))
	for source := range plan.Renames {
		sources = append(sources, source)
	}
	sort.Strings(sources)
	for _, source := range sources {
		_, _ = fmt.Fprintf(out, "  %s %q -> %q\n", verb, source, plan.Renames[source])
	}
	_, _ = fmt.Fprintf(out, "%s: %d bank(s) %s, %d already under %q\n",
		plan.Tenant, len(plan.Renames), verb, len(plan.Kept), plan.Tenant)
}
