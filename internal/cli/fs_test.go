package cli

// Tests for the fs command family: configuration resolution, the health
// verdict matrix, daemon lifecycle through the injected process hooks, and
// the sync/list/logs/unmount command outputs. No test forks a real daemon;
// the process primitives are injected.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/cli/fsengine"
)

// runFS resolves and executes one fs invocation with the client pointed at
// baseURL.
func runFS(t *testing.T, baseURL string, format Format, args []string, stdin io.Reader) commandResult {
	t.Helper()
	registry := NewRegistry()
	RegisterFSCommands(registry)
	cmd, rest, err := registry.Resolve(args)
	if err != nil {
		return commandResult{err: err}
	}
	positionals, err := cmd.ParseArgs(rest)
	if err != nil {
		return commandResult{err: err}
	}
	var stdout, stderr strings.Builder
	deps := &Deps{
		Ctx:    context.Background(),
		Args:   positionals,
		Flags:  cmd.Flags(),
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  stdin,
		Format: format,
		Config: Config{APIURL: baseURL},
		Client: NewClient(baseURL, ""),
		UI:     NewUI(&stdout, &stderr, ColorEnv{}),
	}
	err = cmd.Execute(deps.Ctx, deps)
	return commandResult{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// withFSHooks swaps the process hooks for test doubles and restores them
// at cleanup.
func withFSHooks(t *testing.T, h fsDaemonHooks) {
	t.Helper()
	old := fsHooks
	fsHooks = h
	t.Cleanup(func() { fsHooks = old })
}

// fsClearEnv pins the fs environment variables to unset for isolation.
func fsClearEnv(t *testing.T) {
	t.Helper()
	t.Setenv("WENYUAN_FS_DIR", "")
	t.Setenv("WENYUAN_BANK_ID", "")
	t.Setenv("WENYUAN_FS_INTERVAL", "")
}

func TestFSConfigResolution(t *testing.T) {
	fsClearEnv(t)
	base := func() *Deps {
		return &Deps{Config: Config{APIURL: "http://from-cli:8888", APIKey: "cli-key"}}
	}
	fc := func(bank, url, token string, interval int, intervalUsed, writable bool) *fsCommon {
		return &fsCommon{
			bank: &bank, apiURL: &url, token: &token,
			interval: &interval, intervalUsed: intervalUsed, writable: &writable,
		}
	}

	// Defaults fall through every level.
	r := fsResolve(base(), "/tmp/m", fc("", "", "", 0, false, false))
	if r.APIURL != "http://from-cli:8888" || r.Token != "cli-key" || r.BankID != "" || r.Interval != 30 || r.Writable {
		t.Fatalf("defaults = %+v", r)
	}

	// Flags win; trailing slashes are stripped once.
	r = fsResolve(base(), "/tmp/m", fc("b1", "http://flag/", "tok", 5, true, true))
	if r.APIURL != "http://flag" || r.Token != "tok" || r.BankID != "b1" || r.Interval != 5 || !r.Writable {
		t.Fatalf("flags = %+v", r)
	}

	// The saved control file sits between flags and the CLI config, and
	// writable is sticky.
	saved := t.TempDir()
	if err := fsengine.SaveConfig(saved, fsengine.Config{
		APIURL: "http://saved:1", BankID: "saved-bank", IntervalSeconds: 44, Writable: true,
	}); err != nil {
		t.Fatalf("save: %v", err)
	}
	r = fsResolve(base(), saved, fc("", "", "", 0, false, false))
	if r.APIURL != "http://saved:1" || r.BankID != "saved-bank" || r.Interval != 44 || !r.Writable {
		t.Fatalf("saved = %+v", r)
	}
	r = fsResolve(base(), saved, fc("flag-bank", "http://flag", "", 0, false, false))
	if r.BankID != "flag-bank" || r.APIURL != "http://flag" || !r.Writable {
		t.Fatalf("flag-over-saved = %+v", r)
	}

	// The environment provides the bank and interval below the saved file.
	t.Setenv("WENYUAN_BANK_ID", "env-bank")
	t.Setenv("WENYUAN_FS_INTERVAL", "90")
	empty := t.TempDir()
	r = fsResolve(base(), empty, fc("", "", "", 0, false, false))
	if r.BankID != "env-bank" || r.Interval != 90 {
		t.Fatalf("env = %+v", r)
	}

	// An interval below 1 falls through at every level.
	t.Setenv("WENYUAN_FS_INTERVAL", "0")
	r = fsResolve(base(), empty, fc("", "", "", 0, true, false))
	if r.Interval != 30 {
		t.Fatalf("zero flag interval = %+v", r)
	}
	t.Setenv("WENYUAN_FS_INTERVAL", "notanumber")
	r = fsResolve(base(), empty, fc("", "", "", 0, false, false))
	if r.Interval != 30 {
		t.Fatalf("garbage env interval = %+v", r)
	}
}

func TestFSDirArg(t *testing.T) {
	fsClearEnv(t)
	tmp := t.TempDir()
	t.Chdir(tmp)

	// Default directory, absolutized.
	dir, err := fsDirArg(&Deps{})
	if err != nil || dir != filepath.Join(tmp, "wenyuan-fs") {
		t.Fatalf("default dir = %q err=%v", dir, err)
	}

	// The environment and the positional override it.
	t.Setenv("WENYUAN_FS_DIR", "from-env")
	dir, _ = fsDirArg(&Deps{})
	if dir != filepath.Join(tmp, "from-env") {
		t.Fatalf("env dir = %q", dir)
	}
	t.Setenv("WENYUAN_FS_DIR", "")
	dir, _ = fsDirArg(&Deps{Args: []string{"/abs/dir"}})
	if dir != "/abs/dir" {
		t.Fatalf("positional dir = %q", dir)
	}
	if _, err := fsDirArg(&Deps{Args: []string{"a", "b"}}); err == nil {
		t.Fatalf("two positionals accepted")
	}
}

func TestFSRequireBank(t *testing.T) {
	err := fsRequireBank(fsResolved{})
	if err == nil || !strings.Contains(err.Error(), "WENYUAN_BANK_ID") {
		t.Fatalf("err = %v", err)
	}
	if err := fsRequireBank(fsResolved{BankID: "b"}); err != nil {
		t.Fatalf("err = %v", err)
	}
}

// fsStatusCase is one row of the health verdict matrix.
type fsStatusCase struct {
	name        string
	pid         *fsengine.PIDRecord
	pidAlive    bool
	lastSyncAt  *string
	lastSyncOk  bool
	override    triStateInt
	wantStatus  string
	wantHealthy bool
}

func TestFSStatusVerdicts(t *testing.T) {
	syncAt := func(offset time.Duration) *string {
		s := time.Now().Add(-offset).UTC().Format(fsengine.TimestampFormat)
		return &s
	}
	cases := []fsStatusCase{
		{
			name: "no pid file is dead", wantStatus: "dead",
		},
		{
			name: "dead pid is dead", pid: &fsengine.PIDRecord{PID: 99, IntervalSeconds: 5},
			pidAlive: false, wantStatus: "dead",
		},
		{
			name: "never synced is stale", pid: &fsengine.PIDRecord{PID: 1, IntervalSeconds: 5},
			pidAlive: true, lastSyncAt: nil, wantStatus: "stale",
		},
		{
			name: "failed sync is failed", pid: &fsengine.PIDRecord{PID: 1, IntervalSeconds: 5},
			pidAlive: true, lastSyncAt: syncAt(time.Second), lastSyncOk: false, wantStatus: "failed",
		},
		{
			name: "old ok sync is stale at max(3i,15)", pid: &fsengine.PIDRecord{PID: 1, IntervalSeconds: 5},
			pidAlive: true, lastSyncAt: syncAt(16 * time.Second), lastSyncOk: true, wantStatus: "stale",
		},
		{
			name: "fresh ok sync is ok", pid: &fsengine.PIDRecord{PID: 1, IntervalSeconds: 5},
			pidAlive: true, lastSyncAt: syncAt(14 * time.Second), lastSyncOk: true, wantStatus: "ok", wantHealthy: true,
		},
		{
			name: "large interval widens the window", pid: &fsengine.PIDRecord{PID: 1, IntervalSeconds: 60},
			pidAlive: true, lastSyncAt: syncAt(100 * time.Second), lastSyncOk: true, wantStatus: "ok", wantHealthy: true,
		},
		{
			name: "override wins outright", pid: &fsengine.PIDRecord{PID: 1, IntervalSeconds: 60},
			pidAlive: true, lastSyncAt: syncAt(50 * time.Second), lastSyncOk: true,
			override: triStateInt{set: true, value: 10}, wantStatus: "stale",
		},
		{
			name: "unparseable timestamp is stale", pid: &fsengine.PIDRecord{PID: 1, IntervalSeconds: 5},
			pidAlive: true, lastSyncAt: strPointer("garbage"), lastSyncOk: true, wantStatus: "stale",
		},
		{
			name: "no daemon uses config interval", lastSyncAt: syncAt(100 * time.Second),
			lastSyncOk: true, wantStatus: "dead", // pid nil wins first
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.pid != nil {
				if err := fsengine.SavePID(dir, *tc.pid); err != nil {
					t.Fatalf("save pid: %v", err)
				}
			}
			withFSHooks(t, fsDaemonHooks{
				alive:  func(int) bool { return tc.pidAlive },
				signal: func(int, os.Signal) error { return nil },
				spawn:  func(string, []string, *os.File, *os.File, *os.File) (int, error) { return 0, nil },
				sleep:  func(time.Duration) {},
			})
			pid := fsLoadLivePID(dir)
			state := fsengine.State{
				Version: 1, BankID: "b1", APIURL: "http://x",
				LastSyncAt: tc.lastSyncAt, LastSyncOk: tc.lastSyncOk,
				Files: map[string]fsengine.FileRecord{"a.md": {File: "a.md", Hash: "h"}},
				Dirs:  []string{},
			}
			r := fsResolved{Dir: dir, BankID: "b1", APIURL: "http://x", Interval: 30}
			report := fsComputeHealth(time.Now(), r, pid, state, tc.override)
			if report.Status != tc.wantStatus || report.Healthy != tc.wantHealthy {
				t.Fatalf("status = %s healthy=%v, want %s/%v", report.Status, report.Healthy, tc.wantStatus, tc.wantHealthy)
			}
			if report.MirroredFiles != 1 {
				t.Fatalf("mirrored = %d", report.MirroredFiles)
			}
		})
	}
}

func strPointer(s string) *string { return &s }

func TestFSStatusDeadPIDCleanup(t *testing.T) {
	dir := t.TempDir()
	if err := fsengine.SavePID(dir, fsengine.PIDRecord{PID: 99}); err != nil {
		t.Fatalf("save: %v", err)
	}
	withFSHooks(t, fsDaemonHooks{
		alive:  func(int) bool { return false },
		signal: func(int, os.Signal) error { return nil },
		spawn:  func(string, []string, *os.File, *os.File, *os.File) (int, error) { return 0, nil },
		sleep:  func(time.Duration) {},
	})
	if pid := fsLoadLivePID(dir); pid != nil {
		t.Fatalf("dead pid returned")
	}
	if _, err := os.Stat(fsengine.PIDPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("stale pid file not removed")
	}
}

func TestFSStatusReportShape(t *testing.T) {
	dir := t.TempDir()
	pid := fsengine.PIDRecord{PID: 7, BankID: "b1", IntervalSeconds: 5, StartedAt: "2026-01-02T03:04:05.000Z"}
	if err := fsengine.SavePID(dir, pid); err != nil {
		t.Fatalf("save: %v", err)
	}
	started := time.Now().Add(-2 * time.Second).UTC().Format(fsengine.TimestampFormat)
	state := fsengine.State{
		Version: 1, BankID: "state-bank", APIURL: "http://state",
		LastSyncAt: &started, LastSyncOk: true,
		Files: map[string]fsengine.FileRecord{},
		Dirs:  []string{},
	}
	withFSHooks(t, fsDaemonHooks{
		alive:  func(int) bool { return true },
		signal: func(int, os.Signal) error { return nil },
		spawn:  func(string, []string, *os.File, *os.File, *os.File) (int, error) { return 0, nil },
		sleep:  func(time.Duration) {},
	})
	r := fsResolved{Dir: dir, BankID: "cli-bank", APIURL: "http://cli", Interval: 30, Writable: true}
	report := fsComputeHealth(time.Now(), r, fsLoadLivePID(dir), state, triStateInt{})

	// The state file's bank and API win over the resolved config.
	if report.Bank != "state-bank" || report.APIURL != "http://state" {
		t.Fatalf("bank/api = %s/%s", report.Bank, report.APIURL)
	}
	if report.Mode != "writable" || !report.DaemonRunning || report.DaemonPID == nil || *report.DaemonPID != 7 {
		t.Fatalf("daemon fields = %+v", report)
	}
	if report.DaemonInterval == nil || *report.DaemonInterval != 5 || report.StaleAfter != 15 {
		t.Fatalf("interval fields = %+v", report)
	}
	if report.LastSyncAge == nil || *report.LastSyncAge < 1 || *report.LastSyncAge > 5 {
		t.Fatalf("age = %v", report.LastSyncAge)
	}

	data, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, want := range []string{
		"healthy", "status", "mount", "bank", "api_url", "mode", "daemon_running",
		"daemon_pid", "daemon_started_at", "daemon_interval_seconds", "last_sync_at",
		"last_sync_ok", "last_sync_age_seconds", "last_error", "stale_after_seconds",
		"mirrored_files",
	} {
		if !strings.Contains(string(data), `"`+want+`"`) {
			t.Errorf("report JSON missing %q: %s", want, data)
		}
	}
}

func TestFSStatusPretty(t *testing.T) {
	var out strings.Builder
	ui := NewUI(&out, &out, ColorEnv{})
	started := "2026-01-02T03:04:05.000Z"
	pid := 4242
	interval := 10
	age := int64(3)
	report := fsStatusReport{
		Healthy: true, Status: "ok", Mount: "/m", Bank: "b1", APIURL: "http://x",
		Mode: "writable", DaemonRunning: true, DaemonPID: &pid,
		DaemonStartedAt: &started, DaemonInterval: &interval,
		LastSyncAt: strPointer("2026-10-15T10:00:00.000Z"), LastSyncOK: false,
		LastSyncAge: &age, LastError: strPointer("boom"),
		StaleAfter: 30, MirroredFiles: 2,
	}
	fsRenderStatus(ui, report)
	for _, want := range []string{
		"Mount:    /m", "Bank:     b1", "API:      http://x",
		"Mode:     writable (one-way; edits reverted on refresh)",
		"Daemon:   running (pid 4242)", "Interval: 10s (started 2026-01-02T03:04:05.000Z)",
		"Last sync: 2026-10-15T10:00:00.000Z (3s ago) (FAILED)", "Last error: boom",
		"Mirrored: 2 file(s)", "Health:   ok",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("pretty missing %q:\n%s", want, out.String())
		}
	}

	// Unhealthy verdicts render uppercase and the stopped daemon drops the
	// interval line.
	out.Reset()
	report2 := fsStatusReport{
		Healthy: false, Status: "stale", Mount: "/m", Bank: "", APIURL: "http://x",
		Mode: "read-only", StaleAfter: 15,
	}
	fsRenderStatus(ui, report2)
	text := out.String()
	if !strings.Contains(text, "Bank:     (unset)") ||
		!strings.Contains(text, "Mode:     read-only (edits blocked)") ||
		!strings.Contains(text, "Daemon:   stopped") ||
		!strings.Contains(text, "Health:   STALE") {
		t.Fatalf("unhealthy pretty =\n%s", text)
	}
	if strings.Contains(text, "Interval:") || strings.Contains(text, "Last error:") {
		t.Fatalf("conditional lines leaked:\n%s", text)
	}
}

// fsLifecycleHooks records spawn and signal activity for the daemon tests.
type fsLifecycleHooks struct {
	alive   map[int]bool
	spawned []string
	signals []string
	sleeps  int
}

func (h *fsLifecycleHooks) install(t *testing.T) {
	t.Helper()
	withFSHooks(t, fsDaemonHooks{
		alive: func(pid int) bool { return h.alive[pid] },
		signal: func(pid int, sig os.Signal) error {
			h.signals = append(h.signals, sig.String())
			delete(h.alive, pid)
			return nil
		},
		spawn: func(exe string, args []string, stdin, stdout, stderr *os.File) (int, error) {
			h.spawned = append(h.spawned, exe+" "+strings.Join(args, " "))
			return 4242, nil
		},
		sleep: func(time.Duration) { h.sleeps++ },
	})
}

func TestFSDaemonStartStop(t *testing.T) {
	fsClearEnv(t)
	dir := t.TempDir()
	h := &fsLifecycleHooks{alive: map[int]bool{4242: true}}
	h.install(t)
	r := fsResolved{Dir: dir, APIURL: "http://x", BankID: "b1", Interval: 12, Writable: true}
	var out strings.Builder
	ui := NewUI(&out, &out, ColorEnv{})

	if err := fsStartBackground(ui, r); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(h.spawned) != 1 || !strings.HasSuffix(h.spawned[0], "fs run "+dir) {
		t.Fatalf("spawned = %v", h.spawned)
	}
	rec, ok := fsengine.LoadPID(dir)
	if !ok || rec.PID != 4242 || rec.BankID != "b1" || rec.IntervalSeconds != 12 || rec.StartedAt == "" {
		t.Fatalf("pid record = %+v ok=%v", rec, ok)
	}
	if cfg := fsengine.LoadConfig(dir); cfg.BankID != "b1" || cfg.IntervalSeconds != 12 {
		t.Fatalf("config not saved: %+v", cfg)
	}
	for _, want := range []string{
		`Mounted bank "b1" at ` + dir + ` in background (pid 4242).`,
		"Logs: " + fsengine.LogPath(dir) + " — stop with: ",
	} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("start output missing %q:\n%s", want, out.String())
		}
	}

	// A second start reports the running daemon instead of forking.
	out.Reset()
	if err := fsStartBackground(ui, r); err != nil {
		t.Fatalf("second start: %v", err)
	}
	if len(h.spawned) != 1 {
		t.Fatalf("second start forked again")
	}
	if !strings.Contains(out.String(), "Already mounted at "+dir+" (daemon pid 4242).") {
		t.Fatalf("second start output = %q", out.String())
	}

	// Stop signals, waits, removes the pid file and reports.
	out.Reset()
	if msg := fsStopDaemon(r); msg != "Stopped daemon (pid 4242) for "+dir+"." {
		t.Fatalf("stop msg = %q", msg)
	}
	if len(h.signals) != 1 || h.signals[0] != "terminated" {
		t.Fatalf("signals = %v", h.signals)
	}
	if _, ok := fsengine.LoadPID(dir); ok {
		t.Fatalf("pid file survived stop")
	}

	// Stopping again reports no daemon.
	if msg := fsStopDaemon(r); msg != "No daemon registered for "+dir+"." {
		t.Fatalf("stop msg = %q", msg)
	}

	// A stale pid of a dead process is cleaned up.
	if err := fsengine.SavePID(dir, fsengine.PIDRecord{PID: 77}); err != nil {
		t.Fatalf("save: %v", err)
	}
	msg := fsStopDaemon(r)
	if msg != "Daemon was not running (cleaned up stale pid 77)." {
		t.Fatalf("stale stop msg = %q", msg)
	}
	if _, ok := fsengine.LoadPID(dir); ok {
		t.Fatalf("stale pid file survived")
	}
}

func TestFSSyncCommand(t *testing.T) {
	fsClearEnv(t)
	srv, calls := kbOrFSStub(t)
	dir := t.TempDir()
	r := runFS(t, srv.URL, FormatPretty, []string{"fs", "sync", dir, "--bank", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v\n%s", r.err, r.stdout)
	}
	wantContains(t, r.stdout, "Synced 1 pages / 1 folders into "+dir+" (1 updated, 0 unchanged, 0 removed)")
	for _, call := range calls() {
		if !strings.HasPrefix(call.Path, "/v1/default/banks/b1/knowledge-base/") {
			t.Fatalf("path = %s", call.Path)
		}
	}
	if cfg := fsengine.LoadConfig(dir); cfg.BankID != "b1" {
		t.Fatalf("config not persisted: %+v", cfg)
	}
	if _, err := os.Stat(fsengine.StatePath(dir)); err != nil {
		t.Fatalf("state not written: %v", err)
	}
	// A read-only mirror: files land with 0444.
	info, err := os.Stat(filepath.Join(dir, "guide", "page.md"))
	if err != nil || info.Mode().Perm() != 0o444 {
		t.Fatalf("page mode = %v err=%v", info.Mode().Perm(), err)
	}
}

func TestFSMountOnceCommand(t *testing.T) {
	fsClearEnv(t)
	srv, _ := kbOrFSStub(t)
	dir := t.TempDir()
	r := runFS(t, srv.URL, FormatPretty, []string{"fs", "mount", dir, "--bank", "b1", "--once"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v\n%s", r.err, r.stdout)
	}
	wantContains(t, r.stdout, "Synced 1 pages / 1 folders into "+dir)
}

func TestFSListCommand(t *testing.T) {
	fsClearEnv(t)
	srv, _ := kbOrFSStub(t)
	dir := t.TempDir()
	r := runFS(t, srv.URL, FormatPretty, []string{"fs", "list", dir, "--bank", "b1"}, nil)
	if r.err != nil {
		t.Fatalf("err = %v\n%s", r.err, r.stdout)
	}
	wantContains(t, r.stdout, `Planned mirror for bank "b1" at `+dir)
	wantContains(t, r.stdout, "  guide/")
	wantContains(t, r.stdout, "  guide/page.md")
	// Nothing is written: no control directory, no mirror files.
	if _, err := os.Stat(fsengine.ControlDir(dir)); !os.IsNotExist(err) {
		t.Fatalf("list wrote a control directory")
	}
	if _, err := os.Stat(filepath.Join(dir, "guide")); !os.IsNotExist(err) {
		t.Fatalf("list wrote mirror files")
	}
	r = runFS(t, srv.URL, FormatJSON, []string{"fs", "list", dir, "--bank", "b1"}, nil)
	if !strings.Contains(r.stdout, `"pages"`) || !strings.Contains(r.stdout, `"dirs"`) {
		t.Fatalf("json = %q", r.stdout)
	}
}

func TestFSLogsCommand(t *testing.T) {
	fsClearEnv(t)
	dir := t.TempDir()
	r := runFS(t, "http://unused", FormatPretty, []string{"fs", "logs", dir}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "No logs for "+dir+".")

	if err := os.MkdirAll(fsengine.ControlDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	var log strings.Builder
	for i := 0; i < 50; i++ {
		_, _ = fmt.Fprintf(&log, "[2026-10-15 10:00:00] line %d\n", i)
	}
	if err := os.WriteFile(fsengine.LogPath(dir), []byte(log.String()), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}
	r = runFS(t, "http://unused", FormatPretty, []string{"fs", "logs", dir}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	lines := strings.Split(strings.TrimRight(r.stdout, "\n"), "\n")
	if len(lines) != 40 {
		t.Fatalf("lines = %d", len(lines))
	}
	if !strings.Contains(lines[0], "line 10") || !strings.Contains(lines[39], "line 49") {
		t.Fatalf("wrong tail: %q..%q", lines[0], lines[39])
	}
}

func TestFSUnmountCommand(t *testing.T) {
	fsClearEnv(t)
	srv, _ := kbOrFSStub(t)
	dir := t.TempDir()
	if r := runFS(t, srv.URL, FormatPretty, []string{"fs", "sync", dir, "--bank", "b1"}, nil); r.err != nil {
		t.Fatalf("sync: %v\n%s", r.err, r.stdout)
	}
	h := &fsLifecycleHooks{alive: map[int]bool{4242: true}}
	h.install(t)
	if err := fsengine.SavePID(dir, fsengine.PIDRecord{PID: 4242, BankID: "b1"}); err != nil {
		t.Fatalf("save pid: %v", err)
	}
	r := runFS(t, srv.URL, FormatPretty, []string{"fs", "unmount", dir}, nil)
	if r.err != nil {
		t.Fatalf("err = %v", r.err)
	}
	wantContains(t, r.stdout, "Unmounted "+dir+" — removed 1 mirrored file(s) and control data.")
	if _, err := os.Stat(filepath.Join(dir, "guide", "page.md")); !os.IsNotExist(err) {
		t.Fatalf("mirrored file survived unmount")
	}
	if _, err := os.Stat(fsengine.ControlDir(dir)); !os.IsNotExist(err) {
		t.Fatalf("control directory survived unmount")
	}
}

func TestFSRunLoopLogs(t *testing.T) {
	fsClearEnv(t)
	srv, _ := kbOrFSStub(t)
	dir := t.TempDir()
	r := fsResolved{Dir: dir, APIURL: srv.URL, BankID: "b1", Interval: 1, Writable: false}
	ctx, cancel := context.WithCancel(context.Background())
	ticks := 0
	sleep := func(time.Duration) {
		ticks++
		if ticks == 3 {
			cancel()
		}
	}
	var log strings.Builder
	if err := fsRunLoop(ctx, &log, r, make(chan struct{}), sleep); err != nil {
		t.Fatalf("loop: %v", err)
	}
	text := log.String()
	for _, want := range []string{
		`mounting bank "b1" at ` + dir + ` (every 1s, ` + srv.URL + `)`,
		"synced 1 pages / 1 folders — 1 updated, 0 unchanged, 0 removed",
		"mount stopped",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("loop log missing %q:\n%s", want, text)
		}
	}
	if !strings.HasPrefix(text, "[") || !strings.Contains(text, "] mounting bank") {
		t.Errorf("log lines are not stamped:\n%s", text)
	}
}

// kbOrFSStub serves one folder and one page for the fs command tests.
func kbOrFSStub(t *testing.T) (*httptest.Server, func() []capturedRequest) {
	t.Helper()
	return stubAPI(t, func(call capturedRequest) (int, string) {
		switch {
		case strings.HasSuffix(call.Path, "/knowledge-base/tree"):
			return 200, `{"roots":[{"id":"f1","kind":"folder","name":"Guide","managed":false,"tags":[],"children":[{"id":"p1","kind":"page","name":"Page","managed":true,"tags":[]}]}]}`
		case strings.HasSuffix(call.Path, "/knowledge-base/export"):
			return 200, `{"files":[{"path":"p1.md","content":"Doc"}]}`
		default:
			return 404, `{}`
		}
	})
}
