package bankadmin

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
)

// UsageError marks a missing or malformed flag value. The dispatcher maps it
// to exit code 2, which is what the reference's parser does; every other
// failure is a runtime error and exits 1.
type UsageError struct{ msg string }

// Error returns the message.
func (e *UsageError) Error() string { return e.msg }

// NewUsageError builds a usage error from a format.
func NewUsageError(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

// ErrAborted is returned when an operator declines a confirmation prompt. The
// dispatcher reports it as "Aborted!" and exits 1, matching the reference's
// aborted-prompt behaviour.
var ErrAborted = errors.New("Aborted!") //nolint:staticcheck // exact copy text of the reference CLI

// Confirm writes prompt and reads one answer line from in. y/yes
// case-insensitively is a yes; anything else, including an empty answer or
// EOF, declines.
func Confirm(in io.Reader, out io.Writer, prompt string) bool {
	if in == nil {
		return false
	}
	_, _ = fmt.Fprintf(out, "%s [y/N]: ", prompt)
	line, _ := bufio.NewReader(in).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true
	}
	return false
}

// resolveRequired resolves a command's one required argument, given the
// positional (if any) and the flag's own value.
//
// The argument is accepted either way: the reference takes these as positionals
// (restore INPUT, decommission-worker WORKER_ID) and wenyuan's flag spellings are
// kept as an alternative, so both a reference-shaped script and an existing one
// work. Supplying both is a mistake rather than a precedence question.
func resolveRequired(positional, flagValue, name string) (string, error) {
	switch {
	case positional != "" && flagValue != "":
		return "", NewUsageError("give %s either as a positional argument or with its flag, not both", name)
	case flagValue != "":
		return flagValue, nil
	case positional != "":
		return positional, nil
	default:
		return "", NewUsageError("%s is required", name)
	}
}

// parsePositionals parses args where flags and positional tokens are
// interspersed, returning the single positional. The standard flag set stops at
// the first non-flag, so parsing resumes after each positional; more than one
// positional is an error.
func parsePositionals(fs *flag.FlagSet, args []string) (string, error) {
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return "", err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			break
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
	switch len(positionals) {
	case 0:
		return "", nil
	case 1:
		return positionals[0], nil
	default:
		return "", NewUsageError("unexpected extra argument %q", positionals[1])
	}
}

// RenameFlags is the parsed administrative rename command surface.
type RenameFlags struct {
	Source string
	Target string
	DryRun bool
}

// ParseRename parses a bank rename command. Flags may be supplied as
// --from/--to/--dry-run or with the short aliases -f/-t/-n.
func ParseRename(args []string) (RenameFlags, error) {
	fs := flag.NewFlagSet("bank rename", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags RenameFlags
	fs.StringVar(&flags.Source, "from", "", "source bank identifier")
	fs.StringVar(&flags.Source, "f", "", "source bank identifier (short)")
	fs.StringVar(&flags.Source, "source", "", "source bank identifier (deprecated spelling of --from)")
	fs.StringVar(&flags.Target, "to", "", "target bank identifier")
	fs.StringVar(&flags.Target, "t", "", "target bank identifier (short)")
	fs.BoolVar(&flags.DryRun, "dry-run", false, "report the plan without changing anything")
	fs.BoolVar(&flags.DryRun, "n", false, "report the plan without changing anything (short)")
	if err := fs.Parse(args); err != nil {
		return RenameFlags{}, err
	}
	if flags.Source == "" || flags.Target == "" {
		return RenameFlags{}, NewUsageError("both a source and a target identifier are required")
	}
	return flags, nil
}

// RunRename executes a parsed rename against a store and reports the plan.
func RunRename(ctx context.Context, store storage.Store, flags RenameFlags, out io.Writer) (RenamePlan, error) {
	plan, err := (Renamer{Store: store}).Rename(ctx, flags.Source, flags.Target, flags.DryRun)
	if err != nil {
		return RenamePlan{}, err
	}
	verb := "moved"
	if plan.DryRun {
		verb = "would move"
	}
	_, _ = fmt.Fprintf(out, "%s %d bank row(s) and %d alias row(s) from %q to %q\n",
		verb, plan.Rows, plan.Aliases, plan.Source, plan.Target)
	return plan, nil
}
