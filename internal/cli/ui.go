package cli

// The `ui` command launches the web frontend (the frontend/ Next.js app) from
// the repository tree and points it at the configured API URL. It is pure
// process orchestration: the frontend is a separate npm package that the Go
// module never builds or imports. Process primitives sit behind injectable
// hooks so tests can verify wiring without spawning npm.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
)

// Environment variables the frontend reads; the CLI fills both from its own
// configuration so the frontend talks to the same API as every other command.
const (
	frontendURLEnv = "HINDSIGHT_CP_DATAPLANE_API_URL"
	frontendKeyEnv = "HINDSIGHT_CP_DATAPLANE_API_KEY"
)

// uiHooks bundles the process primitives; tests replace the variable.
var uiHooks = uiHooksType{
	lookPath: exec.LookPath,
	run:      runFrontendProcess,
}

type uiHooksType struct {
	// lookPath resolves an executable name.
	lookPath func(string) (string, error)
	// run starts the npm process in the foreground with the given
	// environment, forwards SIGINT/SIGTERM, and returns its exit code.
	run func(ctx context.Context, dir, exe string, env []string, stdin io.Reader, stdout, stderr *os.File) (int, error)
}

// runFrontendProcess is the default run hook: exec npm, forward termination
// signals, and propagate the child's exit status.
func runFrontendProcess(ctx context.Context, dir, exe string, env []string, stdin io.Reader, stdout, stderr *os.File) (int, error) {
	cmd := exec.Command(exe, "run", "dev")
	cmd.Dir = dir
	cmd.Env = env
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return 1, err
	}
	done := make(chan struct{})
	go func() {
		sigs := make(chan os.Signal, 2)
		signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
		select {
		case sig := <-sigs:
			_ = cmd.Process.Signal(sig)
		case <-done:
		}
		signal.Stop(sigs)
	}()
	err := cmd.Wait()
	close(done)
	if exitErr, ok := err.(*exec.ExitError); ok {
		return exitErr.ExitCode(), nil
	}
	if err != nil {
		return 1, err
	}
	return 0, nil
}

// RegisterUICommand wires the top-level `ui` command.
func RegisterUICommand(r *Registry) {
	cmd := r.Sub("ui", "Launch the web frontend")
	cmd.Flags().String("dir", "", "path to the frontend directory (default: ./frontend or ../frontend)")
	cmd.Action(func(ctx context.Context, d *Deps) error {
		dir, err := uiFrontendDir(d)
		if err != nil {
			return err
		}
		return uiLaunch(ctx, d, dir)
	})
}

// uiFrontendDir resolves the frontend directory: the --dir flag when given,
// otherwise ./frontend then ../frontend relative to the working directory.
func uiFrontendDir(d *Deps) (string, error) {
	if f := d.Flags.Lookup("dir"); f != nil && f.Value.String() != "" {
		dir := f.Value.String()
		if !uiIsFrontend(dir) {
			return "", fmt.Errorf("no frontend package (package.json) found at %s", dir)
		}
		return dir, nil
	}
	for _, candidate := range []string{"frontend", filepath.Join("..", "frontend")} {
		if uiIsFrontend(candidate) {
			abs, err := filepath.Abs(candidate)
			if err != nil {
				return candidate, nil
			}
			return abs, nil
		}
	}
	return "", errors.New("frontend directory not found: run from the repository root or pass --dir")
}

// uiIsFrontend reports whether dir looks like the frontend npm package.
func uiIsFrontend(dir string) bool {
	info, err := os.Stat(filepath.Join(dir, "package.json"))
	return err == nil && !info.IsDir()
}

// uiLaunch starts the frontend dev server with the API URL and key exported
// into its environment, streaming its output to the CLI's own streams.
func uiLaunch(ctx context.Context, d *Deps, dir string) error {
	npm, err := uiHooks.lookPath("npm")
	if err != nil {
		return errors.New("npm is required to run the frontend but was not found on PATH")
	}
	env := append(os.Environ(),
		frontendURLEnv+"="+d.Config.APIURL,
	)
	if d.Config.APIKey != "" {
		env = append(env, frontendKeyEnv+"="+d.Config.APIKey)
	}
	// The port is the `-p` in frontend/package.json's `dev` script; the message
	// restates it so the operator knows where the frontend will listen.
	d.UI.Plain("Starting frontend from %s (API: %s, port 19999)...", dir, d.Config.APIURL)
	d.UI.Plain("Press Ctrl+C to stop.")
	code, err := uiHooks.run(ctx, dir, npm, env, d.Stdin, stdoutFile(d), stderrFile(d))
	if err != nil {
		return err
	}
	if code != 0 {
		return fmt.Errorf("frontend exited with status %d", code)
	}
	return nil
}

// stdoutFile and stderrFile adapt the Deps writers back to process files.
// The CLI's own os.Stdout is the common case; anything else falls back to
// the process defaults so npm output stays visible.
func stdoutFile(d *Deps) *os.File {
	if f, ok := d.Stdout.(*os.File); ok {
		return f
	}
	return os.Stdout
}

func stderrFile(d *Deps) *os.File {
	if f, ok := d.Stderr.(*os.File); ok {
		return f
	}
	return os.Stderr
}
