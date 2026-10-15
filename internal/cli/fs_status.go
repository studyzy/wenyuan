package cli

// The fs status command: the health verdict from the two orthogonal
// signals — daemon liveness and sync freshness — and its pretty and machine
// renderings.

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/cli/fsengine"
)

func registerFSStatus(fs *SubCmd) {
	fc := fsBindCommon(fs)
	var staleAfter triStateInt
	fs.Flags().Var(&staleAfter, "stale-after", "override the staleness threshold in seconds")
	fs.Action(func(ctx context.Context, d *Deps) error {
		dir, err := fsDirArg(d)
		if err != nil {
			return err
		}
		r := fsResolve(d, dir, fc)
		pid := fsLoadLivePID(r.Dir)
		state := fsengine.LoadState(r.Dir, r.BankID, r.APIURL)
		report := fsComputeHealth(time.Now(), r, pid, state, staleAfter)
		if d.Format.IsMachine() {
			if err := d.Format.Render(d.UI.Stdout, report); err != nil {
				return err
			}
		} else {
			fsRenderStatus(d.UI, report)
		}
		if !report.Healthy {
			// The health report is printed either way; the exit code
			// carries the verdict.
			os.Exit(1)
		}
		return nil
	})
}

// fsLoadLivePID reads the pid record and removes it as a side effect when
// the recorded process is dead, making repeated status calls idempotent
// after a crash.
func fsLoadLivePID(dir string) *fsengine.PIDRecord {
	rec, ok := fsengine.LoadPID(dir)
	if !ok {
		return nil
	}
	if !fsHooks.alive(rec.PID) {
		_ = os.Remove(fsengine.PIDPath(dir))
		return nil
	}
	return &rec
}

// fsStatusReport is the health report. The JSON keys are the documented
// snake_case contract, in the documented order.
type fsStatusReport struct {
	Healthy         bool    `json:"healthy" yaml:"healthy"`
	Status          string  `json:"status" yaml:"status"`
	Mount           string  `json:"mount" yaml:"mount"`
	Bank            string  `json:"bank" yaml:"bank"`
	APIURL          string  `json:"api_url" yaml:"api_url"`
	Mode            string  `json:"mode" yaml:"mode"`
	DaemonRunning   bool    `json:"daemon_running" yaml:"daemon_running"`
	DaemonPID       *int    `json:"daemon_pid" yaml:"daemon_pid"`
	DaemonStartedAt *string `json:"daemon_started_at" yaml:"daemon_started_at"`
	DaemonInterval  *int    `json:"daemon_interval_seconds" yaml:"daemon_interval_seconds"`
	LastSyncAt      *string `json:"last_sync_at" yaml:"last_sync_at"`
	LastSyncOK      bool    `json:"last_sync_ok" yaml:"last_sync_ok"`
	LastSyncAge     *int64  `json:"last_sync_age_seconds" yaml:"last_sync_age_seconds"`
	LastError       *string `json:"last_error" yaml:"last_error"`
	StaleAfter      int     `json:"stale_after_seconds" yaml:"stale_after_seconds"`
	MirroredFiles   int     `json:"mirrored_files" yaml:"mirrored_files"`
}

// fsComputeHealth applies the first-match verdict order: dead, stale
// (never synced), failed, stale (wedged), ok. The staleness threshold is
// max(interval × 3, 15) seconds unless overridden outright.
func fsComputeHealth(now time.Time, r fsResolved, pid *fsengine.PIDRecord, state fsengine.State, staleAfterOverride triStateInt) fsStatusReport {
	interval := r.Interval
	if pid != nil && pid.IntervalSeconds > 0 {
		interval = pid.IntervalSeconds
	}
	staleAfter := interval * 3
	if staleAfter < 15 {
		staleAfter = 15
	}
	// An explicit --stale-after replaces the computed threshold outright,
	// including a literal 0.
	if staleAfterOverride.set {
		staleAfter = staleAfterOverride.value
	}

	status := "ok"
	switch {
	case pid == nil:
		status = "dead"
	case state.LastSyncAt == nil:
		status = "stale"
	case !state.LastSyncOk:
		status = "failed"
	default:
		ts, err := time.Parse(time.RFC3339, *state.LastSyncAt)
		if err != nil || now.Sub(ts) >= time.Duration(staleAfter)*time.Second {
			status = "stale"
		}
	}

	var age *int64
	if state.LastSyncAt != nil {
		if ts, err := time.Parse(time.RFC3339, *state.LastSyncAt); err == nil {
			d := now.Sub(ts)
			if d < 0 {
				d = 0
			}
			seconds := int64(d.Seconds())
			age = &seconds
		}
	}

	bank, apiURL := r.BankID, r.APIURL
	if state.BankID != "" {
		bank = state.BankID
	}
	if state.APIURL != "" {
		apiURL = state.APIURL
	}
	mode := "read-only"
	if r.Writable {
		mode = "writable"
	}

	report := fsStatusReport{
		Healthy:       status == "ok",
		Status:        status,
		Mount:         r.Dir,
		Bank:          bank,
		APIURL:        apiURL,
		Mode:          mode,
		DaemonRunning: pid != nil,
		StaleAfter:    staleAfter,
		MirroredFiles: len(state.Files),
		LastSyncAge:   age,
	}
	report.LastSyncOK = state.LastSyncOk
	if state.LastSyncAt != nil {
		report.LastSyncAt = state.LastSyncAt
	}
	if state.LastError != nil {
		report.LastError = state.LastError
	}
	if pid != nil {
		report.DaemonPID = &pid.PID
		report.DaemonStartedAt = &pid.StartedAt
		report.DaemonInterval = &pid.IntervalSeconds
	}
	return report
}

// fsRenderStatus prints the pretty health layout. Unhealthy verdicts render
// uppercase.
func fsRenderStatus(ui *UI, report fsStatusReport) {
	bank := report.Bank
	if bank == "" {
		bank = "(unset)"
	}
	daemon := "stopped"
	if report.DaemonRunning {
		daemon = fmt.Sprintf("running (pid %d)", *report.DaemonPID)
	}
	mode := "read-only (edits blocked)"
	if report.Mode == "writable" {
		mode = "writable (one-way; edits reverted on refresh)"
	}
	ui.Plain("Mount:    %s", report.Mount)
	ui.Plain("Bank:     %s", bank)
	ui.Plain("API:      %s", report.APIURL)
	ui.Plain("Mode:     %s", mode)
	ui.Plain("Daemon:   %s", daemon)
	if report.DaemonStartedAt != nil {
		ui.Plain("Interval: %ds (started %s)", *report.DaemonInterval, *report.DaemonStartedAt)
	}
	syncLine := "never (never)"
	switch {
	case report.LastSyncAt != nil && report.LastSyncAge != nil:
		syncLine = fmt.Sprintf("%s (%ds ago)", *report.LastSyncAt, *report.LastSyncAge)
	case report.LastSyncAt != nil:
		syncLine = fmt.Sprintf("%s (never)", *report.LastSyncAt)
	}
	if !report.LastSyncOK {
		syncLine += " (FAILED)"
	}
	ui.Plain("Last sync: %s", syncLine)
	if report.LastError != nil && *report.LastError != "" {
		ui.Plain("Last error: %s", *report.LastError)
	}
	ui.Plain("Mirrored: %d file(s)", report.MirroredFiles)
	health := report.Status
	if !report.Healthy {
		health = strings.ToUpper(health)
	}
	ui.Plain("Health:   %s", health)
}
