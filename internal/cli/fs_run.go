package cli

// The fs refresh loop shared by the foreground mount and the hidden run
// daemon entrypoint, plus its stamped log format.

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/studyzy/wenyuan/internal/cli/fsengine"
)

// fsLogStamp renders one log line's leading timestamp, local time.
func fsLogStamp() string {
	return time.Now().Format("2006-01-02 15:04:05")
}

// fsLogf writes one stamped log line.
func fsLogf(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, "[%s] %s\n", fsLogStamp(), fmt.Sprintf(format, args...))
}

// fsSignalStop installs SIGINT/SIGTERM handlers and returns a channel the
// loop watches; it closes on the first signal.
func fsSignalStop(ctx context.Context) <-chan struct{} {
	stop := make(chan struct{})
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		defer signal.Stop(ch)
		select {
		case <-ch:
			close(stop)
		case <-ctx.Done():
		}
	}()
	return stop
}

// fsRunLoop runs the mirror loop until stop closes or ctx is done: one pass
// immediately, then the interval slept in 100 ms slices so a stop signal is
// honored promptly. The log lines are the daemon.log contract.
func fsRunLoop(ctx context.Context, w io.Writer, r fsResolved, stop <-chan struct{}, sleep func(time.Duration)) error {
	if sleep == nil {
		sleep = time.Sleep
	}
	fsLogf(w, "mounting bank %q at %s (every %ds, %s)", r.BankID, r.Dir, r.Interval, r.APIURL)
	fetcher := fsFetcher(r)
	opts := fsOptions(r)
	stopped := func() bool {
		select {
		case <-stop:
			return true
		default:
		}
		select {
		case <-ctx.Done():
			return true
		default:
			return false
		}
	}
	for !stopped() {
		res, err := fsengine.Sync(ctx, fetcher, opts)
		if err != nil {
			fsLogf(w, "sync failed: %s", err)
		} else {
			line := fmt.Sprintf("synced %d pages / %d folders — %d updated, %d unchanged, %d removed",
				res.Pages, res.Folders, res.Written, res.Unchanged, res.Removed)
			if res.Reverted > 0 {
				line += fmt.Sprintf(", %d reverted", res.Reverted)
			}
			fsLogf(w, "%s", line)
		}
		for i := 0; i < r.Interval*10 && !stopped(); i++ {
			sleep(100 * time.Millisecond)
		}
	}
	fsLogf(w, "mount stopped")
	return nil
}

func registerFSRun(fs *SubCmd) {
	fc := fsBindCommon(fs)
	fs.Action(func(ctx context.Context, d *Deps) error {
		dir, err := fsDirArg(d)
		if err != nil {
			return err
		}
		r := fsResolve(d, dir, fc)
		if err := fsRequireBank(r); err != nil {
			return err
		}
		if err := fsSaveConfig(r); err != nil {
			return err
		}
		// The daemon's stdout is redirected into daemon.log by its
		// spawner, so the loop logs there. Signals stop it.
		return fsRunLoop(ctx, d.UI.Stdout, r, fsSignalStop(ctx), nil)
	})
}
