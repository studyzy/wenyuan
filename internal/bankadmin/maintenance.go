package bankadmin

// The repair and status commands: per-bank vector index coverage repair, and
// the report of the work the worker fleet is currently holding.

import (
	"context"
	"flag"
	"fmt"
	"io"
	"sort"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// RepairFlags selects what a repair targets.
type RepairFlags struct {
	// BankID narrows the repair to one bank; AllBanks repairs every bank.
	BankID string
	// AllBanks repairs every bank in the schema.
	AllBanks bool
	// DryRun reports the planned index changes without applying them.
	DryRun bool
	// VectorIndexMinRows is the row count at which a (bank, fact_type)
	// partition earns its own vector index; zero leaves coverage store-owned.
	VectorIndexMinRows int
}

// ParseRepair reads the repair command flags.
//
// Exactly one of --bank or --all is required, as the reference requires: a bare
// run would otherwise silently sweep every bank, which is a much larger
// operation than the operator asked for.
func ParseRepair(args []string) (RepairFlags, error) {
	fs := flag.NewFlagSet("repair-bank", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags RepairFlags
	fs.StringVar(&flags.BankID, "bank", "", "bank to repair; mutually exclusive with --all")
	fs.StringVar(&flags.BankID, "b", "", "bank to repair (short)")
	fs.BoolVar(&flags.AllBanks, "all", false, "repair every bank")
	fs.BoolVar(&flags.DryRun, "dry-run", false, "report what would be repaired without creating or dropping any index")
	fs.IntVar(&flags.VectorIndexMinRows, "vector-index-min-rows", 0,
		"row count at which a (bank, fact_type) partition earns its own vector index; 0 uses the configured value")
	if err := fs.Parse(args); err != nil {
		return RepairFlags{}, err
	}
	if (flags.BankID != "") == flags.AllBanks {
		return RepairFlags{}, NewUsageError("pass exactly one of --bank <id> or --all")
	}
	return flags, nil
}

// repairCounters accumulates one sweep's outcome in the reference's vocabulary.
type repairCounters struct {
	banks          int
	alreadyPresent int
	created        int
	dropped        int
	wouldCreate    int
	wouldDrop      int
	failed         int
	failedNames    []string
}

// repairCounters derives the counters from a bank's partition states, its plan
// and — when the plan was applied — the actions actually applied.
func newRepairCounters() *repairCounters { return &repairCounters{} }

// add folds one bank's outcome in. applied carries the actions that ran; it is
// nil for a dry run, where the plan itself is the outcome.
func (c *repairCounters) add(states []storage.BankVectorIndexState, plan, applied []storage.BankVectorIndexAction, minRows int, dryRun bool) {
	c.banks++
	for _, state := range states {
		if state.Present && !state.Invalid && state.Rows >= int64(minRows) {
			c.alreadyPresent++
		}
	}
	actions := applied
	if dryRun {
		actions = plan
	}
	for _, action := range actions {
		switch {
		case action.Create && dryRun:
			c.wouldCreate++
		case action.Create:
			c.created++
		case dryRun:
			c.wouldDrop++
		default:
			c.dropped++
		}
	}
}

// String renders the reference's aggregate line.
func (c *repairCounters) String() string {
	return fmt.Sprintf("Done: %d bank(s) scanned, %d already present, %d created, %d dropped, "+
		"%d to-create (dry-run), %d to-drop (dry-run), %d failed",
		c.banks, c.alreadyPresent, c.created, c.dropped, c.wouldCreate, c.wouldDrop, c.failed)
}

// RunRepair brings every targeted bank's per-(bank, fact_type) vector index
// coverage to the plan: a partition holding at least the threshold earns its own
// index, one below it loses the index it held, and an index left behind by a
// deleted bank is dropped. With --dry-run it reports the same plan and changes
// nothing.
//
// A deployment whose vector indexes are store-owned — no per-bank indexes at
// all, or a threshold of zero where the indexes are created and dropped with
// their bank — has nothing to repair, which is reported rather than treated as
// a failure.
func RunRepair(ctx context.Context, store storage.Store, flags RepairFlags, out io.Writer) error {
	planner, planOK := store.(storage.VectorIndexPlanner)
	reconciler, applyOK := store.(storage.VectorIndexReconciler)
	if !planOK || !applyOK {
		_, _ = fmt.Fprintln(out, "Configured vector backend does not use per-bank vector indexes - nothing to repair.")
		return nil
	}
	if flags.VectorIndexMinRows <= 0 {
		_, _ = fmt.Fprintln(out, "Configured vector backend does not use per-bank vector indexes - nothing to repair.")
		return nil
	}

	target := fmt.Sprintf("bank '%s'", flags.BankID)
	if flags.AllBanks {
		target = "all banks"
	}
	_, _ = fmt.Fprintf(out, "Repairing per-bank vector indexes for %s...\n", target)
	if flags.DryRun {
		_, _ = fmt.Fprintln(out, "Dry run: no indexes will be created or dropped.")
	}

	banks, err := repairTargets(ctx, store, flags)
	if err != nil {
		return err
	}

	counters := newRepairCounters()
	for _, bankID := range banks {
		if err := repairOneBank(ctx, planner, reconciler, bankID, flags, counters, out); err != nil {
			return err
		}
	}

	if flags.AllBanks {
		orphans, err := planner.OrphanedVectorIndexes(ctx)
		if err != nil {
			_, _ = fmt.Fprintf(out, "  orphan collection failed: %v\n", err)
		} else if len(orphans) > 0 {
			if flags.DryRun {
				_, _ = fmt.Fprintf(out, "  %d orphaned index(es) to drop (dry-run): %v\n", len(orphans), orphans)
			} else if err := planner.DropVectorIndexes(ctx, orphans); err != nil {
				_, _ = fmt.Fprintf(out, "  dropping %d orphaned index(es) failed: %v\n", len(orphans), err)
				counters.failed += len(orphans)
				counters.failedNames = append(counters.failedNames, orphans...)
			} else {
				_, _ = fmt.Fprintf(out, "  %d orphaned index(es) dropped (no matching bank)\n", len(orphans))
			}
		}
	}

	_, _ = fmt.Fprintln(out, counters.String())
	if len(counters.failedNames) > 0 {
		return fmt.Errorf("failed indexes (retry with a re-run): %v", counters.failedNames)
	}
	return nil
}

// repairOneBank plans and, unless this is a dry run, applies one bank's coverage
// change, folding the outcome into the sweep counters.
func repairOneBank(ctx context.Context, planner storage.VectorIndexPlanner, reconciler storage.VectorIndexReconciler,
	bankID string, flags RepairFlags, counters *repairCounters, out io.Writer) error {
	states, err := planner.BankVectorIndexStates(ctx, bankID)
	if err != nil {
		return fmt.Errorf("read vector index coverage for bank %s: %w", bankID, err)
	}
	plan := storage.PlanBankVectorIndexes(states, flags.VectorIndexMinRows)
	if flags.DryRun {
		counters.add(states, plan, nil, flags.VectorIndexMinRows, true)
		if len(plan) > 0 {
			_, _ = fmt.Fprintf(out, "  bank %q: %d change(s) planned: %s\n", bankID, len(plan), describeActions(plan))
		}
		return nil
	}
	applied, err := reconciler.ReconcileBankVectorIndexes(ctx, bankID, flags.VectorIndexMinRows)
	counters.add(states, plan, applied, flags.VectorIndexMinRows, false)
	if err != nil {
		counters.failed++
		counters.failedNames = append(counters.failedNames, bankID)
		_, _ = fmt.Fprintf(out, "  bank %q: %v\n", bankID, err)
		return nil
	}
	if len(applied) > 0 {
		_, _ = fmt.Fprintf(out, "  bank %q: %d change(s) applied: %s\n", bankID, len(applied), describeActions(applied))
	}
	return nil
}

// describeActions renders a plan as create/drop index names, ordered.
func describeActions(actions []storage.BankVectorIndexAction) string {
	created := make([]string, 0, len(actions))
	dropped := make([]string, 0, len(actions))
	for _, action := range actions {
		if action.Create {
			created = append(created, action.IndexName)
		} else {
			dropped = append(dropped, action.IndexName)
		}
	}
	sort.Strings(created)
	sort.Strings(dropped)
	switch {
	case len(created) > 0 && len(dropped) > 0:
		return fmt.Sprintf("create %v, drop %v", created, dropped)
	case len(created) > 0:
		return fmt.Sprintf("create %v", created)
	default:
		return fmt.Sprintf("drop %v", dropped)
	}
}

// repairTargets resolves the banks a sweep visits.
func repairTargets(ctx context.Context, store storage.Store, flags RepairFlags) ([]string, error) {
	if !flags.AllBanks {
		if _, err := store.GetBank(ctx, flags.BankID); err != nil {
			return nil, err
		}
		return []string{flags.BankID}, nil
	}
	return allBanks(ctx, store)
}

// allBanks pages the bank list so a sweep is not capped by one page.
func allBanks(ctx context.Context, store storage.Store) ([]string, error) {
	const pageSize = 1000
	var ids []string
	for offset := 0; ; offset += pageSize {
		page, err := store.ListBanks(ctx, storage.BankQuery{Limit: pageSize, Offset: offset})
		if err != nil {
			return nil, err
		}
		for _, bank := range page.Banks {
			ids = append(ids, bank.BankID)
		}
		if len(page.Banks) < pageSize {
			return ids, nil
		}
	}
}

// RunWorkerStatus reports the tasks the worker fleet is currently holding:
// every operation in `processing`, grouped by the worker that claimed it, with
// how long it has been running and how long ago it last reported. An empty queue
// is reported rather than printed as an empty table.
func RunWorkerStatus(ctx context.Context, store storage.Store, out io.Writer) error {
	operations, err := processingOperations(ctx, store)
	if err != nil {
		return err
	}
	if len(operations) == 0 {
		_, _ = fmt.Fprintln(out, "No processing tasks found")
		return nil
	}

	byWorker := map[string][]storage.Operation{}
	order := make([]string, 0)
	for _, op := range operations {
		worker := op.WorkerID
		if worker == "" {
			worker = "unknown"
		}
		if _, seen := byWorker[worker]; !seen {
			order = append(order, worker)
		}
		byWorker[worker] = append(byWorker[worker], op)
	}
	sort.Strings(order)

	now := time.Now().UTC()
	_, _ = fmt.Fprintf(out, "Processing tasks across %d worker(s):\n\n", len(order))
	for _, worker := range order {
		tasks := byWorker[worker]
		_, _ = fmt.Fprintf(out, "Worker: %s (%d task(s))\n", worker, len(tasks))
		for _, op := range tasks {
			_, _ = fmt.Fprintf(out, "  %-8s  %-20s bank=%s  running=%s  last_update=%s ago\n",
				shortOperationID(op.OperationID), op.OperationType, op.BankID,
				elapsed(op.ClaimedAt, now), elapsed(&op.UpdatedAt, now))
		}
		_, _ = fmt.Fprintln(out)
	}
	return nil
}

// processingOperations pages every operation the fleet is holding.
func processingOperations(ctx context.Context, store storage.Store) ([]storage.Operation, error) {
	const pageSize = 1000
	var operations []storage.Operation
	for offset := 0; ; offset += pageSize {
		page, err := store.ListOperations(ctx, storage.OperationQuery{
			Status: string(storage.StatusProcessing),
			Limit:  pageSize,
			Offset: offset,
		})
		if err != nil {
			return nil, err
		}
		operations = append(operations, page.Operations...)
		if len(page.Operations) < pageSize {
			return operations, nil
		}
	}
}

// shortOperationID renders the first eight characters of an operation id, the
// reference's table width.
func shortOperationID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}

// elapsed renders how long ago a timestamp was, in the reference's
// `[[D days, ]H:]MM:SS` shape. A nil timestamp — a claim never recorded, or an
// update never written — renders as "never".
func elapsed(at *time.Time, now time.Time) string {
	if at == nil {
		return "never"
	}
	d := now.Sub(*at)
	if d < 0 {
		d = 0
	}
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	minutes := d / time.Minute
	d -= minutes * time.Minute
	seconds := d / time.Second
	clock := fmt.Sprintf("%d:%02d:%02d", hours, minutes, seconds)
	if days > 0 {
		return fmt.Sprintf("%d days, %s", days, clock)
	}
	return clock
}
