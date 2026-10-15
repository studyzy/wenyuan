package cli

// Daemon control for the fs family: the process hooks (injectable for
// tests), background start, stop, restart, unmount and log reading. The
// platform-specific primitives live in the build-tagged siblings.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/studyzy/wenyuan/internal/cli/fsengine"
)

// fsDaemonHooks bundles the process primitives the daemon control flows
// use. Tests replace this variable; the platform files set the defaults.
var fsHooks = fsDaemonHooks{
	alive:  fsProcessAlive,
	signal: fsSignal,
	spawn:  fsSpawn,
	sleep:  time.Sleep,
}

type fsDaemonHooks struct {
	// alive reports liveness with kill(pid, 0) semantics.
	alive func(pid int) bool
	// signal sends one signal to a pid.
	signal func(pid int, sig os.Signal) error
	// spawn re-executes exe in a new session and returns its pid without
	// waiting for it.
	spawn func(exe string, args []string, stdin, stdout, stderr *os.File) (int, error)
	// sleep parks the caller; injected to keep tests fast.
	sleep func(time.Duration)
}

func registerFSStart(fs *SubCmd) {
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
		return fsStartBackground(d.UI, r)
	})
}

// fsStartBackground starts the daemon unless one is already running. The
// parent saves the config before spawning so the re-executed child finds
// its bank, and writes the pid record after the spawn.
func fsStartBackground(ui *UI, r fsResolved) error {
	if rec, ok := fsengine.LoadPID(r.Dir); ok && fsHooks.alive(rec.PID) {
		ui.Plain("Already mounted at %s (daemon pid %d).", r.Dir, rec.PID)
		return nil
	}
	if err := fsSaveConfig(r); err != nil {
		return err
	}
	logFile, err := os.OpenFile(fsengine.LogPath(r.Dir), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer func() { _ = devNull.Close() }()
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	pid, err := fsHooks.spawn(exe, []string{"fs", "run", r.Dir}, devNull, logFile, logFile)
	if err != nil {
		return err
	}
	rec := fsengine.PIDRecord{
		PID:             pid,
		BankID:          r.BankID,
		IntervalSeconds: r.Interval,
		StartedAt:       fsengine.NowStamp(),
	}
	if err := fsengine.SavePID(r.Dir, rec); err != nil {
		return err
	}
	ui.Plain("Mounted bank %q at %s in background (pid %d).", r.BankID, r.Dir, pid)
	ui.Plain("Logs: %s — stop with: %s fs stop %s", fsengine.LogPath(r.Dir), os.Args[0], r.Dir)
	return nil
}

func registerFSStop(fs *SubCmd) {
	fc := fsBindCommon(fs)
	fs.Action(func(ctx context.Context, d *Deps) error {
		dir, err := fsDirArg(d)
		if err != nil {
			return err
		}
		r := fsResolve(d, dir, fc)
		d.UI.Plain("%s", fsStopDaemon(r))
		return nil
	})
}

// fsStopDaemon signals a running daemon to stop, escalating to SIGKILL
// after five seconds, and removes the pid record. It returns the report
// message for the command to print.
func fsStopDaemon(r fsResolved) string {
	rec, ok := fsengine.LoadPID(r.Dir)
	if !ok {
		return fmt.Sprintf("No daemon registered for %s.", r.Dir)
	}
	if !fsHooks.alive(rec.PID) {
		_ = os.Remove(fsengine.PIDPath(r.Dir))
		return fmt.Sprintf("Daemon was not running (cleaned up stale pid %d).", rec.PID)
	}
	_ = fsHooks.signal(rec.PID, syscall.SIGTERM)
	for i := 0; i < 50 && fsHooks.alive(rec.PID); i++ {
		fsHooks.sleep(100 * time.Millisecond)
	}
	if fsHooks.alive(rec.PID) {
		_ = fsHooks.signal(rec.PID, syscall.SIGKILL)
	}
	_ = os.Remove(fsengine.PIDPath(r.Dir))
	return fmt.Sprintf("Stopped daemon (pid %d) for %s.", rec.PID, r.Dir)
}

func registerFSRestart(fs *SubCmd) {
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
		d.UI.Plain("%s", fsStopDaemon(r))
		return fsStartBackground(d.UI, r)
	})
}

func registerFSUnmount(fs *SubCmd) {
	fc := fsBindCommon(fs)
	fs.Action(func(ctx context.Context, d *Deps) error {
		dir, err := fsDirArg(d)
		if err != nil {
			return err
		}
		r := fsResolve(d, dir, fc)
		_ = fsStopDaemon(r)
		state := fsengine.LoadState(r.Dir, r.BankID, r.APIURL)
		removed := 0
		for rel := range state.Files {
			path := r.Dir + string(os.PathSeparator) + rel
			_ = os.Chmod(path, 0o644)
			if os.Remove(path) == nil {
				removed++
			}
		}
		_ = os.RemoveAll(fsengine.ControlDir(r.Dir))
		d.UI.Plain("Unmounted %s — removed %d mirrored file(s) and control data.", r.Dir, removed)
		return nil
	})
}

func registerFSLogs(fs *SubCmd) {
	fc := fsBindCommon(fs)
	fs.Action(func(ctx context.Context, d *Deps) error {
		dir, err := fsDirArg(d)
		if err != nil {
			return err
		}
		r := fsResolve(d, dir, fc)
		data, err := os.ReadFile(fsengine.LogPath(r.Dir))
		if err != nil {
			d.UI.Plain("No logs for %s.", r.Dir)
			return nil
		}
		lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
		if len(lines) > 40 {
			lines = lines[len(lines)-40:]
		}
		for _, line := range lines {
			d.UI.Plain("%s", line)
		}
		return nil
	})
}
