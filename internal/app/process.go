package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
)

// Process shape.
//
// Both shapes the reference's entrypoint offers are process-level, not
// request-level, so they live here rather than in the composition root: a
// daemon launch re-execs this binary detached from its terminal, and a
// multi-worker launch runs that many copies of it behind one listening socket.

const (
	// DaemonChildEnv marks the re-exec'd child of a daemon launch, so the child
	// redirects its output and serves instead of detaching again. The name is
	// the reference's own: a launcher that sets it (the reference's embed
	// manager does) starts this build as a daemon without the flag.
	DaemonChildEnv = "_WENYUAN_DAEMON_CHILD"
	// ListenFDEnv carries the inherited listening socket's descriptor to a
	// worker process. uvicorn passes its socket the same way, which is why one
	// bind serves every worker rather than one bind per worker.
	ListenFDEnv = "_WENYUAN_LISTEN_FD"
	// WorkerIndexEnv distinguishes a spawned worker from the supervisor, which
	// must not serve.
	WorkerIndexEnv = "_WENYUAN_WORKER_INDEX"
	// defaultListenFD is the descriptor a child inherits its socket on: the
	// first entry of the parent's ExtraFiles.
	defaultListenFD = 3
)

// IsDaemonChild reports whether this process is the detached child of a daemon
// launch and must therefore redirect its output and serve.
func IsDaemonChild() bool { return os.Getenv(DaemonChildEnv) == "1" }

// IsWorkerChild reports whether this process is one of a supervised worker set.
func IsWorkerChild() bool { return os.Getenv(WorkerIndexEnv) != "" }

// DaemonLogPath resolves the file a daemon appends its output to: the
// configured WENYUAN_API_DAEMON_LOG, or the reference's fallback under the
// operator's home directory.
func DaemonLogPath(cfg config.Config) (string, error) {
	if path := strings.TrimSpace(cfg.DaemonLog); path != "" {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving the daemon log path: %w", err)
	}
	return filepath.Join(home, filepath.FromSlash(config.DefaultDaemonLogName)), nil
}

// DaemonArguments strips the daemon flag from a launch's arguments: the child
// learns it is a daemon from the environment, and leaving the flag in would
// make it detach again.
func DaemonArguments(args []string) []string {
	out := make([]string, 0, len(args))
	for _, arg := range args {
		if arg == "--daemon" || strings.HasPrefix(arg, "--daemon=") {
			continue
		}
		out = append(out, arg)
	}
	return out
}

// PreflightAddress reports whether the address can be bound, by binding and
// releasing it. A daemon launch checks this in the foreground parent, so an
// occupied port is reported to the terminal that asked for the daemon instead
// of being buried in its log.
func PreflightAddress(address string) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return err
	}
	return listener.Close()
}

// Daemonize detaches the process when a daemon was requested. It reports
// whether the caller must stop: the parent that spawned the detached child
// exits, while the child (and a process that never asked to detach) serves.
func Daemonize(cfg config.Config) (bool, error) {
	if !cfg.Daemon {
		return false, nil
	}
	logPath, err := DaemonLogPath(cfg)
	if err != nil {
		return false, err
	}
	if IsDaemonChild() {
		// The re-exec'd child: its output goes to the log and it serves.
		return false, redirectStdio(logPath)
	}

	// Fail before detaching if the port cannot be bound: the child's failure
	// would otherwise only reach the log file.
	if err := PreflightAddress(cfg.Address()); err != nil {
		return false, fmt.Errorf("cannot bind %s: %w", cfg.Address(), err)
	}

	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return false, fmt.Errorf("creating the daemon log directory: %w", err)
	}
	executable, err := os.Executable()
	if err != nil {
		return false, fmt.Errorf("resolving the executable: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return false, fmt.Errorf("opening the daemon log: %w", err)
	}
	defer func() { _ = logFile.Close() }()
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return false, fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	defer func() { _ = devNull.Close() }()

	cmd := exec.Command(executable, DaemonArguments(os.Args[1:])...)
	cmd.Env = append(os.Environ(), DaemonChildEnv+"=1", config.EnvDaemonLog+"="+logPath)
	cmd.Stdin = devNull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// A detached session survives the terminal that launched it, so the daemon
	// keeps running after the launcher exits.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("starting the daemon: %w", err)
	}
	// The child is on its own: release it so the parent can exit without
	// reaping it or holding a copy of its log descriptor open.
	if err := cmd.Process.Release(); err != nil {
		return false, fmt.Errorf("detaching the daemon: %w", err)
	}
	return true, nil
}

// redirectStdio points the process's standard streams at the daemon log. It
// runs in the detached child, before the first log line is written.
func redirectStdio(logPath string) error {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return fmt.Errorf("creating the daemon log directory: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening the daemon log: %w", err)
	}
	devNull, err := os.OpenFile(os.DevNull, os.O_RDONLY, 0)
	if err != nil {
		return fmt.Errorf("opening %s: %w", os.DevNull, err)
	}
	for _, target := range []struct {
		from *os.File
		fd   uintptr
	}{{devNull, 0}, {logFile, 1}, {logFile, 2}} {
		if err := syscall.Dup2(int(target.from.Fd()), int(target.fd)); err != nil {
			return fmt.Errorf("redirecting standard streams: %w", err)
		}
	}
	return nil
}

// Supervise runs the requested number of worker processes behind one listening
// socket and returns when they have all stopped.
//
// The socket is bound once, here, and inherited by every worker: the port is
// held for the lifetime of the supervisor, so a worker that dies and a
// deployment that restarts never race another process for it.
func Supervise(ctx context.Context, cfg config.Config, args []string) error {
	listener, err := net.Listen("tcp", cfg.Address())
	if err != nil {
		return err
	}
	defer func() { _ = listener.Close() }()

	file, err := listenerFile(listener)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	children, err := spawnWorkers(cfg, args, file)
	if err != nil {
		stopChildren(children)
		return err
	}
	return superviseChildren(ctx, children)
}

// listenerFile duplicates the listener's descriptor so it can be inherited by a
// child process.
func listenerFile(listener net.Listener) (*os.File, error) {
	tcp, ok := listener.(*net.TCPListener)
	if !ok {
		return nil, fmt.Errorf("worker processes need a TCP listener, got %T", listener)
	}
	file, err := tcp.File()
	if err != nil {
		return nil, fmt.Errorf("duplicating the listening socket: %w", err)
	}
	// File switches the original socket to blocking mode, which would stall the
	// accept loop of every other reader of it.
	_ = tcp.SetDeadline(time.Time{})
	return file, nil
}

// spawnWorkers starts cfg.Workers copies of this binary, each inheriting the
// listening socket and knowing its index.
func spawnWorkers(cfg config.Config, args []string, socket *os.File) ([]*exec.Cmd, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolving the executable: %w", err)
	}
	children := make([]*exec.Cmd, 0, cfg.Workers)
	for index := range cfg.Workers {
		cmd := exec.Command(executable, args...)
		cmd.Env = append(os.Environ(),
			WorkerIndexEnv+"="+strconv.Itoa(index),
			config.EnvWorkers+"="+strconv.Itoa(cfg.Workers),
			ListenFDEnv+"="+strconv.Itoa(defaultListenFD),
		)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.ExtraFiles = []*os.File{socket}
		if err := cmd.Start(); err != nil {
			return children, fmt.Errorf("starting worker %d: %w", index, err)
		}
		children = append(children, cmd)
	}
	return children, nil
}

// superviseChildren waits for the first worker to stop and then stops the rest,
// so a set never survives a failure half-served. The first exit is the one the
// supervisor reports, which is how uvicorn's supervisor behaves.
func superviseChildren(ctx context.Context, children []*exec.Cmd) error {
	exits := make(chan error, len(children))
	for _, child := range children {
		go func(cmd *exec.Cmd) { exits <- cmd.Wait() }(child)
	}

	stop := make(chan os.Signal, 2)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(stop)

	var first error
	// One worker's exit may already have been taken above; the rest are awaited
	// so the supervisor never returns while a worker still holds the socket.
	collected := 0
	select {
	case <-ctx.Done():
	case <-stop:
	case first = <-exits:
		collected = 1
	}
	stopChildren(children)
	for ; collected < len(children); collected++ {
		// The drain only waits the children out; their errors are already
		// accounted for by the first exit the select picked up.
		<-exits //nolint:errcheck // the drain waits the children out; their errors are already accounted for
	}
	if first != nil && !isCleanExit(first) {
		return first
	}
	return nil
}

// stopChildren asks every worker to shut down, tolerating one that has already
// exited.
func stopChildren(children []*exec.Cmd) {
	for _, child := range children {
		if child.Process != nil {
			_ = child.Process.Signal(syscall.SIGTERM)
		}
	}
}

// isCleanExit reports whether a worker stopped because it was asked to: a
// signal-driven shutdown is a normal end of a supervised run.
func isCleanExit(err error) bool {
	if err == nil {
		return true
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) {
		return true
	}
	status, ok := exit.Sys().(syscall.WaitStatus)
	if !ok {
		return false
	}
	return status.Signaled() && (status.Signal() == syscall.SIGTERM || status.Signal() == syscall.SIGINT)
}

// inheritedListener rebuilds the listening socket a worker inherited, or
// reports that there is none so the caller binds its own.
func inheritedListener() (net.Listener, bool, error) {
	raw := strings.TrimSpace(os.Getenv(ListenFDEnv))
	if raw == "" {
		return nil, false, nil
	}
	fd, err := strconv.Atoi(raw)
	if err != nil {
		return nil, false, fmt.Errorf("%s is not a descriptor: %q", ListenFDEnv, raw)
	}
	file := os.NewFile(uintptr(fd), "listener")
	if file == nil {
		return nil, false, fmt.Errorf("descriptor %d is not open", fd)
	}
	listener, err := net.FileListener(file)
	// FileListener duplicates the descriptor, so the inherited copy is closed
	// whether or not the listener could be rebuilt.
	_ = file.Close()
	if err != nil {
		return nil, false, fmt.Errorf("inheriting the listening socket: %w", err)
	}
	return listener, true, nil
}
