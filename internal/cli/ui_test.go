package cli

// Tests for the `ui` command wiring: console directory resolution, environment
// construction, and exit-code propagation. The process hooks are replaced, so
// no npm or console tree is needed.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// uiCapture records what the command passed to the run hook.
type uiCapture struct {
	dir    string
	exe    string
	env    []string
	stdout io.Writer
	code   int
}

func TestUILaunchesConsoleWithAPIEnv(t *testing.T) {
	root := t.TempDir()
	consoleDir := filepath.Join(root, "console")
	if err := os.MkdirAll(consoleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consoleDir, "package.json"), []byte(`{"name":"console"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// Work relative to root so the default ./console resolution hits.
	previous, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	var captured uiCapture
	original := uiHooks
	uiHooks = uiHooksType{
		lookPath: func(string) (string, error) { return "/usr/bin/npm", nil },
		run: func(_ context.Context, dir, exe string, env []string, _ io.Reader, stdout, _ *os.File) (int, error) {
			captured = uiCapture{dir: dir, exe: exe, env: env, stdout: stdout, code: 0}
			return 0, nil
		},
	}
	t.Cleanup(func() { uiHooks = original })

	r := NewRegistry()
	RegisterUICommand(r)
	cmd, rest, err := r.Resolve([]string{"ui"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := cmd.ParseArgs(rest); err != nil {
		t.Fatalf("parse: %v", err)
	}
	var stdout, stderr bytes.Buffer
	deps := &Deps{
		Ctx:    context.Background(),
		Flags:  cmd.Flags(),
		Stdout: &stdout,
		Stderr: &stderr,
		Stdin:  strings.NewReader(""),
		Format: FormatPretty,
		Config: Config{APIURL: "http://api.local:8888", APIKey: "sekrit"},
		UI:     NewUI(&stdout, &stderr, ColorEnv{}),
	}
	if err := cmd.Execute(deps.Ctx, deps); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if captured.dir != consoleDir {
		t.Errorf("dir = %q, want %q", captured.dir, consoleDir)
	}
	if filepath.Base(captured.exe) != "npm" {
		t.Errorf("exe = %q, want npm", captured.exe)
	}
	if !uiEnvHas(captured.env, consoleURLEnv+"=http://api.local:8888") {
		t.Errorf("env missing %s, got %v", consoleURLEnv, captured.env)
	}
	if !uiEnvHas(captured.env, consoleKeyEnv+"=sekrit") {
		t.Errorf("env missing %s, got %v", consoleKeyEnv, captured.env)
	}
}

func TestUIWithoutKeyOmitsKeyEnv(t *testing.T) {
	root := t.TempDir()
	consoleDir := filepath.Join(root, "console")
	if err := os.MkdirAll(consoleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consoleDir, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	var captured uiCapture
	original := uiHooks
	uiHooks = uiHooksType{
		lookPath: func(string) (string, error) { return "/usr/bin/npm", nil },
		run: func(_ context.Context, dir, exe string, env []string, _ io.Reader, _, _ *os.File) (int, error) {
			captured = uiCapture{dir: dir, exe: exe, env: env}
			return 0, nil
		},
	}
	t.Cleanup(func() { uiHooks = original })

	r := NewRegistry()
	RegisterUICommand(r)
	cmd, rest, err := r.Resolve([]string{"ui"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := cmd.ParseArgs(rest); err != nil {
		t.Fatalf("parse: %v", err)
	}
	deps := &Deps{
		Ctx:    context.Background(),
		Flags:  cmd.Flags(),
		Stdout: io.Discard,
		Stderr: io.Discard,
		Stdin:  strings.NewReader(""),
		Format: FormatPretty,
		Config: Config{APIURL: "http://localhost:8888"},
		UI:     NewUI(io.Discard, io.Discard, ColorEnv{}),
	}
	if err := cmd.Execute(deps.Ctx, deps); err != nil {
		t.Fatalf("execute: %v", err)
	}
	for _, entry := range captured.env {
		if strings.HasPrefix(entry, consoleKeyEnv+"=") {
			t.Errorf("key env set without a configured key: %s", entry)
		}
	}
}

func TestUIMissingConsoleDir(t *testing.T) {
	root := t.TempDir()
	previous, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	original := uiHooks
	uiHooks = uiHooksType{
		lookPath: func(string) (string, error) { return "/usr/bin/npm", nil },
		run: func(context.Context, string, string, []string, io.Reader, *os.File, *os.File) (int, error) {
			t.Fatal("run must not be called")
			return 0, nil
		},
	}
	t.Cleanup(func() { uiHooks = original })

	r := NewRegistry()
	RegisterUICommand(r)
	cmd, rest, err := r.Resolve([]string{"ui"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := cmd.ParseArgs(rest); err != nil {
		t.Fatalf("parse: %v", err)
	}
	deps := &Deps{
		Ctx:    context.Background(),
		Flags:  cmd.Flags(),
		Stdout: io.Discard,
		Stderr: io.Discard,
		Stdin:  strings.NewReader(""),
		Format: FormatPretty,
		Config: Config{APIURL: "http://localhost:8888"},
		UI:     NewUI(io.Discard, io.Discard, ColorEnv{}),
	}
	err = cmd.Execute(deps.Ctx, deps)
	if err == nil || !strings.Contains(err.Error(), "console directory not found") {
		t.Fatalf("err = %v, want console directory not found", err)
	}
}

func TestUIExitCodePropagation(t *testing.T) {
	root := t.TempDir()
	consoleDir := filepath.Join(root, "console")
	if err := os.MkdirAll(consoleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consoleDir, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	original := uiHooks
	uiHooks = uiHooksType{
		lookPath: func(string) (string, error) { return "/usr/bin/npm", nil },
		run: func(context.Context, string, string, []string, io.Reader, *os.File, *os.File) (int, error) {
			return 3, nil
		},
	}
	t.Cleanup(func() { uiHooks = original })

	r := NewRegistry()
	RegisterUICommand(r)
	cmd, rest, err := r.Resolve([]string{"ui"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := cmd.ParseArgs(rest); err != nil {
		t.Fatalf("parse: %v", err)
	}
	deps := &Deps{
		Ctx:    context.Background(),
		Flags:  cmd.Flags(),
		Stdout: io.Discard,
		Stderr: io.Discard,
		Stdin:  strings.NewReader(""),
		Format: FormatPretty,
		Config: Config{APIURL: "http://localhost:8888"},
		UI:     NewUI(io.Discard, io.Discard, ColorEnv{}),
	}
	err = cmd.Execute(deps.Ctx, deps)
	if err == nil || !strings.Contains(err.Error(), "status 3") {
		t.Fatalf("err = %v, want status 3", err)
	}
}

func TestUIMissingNPM(t *testing.T) {
	root := t.TempDir()
	consoleDir := filepath.Join(root, "console")
	if err := os.MkdirAll(consoleDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consoleDir, "package.json"), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	previous, _ := os.Getwd()
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(previous) })

	original := uiHooks
	uiHooks = uiHooksType{
		lookPath: func(string) (string, error) { return "", errors.New("not found") },
		run: func(context.Context, string, string, []string, io.Reader, *os.File, *os.File) (int, error) {
			t.Fatal("run must not be called")
			return 0, nil
		},
	}
	t.Cleanup(func() { uiHooks = original })

	r := NewRegistry()
	RegisterUICommand(r)
	cmd, rest, err := r.Resolve([]string{"ui"})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if _, err := cmd.ParseArgs(rest); err != nil {
		t.Fatalf("parse: %v", err)
	}
	deps := &Deps{
		Ctx:    context.Background(),
		Flags:  cmd.Flags(),
		Stdout: io.Discard,
		Stderr: io.Discard,
		Stdin:  strings.NewReader(""),
		Format: FormatPretty,
		Config: Config{APIURL: "http://localhost:8888"},
		UI:     NewUI(io.Discard, io.Discard, ColorEnv{}),
	}
	err = cmd.Execute(deps.Ctx, deps)
	if err == nil || !strings.Contains(err.Error(), "npm is required") {
		t.Fatalf("err = %v, want npm is required", err)
	}
}

func uiEnvHas(env []string, want string) bool {
	for _, entry := range env {
		if entry == want {
			return true
		}
	}
	return false
}
