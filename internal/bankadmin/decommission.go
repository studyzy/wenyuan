package bankadmin

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/studyzy/wenyuan/internal/storage"
)

// DecommissionFlags is the parsed `decommission-worker` command surface.
type DecommissionFlags struct {
	// Worker is the worker identifier whose claims to release.
	Worker string
	// Yes skips the confirmation prompt.
	Yes bool
}

// ParseDecommissionWorker parses a `decommission-worker --worker ID` command.
func ParseDecommissionWorker(args []string) (DecommissionFlags, error) {
	fs := flag.NewFlagSet("decommission-worker", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags DecommissionFlags
	fs.StringVar(&flags.Worker, "worker", "", "worker identifier whose claims to release")
	fs.StringVar(&flags.Worker, "w", "", "worker identifier whose claims to release (short)")
	fs.BoolVar(&flags.Yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&flags.Yes, "y", false, "skip the confirmation prompt (short)")
	positional, err := parsePositionals(fs, args)
	if err != nil {
		return DecommissionFlags{}, err
	}
	worker, err := resolveRequired(positional, flags.Worker, "a worker identifier")
	if err != nil {
		return DecommissionFlags{}, err
	}
	flags.Worker = worker
	return flags, nil
}

// DecommissionWorkersFlags is the parsed `decommission-workers` surface.
type DecommissionWorkersFlags struct {
	// Yes skips the confirmation prompt.
	Yes bool
}

// ParseDecommissionWorkers parses a `decommission-workers` command.
func ParseDecommissionWorkers(args []string) (DecommissionWorkersFlags, error) {
	fs := flag.NewFlagSet("decommission-workers", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags DecommissionWorkersFlags
	fs.BoolVar(&flags.Yes, "yes", false, "skip the confirmation prompt")
	fs.BoolVar(&flags.Yes, "y", false, "skip the confirmation prompt (short)")
	if err := fs.Parse(args); err != nil {
		return DecommissionWorkersFlags{}, err
	}
	return flags, nil
}

// RunDecommissionWorker releases every operation a worker still holds back to
// the claimable pool, so another worker can pick the work up immediately. It is
// what an operator runs after a worker crashed or was removed without a
// graceful shutdown.
func RunDecommissionWorker(ctx context.Context, store storage.Store, flags DecommissionFlags, in io.Reader, out io.Writer) (int, error) {
	if !flags.Yes {
		prompt := fmt.Sprintf("This will release all tasks owned by worker '%s' back to pending. Continue?", flags.Worker)
		if !Confirm(in, out, prompt) {
			return 0, ErrAborted
		}
	}
	workerID := flags.Worker
	released, err := releaseProcessing(ctx, store, workerID)
	if err != nil {
		return released, err
	}
	if released > 0 {
		_, _ = fmt.Fprintf(out, "released %d task(s) from worker %q\n", released, workerID)
	} else {
		_, _ = fmt.Fprintf(out, "no tasks found for worker %q\n", workerID)
	}
	return released, nil
}

// RunDecommissionWorkers releases every operation held by any worker, for the
// case where several workers were lost and each is unknown.
func RunDecommissionWorkers(ctx context.Context, store storage.Store, flags DecommissionWorkersFlags, in io.Reader, out io.Writer) (int, error) {
	if !flags.Yes {
		if !Confirm(in, out, "This will release ALL processing tasks from ALL workers back to pending. Continue?") {
			return 0, ErrAborted
		}
	}
	released, err := releaseProcessing(ctx, store, "")
	if err != nil {
		return released, err
	}
	_, _ = fmt.Fprintf(out, "released %d task(s) from all workers\n", released)
	return released, nil
}

// releaseProcessing hands every processing operation held by the named worker
// (or by any worker when workerID is empty) back to the claimable pool. It
// reuses the store's own release path rather than writing a second one, so a
// release here has exactly the semantics a graceful shutdown relies on.
func releaseProcessing(ctx context.Context, store storage.Store, workerID string) (int, error) {
	released := 0
	for {
		page, err := store.ListOperations(ctx, storage.OperationQuery{
			Status:   string(storage.StatusProcessing),
			WorkerID: workerID,
			Limit:    1000,
		})
		if err != nil {
			return released, err
		}
		if len(page.Operations) == 0 {
			return released, nil
		}
		// Releasing removes each operation from the processing set, so the next
		// listing returns the remainder and this always terminates.
		for _, op := range page.Operations {
			if err := store.ReleaseOperationClaim(ctx, op.OperationID, op.WorkerID); err != nil {
				return released, err
			}
			released++
		}
	}
}
