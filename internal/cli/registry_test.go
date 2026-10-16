package cli

import (
	"errors"
	"strings"
	"testing"
)

// TestRegistryResolvesAlias checks the alias mechanism the reference CLI uses
// for alternates like `tui` for `explore`: the alias resolves to the same
// command, leaves its flags intact, and is not listed as a command of its own.
func TestRegistryResolvesAlias(t *testing.T) {
	r := NewRegistry()
	explore := r.Sub("explore", "open the interactive explorer").Alias("tui")
	explore.Flags().Bool("probe", false, "a bound flag")

	cmd, rest, err := r.Resolve([]string{"tui", "--probe"})
	if err != nil {
		t.Fatalf("resolve alias: %v", err)
	}
	if cmd != explore {
		t.Fatalf("alias resolved to %q, want explore", cmd.Name())
	}
	if len(rest) != 1 || rest[0] != "--probe" {
		t.Fatalf("rest = %v", rest)
	}
	if got, ok := r.Lookup("tui"); !ok || got != explore {
		t.Fatalf("Lookup(tui) = %v, %v", got, ok)
	}

	var listed []string
	for _, c := range r.Commands() {
		listed = append(listed, c.Name())
	}
	if strings.Join(listed, ",") != "explore" {
		t.Fatalf("aliases must not be listed as commands: %v", listed)
	}
}

// TestRegistryResolvesChildAlias covers the same mechanism one level down.
func TestRegistryResolvesChildAlias(t *testing.T) {
	r := NewRegistry()
	parent := r.Sub("bank", "manage banks")
	child := parent.Sub("list", "list banks").Alias("ls")

	cmd, rest, err := r.Resolve([]string{"bank", "ls"})
	if err != nil {
		t.Fatalf("resolve child alias: %v", err)
	}
	if cmd != child || len(rest) != 0 {
		t.Fatalf("resolved %q rest=%v", cmd.Name(), rest)
	}
}

// TestRegistryUnknownAliasStaysUnknown keeps the failure mode intact.
func TestRegistryUnknownAliasStaysUnknown(t *testing.T) {
	r := NewRegistry()
	r.Sub("explore", "x").Alias("tui")
	_, _, err := r.Resolve([]string{"nope"})
	var usage *UsageError
	if err == nil || !errors.As(err, &usage) {
		t.Fatalf("err = %v, want a usage error", err)
	}
}

// TestHiddenCommandStaysDispatchable checks that hiding a command only keeps
// it out of listings and usage hints: resolving it still walks to it.
func TestHiddenCommandStaysDispatchable(t *testing.T) {
	r := NewRegistry()
	parent := r.Sub("fs", "mirror")
	parent.Sub("mount", "mount it")
	run := parent.Sub("run", "run the loop").Hide()

	if !run.Hidden() {
		t.Fatal("run should be hidden")
	}
	var listed []string
	for _, c := range parent.Listed() {
		listed = append(listed, c.Name())
	}
	if strings.Join(listed, ",") != "mount" {
		t.Fatalf("hidden command leaked into the listing: %v", listed)
	}

	cmd, rest, err := r.Resolve([]string{"fs", "run"})
	if err != nil {
		t.Fatalf("resolve hidden command: %v", err)
	}
	if cmd != run || len(rest) != 0 {
		t.Fatalf("resolved %q rest=%v", cmd.Name(), rest)
	}
}

// TestHiddenCommandsOmittedFromUsageHints checks the "one of: ..." error
// stays free of hidden commands.
func TestHiddenCommandsOmittedFromUsageHints(t *testing.T) {
	r := NewRegistry()
	parent := r.Sub("fs", "mirror")
	parent.Sub("mount", "mount it").Hide()
	parent.Sub("stop", "stop it")

	_, _, err := r.Resolve([]string{"fs", "bogus"})
	if err == nil {
		t.Fatal("want an error")
	}
	if strings.Contains(err.Error(), "mount") {
		t.Fatalf("hidden command leaked into the hint: %v", err)
	}
	if !strings.Contains(err.Error(), "stop") {
		t.Fatalf("visible command missing from the hint: %v", err)
	}
}

// TestNoSpinnerMarker records the long-running opt-out.
func TestNoSpinnerMarker(t *testing.T) {
	r := NewRegistry()
	plain := r.Sub("health", "check health")
	loop := r.Sub("mount", "run forever").NoSpinner()
	if !plain.UsesSpinner() || loop.UsesSpinner() {
		t.Fatalf("UsesSpinner: plain=%v loop=%v", plain.UsesSpinner(), loop.UsesSpinner())
	}
}
