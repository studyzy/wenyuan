package app

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
)

// The daemon child learns it is a daemon from the environment, so the flag must
// not survive the re-exec: leaving it in would detach the child again.
func TestDaemonArgumentsDropTheFlag(t *testing.T) {
	got := DaemonArguments([]string{"--port", "9000", "--daemon", "--workers", "2", "--daemon=true"})
	want := []string{"--port", "9000", "--workers", "2"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("arguments = %v, want %v", got, want)
	}
}

// The log is the configured path, and the reference's fallback under the
// operator's home directory otherwise.
func TestDaemonLogPath(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	path, err := DaemonLogPath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".hindsight", "daemon.log")
	if path != want {
		t.Fatalf("fallback log = %q, want %q", path, want)
	}

	configured := filepath.Join(home, "custom.log")
	cfg.DaemonLog = configured
	path, err = DaemonLogPath(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if path != configured {
		t.Fatalf("configured log = %q, want %q", path, configured)
	}
}

// The preflight reports an occupied address, which is what lets a daemon launch
// fail in the terminal that asked for it.
func TestPreflightAddress(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	if err := PreflightAddress(listener.Addr().String()); err == nil {
		t.Fatal("an occupied address passed the preflight")
	}
	if err := PreflightAddress("127.0.0.1:0"); err != nil {
		t.Fatalf("a free address failed the preflight: %v", err)
	}
}

// A worker inherits the supervisor's socket: the round trip through a child
// process is what proves one bind serves every worker.
func TestInheritedListenerRoundTrip(t *testing.T) {
	if os.Getenv("WENYUAN_PROCESS_HELPER") == "listener" {
		runListenerHelper(t)
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	file, err := listenerFile(listener)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()

	cmd := exec.Command(os.Args[0], "-test.run=TestInheritedListenerRoundTrip")
	cmd.Env = append(os.Environ(),
		"WENYUAN_PROCESS_HELPER=listener",
		ListenFDEnv+"="+strconv.Itoa(defaultListenFD),
	)
	cmd.ExtraFiles = []*os.File{file}
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("helper failed: %v\n%s", err, output)
	}
	// The child reports the address it inherited, which must be the one the
	// parent bound.
	if !strings.Contains(string(output), listener.Addr().String()) {
		t.Fatalf("child did not inherit the bound address: %q", output)
	}
}

// runListenerHelper is the child half of the inheritance round trip.
func runListenerHelper(t *testing.T) {
	inherited, ok, err := inheritedListener()
	if err != nil {
		t.Fatalf("inheriting: %v", err)
	}
	if !ok {
		t.Fatal("no socket was inherited")
	}
	defer func() { _ = inherited.Close() }()
	if _, err := fmt.Fprint(os.Stdout, inherited.Addr().String()); err != nil {
		t.Fatalf("reporting the address: %v", err)
	}
}

// A worker that fails ends the whole set: a half-served deployment is worse than
// one that stopped.
func TestSuperviseChildrenStopTheRest(t *testing.T) {
	children := []*exec.Cmd{
		exec.Command("sh", "-c", "exit 3"),
		exec.Command("sh", "-c", "sleep 30"),
	}
	for _, child := range children {
		if err := child.Start(); err != nil {
			t.Fatal(err)
		}
	}
	done := make(chan error, 1)
	go func() { done <- superviseChildren(context.Background(), children) }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a worker's failure was reported as a clean end")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the supervisor did not stop the surviving worker")
	}
}

// A worker stopped by a signal is a normal end of a supervised run, not a
// failure to report.
func TestSuperviseChildrenTreatSignalAsClean(t *testing.T) {
	child := exec.Command("sh", "-c", "sleep 30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- superviseChildren(context.Background(), []*exec.Cmd{child}) }()
	time.Sleep(200 * time.Millisecond)
	if err := child.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a signalled worker was reported as a failure: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the supervisor did not return after its worker stopped")
	}
}

// A daemon launch without the flag does nothing, so the flag is what detaches.
func TestDaemonizeWithoutFlagServes(t *testing.T) {
	cfg := config.FromEnv(func(string) (string, bool) { return "", false })
	detached, err := Daemonize(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if detached {
		t.Fatal("a launch that did not ask to detach stopped")
	}
}
