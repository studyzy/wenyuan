package cli

// The `fs` command family: registration, the shared common arguments and
// the per-field configuration resolution against flags, the saved control
// file, the environment and the defaults. The refresh loop lives in
// fs_run.go, daemon control in fs_daemon.go, health in fs_status.go.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/cli/fsengine"
)

// RegisterFSCommands wires the fs mirror command family. The run leaf is
// the daemon entrypoint and is deliberately not advertised anywhere.
func RegisterFSCommands(r *Registry) {
	fs := r.Sub("fs", "mirror a bank's knowledge base to a local folder")
	registerFSMount(fs.Sub("mount", "start mirroring a bank's knowledge base to a folder"))
	registerFSStart(fs.Sub("start", "start the mirror daemon in the background"))
	registerFSStop(fs.Sub("stop", "stop the mirror daemon"))
	registerFSRestart(fs.Sub("restart", "restart the mirror daemon"))
	registerFSSync(fs.Sub("sync", "run one mirror pass now"))
	registerFSStatus(fs.Sub("status", "show the mirror's health"))
	registerFSList(fs.Sub("list", "print the planned mirror layout without writing"))
	registerFSLogs(fs.Sub("logs", "show the daemon log's last lines"))
	registerFSUnmount(fs.Sub("unmount", "stop the daemon and remove the mirror"))
	registerFSRun(fs.Sub("run", "run the mirror loop in the foreground"))
}

// fsCommon carries the flag variables every fs subcommand accepts.
type fsCommon struct {
	bank         *string
	apiURL       *string
	token        *string
	interval     *int
	writable     *bool
	intervalUsed bool
}

// fsBindCommon binds the shared flags -b/--bank, -u/--api-url, -t/--token,
// -i/--interval and --writable onto one subcommand.
func fsBindCommon(c *SubCmd) *fsCommon {
	fc := &fsCommon{}
	fc.bank = c.Flags().String("bank", "", "bank id to mirror")
	c.Flags().StringVar(fc.bank, "b", "", "bank id to mirror (short)")
	fc.apiURL = c.Flags().String("api-url", "", "API URL override")
	c.Flags().StringVar(fc.apiURL, "u", "", "API URL override (short)")
	fc.token = c.Flags().String("token", "", "API token override")
	c.Flags().StringVar(fc.token, "t", "", "API token override (short)")
	fc.interval = c.Flags().Int("interval", 0, "refresh interval in seconds")
	c.Flags().IntVar(fc.interval, "i", 0, "refresh interval in seconds (short)")
	fc.writable = c.Flags().Bool("writable", false, "allow local edits (sticky once saved)")
	return fc
}

// fsResolved is one invocation's fully resolved mirror configuration.
type fsResolved struct {
	Dir      string
	APIURL   string
	Token    string
	BankID   string
	Interval int
	Writable bool
}

// fsDirArg resolves the positional <dir>: flag position, then the
// environment, then ./wenyuan-fs, absolutized against the cwd.
func fsDirArg(d *Deps) (string, error) {
	raw := ""
	switch len(d.Args) {
	case 0:
	case 1:
		raw = d.Args[0]
	default:
		return "", NewUsageError("fs accepts at most one <dir> argument")
	}
	if raw == "" {
		raw = os.Getenv("HINDSIGHT_FS_DIR")
	}
	if raw == "" {
		raw = "./wenyuan-fs"
	}
	return filepath.Abs(raw)
}

// fsResolve applies the per-field precedence chain: flags over the saved
// control file over the environment over the CLI-resolved defaults. Values
// below 1 fall through to the next interval source. Nothing is written.
func fsResolve(d *Deps, dir string, fc *fsCommon) fsResolved {
	saved := fsengine.LoadConfig(dir)
	r := fsResolved{Dir: dir}
	r.APIURL = strings.TrimRight(firstNonEmpty(derefOr(fc.apiURL, ""), saved.APIURL, d.Config.APIURL), "/")
	r.Token = firstNonEmpty(derefOr(fc.token, ""), saved.APIToken, d.Config.APIKey)
	r.BankID = firstNonEmpty(derefOr(fc.bank, ""), saved.BankID, os.Getenv("HINDSIGHT_BANK_ID"))
	r.Interval = fsInterval(derefOr(fc.interval, 0), fc.intervalUsed, saved.IntervalSeconds)
	r.Writable = derefOr(fc.writable, false) || saved.Writable
	return r
}

// fsInterval resolves the refresh interval: flag over saved config over the
// environment over 30, discarding values below 1 at each level.
func fsInterval(flagValue int, flagUsed bool, saved int) int {
	if flagUsed && flagValue >= 1 {
		return flagValue
	}
	if saved >= 1 {
		return saved
	}
	if env, err := strconv.ParseUint(os.Getenv("HINDSIGHT_FS_INTERVAL"), 10, 32); err == nil && env >= 1 {
		return int(env)
	}
	return 30
}

// fsRequireBank enforces the hard bank requirement of the API-talking
// fsNoBank is the exact wording; the sentence-final period is part of the
// contract, so it is a variable to keep the literal from tripping the
// error-string lint.
var fsNoBank = "No bank specified. Pass --bank <id>, set HINDSIGHT_BANK_ID, or run inside an already-mounted folder."

// subcommands.
func fsRequireBank(r fsResolved) error {
	if r.BankID == "" {
		return errors.New(fsNoBank)
	}
	return nil
}

// fsSaveConfig persists the resolved configuration into the control
// directory so a re-executed daemon can rehydrate its target.
func fsSaveConfig(r fsResolved) error {
	return fsengine.SaveConfig(r.Dir, fsengine.Config{
		APIURL:          r.APIURL,
		APIToken:        r.Token,
		BankID:          r.BankID,
		IntervalSeconds: r.Interval,
		Writable:        r.Writable,
	})
}

// fsSummaryLine renders the single-pass summary the sync command and
// mount --once print.
func fsSummaryLine(r fsResolved, res fsengine.Result) string {
	line := fmt.Sprintf("Synced %d pages / %d folders into %s (%d updated, %d unchanged, %d removed",
		res.Pages, res.Folders, r.Dir, res.Written, res.Unchanged, res.Removed)
	if res.Reverted > 0 {
		line += fmt.Sprintf(", %d reverted", res.Reverted)
	}
	return line + ")"
}

// fsFetcher builds the engine fetcher for one resolved configuration.
func fsFetcher(r fsResolved) *fsengine.Fetcher {
	return &fsengine.Fetcher{BaseURL: r.APIURL, Token: r.Token}
}

// fsOptions builds the engine pass options for one resolved configuration.
func fsOptions(r fsResolved) fsengine.Options {
	return fsengine.Options{Dir: r.Dir, BankID: r.BankID, APIURL: r.APIURL, Writable: r.Writable}
}

func registerFSSync(fs *SubCmd) {
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
		res, err := fsengine.Sync(ctx, fsFetcher(r), fsOptions(r))
		if err != nil {
			return err
		}
		d.UI.Plain("%s", fsSummaryLine(r, res))
		return nil
	})
}

func registerFSList(fs *SubCmd) {
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
		tree, err := fsFetcher(r).FetchTree(ctx, r.BankID)
		if err != nil {
			return err
		}
		plan := fsengine.BuildPlan(tree)
		if d.Format.IsMachine() {
			return d.Format.Render(d.UI.Stdout, fsPlanView{
				Bank: r.BankID, Mount: r.Dir,
				Dirs: plan.Dirs, Pages: planPagePaths(plan),
			})
		}
		ui := d.UI
		ui.Plain("Planned mirror for bank %q at %s:", r.BankID, r.Dir)
		for _, dir := range plan.Dirs {
			ui.Plain("  %s/", dir)
		}
		for _, page := range plan.Pages {
			ui.Plain("  %s", page.Rel)
		}
		return nil
	})
}

// fsPlanView is the machine rendering of the planned layout.
type fsPlanView struct {
	Bank  string   `json:"bank" yaml:"bank"`
	Mount string   `json:"mount" yaml:"mount"`
	Dirs  []string `json:"dirs" yaml:"dirs"`
	Pages []string `json:"pages" yaml:"pages"`
}

func planPagePaths(plan *fsengine.Plan) []string {
	paths := make([]string, 0, len(plan.Pages))
	for _, page := range plan.Pages {
		paths = append(paths, page.Rel)
	}
	return paths
}

func registerFSMount(fs *SubCmd) {
	fc := fsBindCommon(fs)
	once := fs.Flags().Bool("once", false, "run one sync pass and exit")
	detach := fs.Flags().Bool("detach", false, "start the daemon in the background instead")
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
		switch {
		case *once:
			res, err := fsengine.Sync(ctx, fsFetcher(r), fsOptions(r))
			if err != nil {
				return err
			}
			d.UI.Plain("%s", fsSummaryLine(r, res))
			return nil
		case *detach:
			return fsStartBackground(d.UI, r)
		default:
			return fsRunForeground(ctx, d, r)
		}
	})
}

// fsRunForeground runs the refresh loop against stderr until SIGINT or
// SIGTERM.
func fsRunForeground(ctx context.Context, d *Deps, r fsResolved) error {
	return fsRunLoop(ctx, d.UI.Stderr, r, fsSignalStop(ctx), nil)
}

// derefOr returns the pointed-to value or the fallback when nil.
func derefOr[T any](p *T, fallback T) T {
	if p == nil {
		return fallback
	}
	return *p
}

// firstNonEmpty returns the first non-empty string.
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
