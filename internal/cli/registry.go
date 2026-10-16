package cli

import (
	"context"
	"flag"
	"fmt"
	"io"
	"strings"
)

// Deps carries what a command action may use. Commands registered with
// SkipConfig run before the configuration loads and see a zero Config and a
// nil Client.
type Deps struct {
	// Ctx is the caller's context.
	Ctx context.Context
	// Args holds the positional arguments left after the command path and
	// its flags were parsed.
	Args []string
	// Flags is the command's parsed flag set; actions read their bound
	// variables directly, this is for Visit and arg-count checks.
	Flags *flag.FlagSet
	// Stdout, Stderr and Stdin are the process streams.
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
	// Format is the resolved -o/--output value.
	Format Format
	// Verbose reports the -v flag.
	Verbose bool
	// Profile is the -p flag value, empty when unset.
	Profile string
	// Config is the resolved configuration.
	Config Config
	// Client is built from Config; nil for pre-configuration commands.
	Client *Client
	// UI renders styled output.
	UI *UI
}

// UsageError marks an argument-parsing failure; main maps it to exit code 2.
type UsageError struct{ msg string }

// Error returns the message.
func (e *UsageError) Error() string { return e.msg }

// NewUsageError builds a usage error from a format.
func NewUsageError(format string, args ...any) error {
	return &UsageError{msg: fmt.Sprintf(format, args...)}
}

// CommandError marks a validation failure raised inside a command body, once
// the arguments have parsed. The reference CLI reports these as ordinary
// command failures, so main prints the message and maps it to exit code 1;
// only argument parsing exits 2.
type CommandError struct{ msg string }

// Error returns the message.
func (e *CommandError) Error() string { return e.msg }

// NewCommandError builds a command error from a format.
func NewCommandError(format string, args ...any) error {
	return &CommandError{msg: fmt.Sprintf(format, args...)}
}

// SubCmd is one node of the command table: a leaf command or a group.
type SubCmd struct {
	name       string
	summary    string
	aliases    []string
	skipConfig bool
	hidden     bool
	noSpinner  bool
	flags      *flag.FlagSet
	action     func(context.Context, *Deps) error
	children   []*SubCmd
	index      map[string]*SubCmd
}

// Name returns the command name.
func (s *SubCmd) Name() string { return s.name }

// Summary returns the command's one-line description.
func (s *SubCmd) Summary() string { return s.summary }

// Alias registers alternate spellings that resolve to this command, for the
// reference CLI's aliased subcommands (for example `tui` for `explore`).
// Aliases are accepted by Resolve but are not listed as commands.
func (s *SubCmd) Alias(names ...string) *SubCmd {
	s.aliases = append(s.aliases, names...)
	return s
}

// NeedsConfig reports whether the command runs after the configuration
// loads and receives a built client.
func (s *SubCmd) NeedsConfig() bool { return !s.skipConfig }

// Flags returns the command's flag set, creating it on first use. Register
// functions bind flag variables here and read them in the action closure.
func (s *SubCmd) Flags() *flag.FlagSet {
	if s.flags == nil {
		s.flags = flag.NewFlagSet(s.name, flag.ContinueOnError)
		s.flags.SetOutput(io.Discard)
	}
	return s.flags
}

// Action binds the command's implementation and returns the command for
// chaining.
func (s *SubCmd) Action(fn func(context.Context, *Deps) error) *SubCmd {
	s.action = fn
	return s
}

// SkipConfig marks the command as running before any configuration load and
// returns the command for chaining.
func (s *SubCmd) SkipConfig() *SubCmd {
	s.skipConfig = true
	return s
}

// Hide marks the command as unlisted: it stays dispatchable by name but is
// omitted from help listings and from the "one of: ..." hints in usage
// errors. It is for internal entrypoints that are deliberately not
// advertised.
func (s *SubCmd) Hide() *SubCmd {
	s.hidden = true
	return s
}

// Hidden reports whether the command is hidden from listings.
func (s *SubCmd) Hidden() bool { return s.hidden }

// NoSpinner marks a long-running interactive or streaming command (a
// full-screen TUI, a daemon loop, a dev server) that must not show the
// progress line, and returns the command for chaining.
func (s *SubCmd) NoSpinner() *SubCmd {
	s.noSpinner = true
	return s
}

// UsesSpinner reports whether the command may show the progress line.
func (s *SubCmd) UsesSpinner() bool { return !s.noSpinner }

// Sub creates a child command under this one, for grouped surfaces such as
// `wenyuan-admin profile create`.
func (s *SubCmd) Sub(name, summary string) *SubCmd {
	child := &SubCmd{name: name, summary: summary, index: map[string]*SubCmd{}}
	s.children = append(s.children, child)
	s.index[name] = child
	return child
}

// ParseArgs parses the command's flags from interspersed arguments and
// returns the positional arguments. The standard flag set stops at the first
// positional, so parsing resumes after each one; this keeps
// `wenyuan-admin profile create NAME --api-url URL` working.
func (s *SubCmd) ParseArgs(args []string) ([]string, error) {
	fs := s.Flags()
	var positionals []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return positionals, nil
		}
		positionals = append(positionals, rest[0])
		args = rest[1:]
	}
}

// Execute runs the command's action.
func (s *SubCmd) Execute(ctx context.Context, d *Deps) error {
	if s.action == nil {
		return NewUsageError("%s is not implemented yet", s.name)
	}
	return s.action(ctx, d)
}

// Registry is the top-level command table.
type Registry struct {
	roots []*SubCmd
	index map[string]*SubCmd
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{index: map[string]*SubCmd{}}
}

// Sub registers a top-level command.
func (r *Registry) Sub(name, summary string) *SubCmd {
	cmd := &SubCmd{name: name, summary: summary, index: map[string]*SubCmd{}}
	r.roots = append(r.roots, cmd)
	r.index[name] = cmd
	return cmd
}

// Lookup finds a top-level command by name or alias.
func (r *Registry) Lookup(name string) (*SubCmd, bool) {
	if cmd, ok := r.index[name]; ok {
		return cmd, true
	}
	for _, cmd := range r.roots {
		if cmd.matchesAlias(name) {
			return cmd, true
		}
	}
	return nil, false
}

// Commands lists the top-level commands in registration order.
func (r *Registry) Commands() []*SubCmd {
	return append([]*SubCmd(nil), r.roots...)
}

// Listed lists the top-level commands a help listing shows: everything but
// the hidden ones, in registration order.
func (r *Registry) Listed() []*SubCmd {
	return listedCommands(r.roots)
}

// Resolve walks positional names from the roots down through groups and
// returns the deepest command plus the remaining arguments.
func (r *Registry) Resolve(args []string) (*SubCmd, []string, error) {
	if len(args) == 0 {
		return nil, nil, NewUsageError("no command given")
	}
	cmd, ok := r.Lookup(args[0])
	if !ok {
		return nil, nil, NewUsageError("unknown command %q", args[0])
	}
	rest := args[1:]
	for len(cmd.children) > 0 {
		if len(rest) == 0 {
			return nil, nil, NewUsageError("%s requires a subcommand (one of: %s)", cmd.name, joinNames(cmd.children))
		}
		child, ok := cmd.lookupChild(rest[0])
		if !ok {
			return nil, nil, NewUsageError("unknown %s subcommand %q (one of: %s)", cmd.name, rest[0], joinNames(cmd.children))
		}
		cmd = child
		rest = rest[1:]
	}
	return cmd, rest, nil
}

// Child finds a direct child command by name or alias.
func (s *SubCmd) Child(name string) (*SubCmd, bool) {
	return s.lookupChild(name)
}

// Commands lists the direct children in registration order; a leaf has none.
func (s *SubCmd) Commands() []*SubCmd {
	return append([]*SubCmd(nil), s.children...)
}

// Listed lists the direct children a help listing shows: everything but the
// hidden ones, in registration order.
func (s *SubCmd) Listed() []*SubCmd {
	return listedCommands(s.children)
}

// listedCommands filters out hidden commands, preserving order.
func listedCommands(cmds []*SubCmd) []*SubCmd {
	listed := make([]*SubCmd, 0, len(cmds))
	for _, cmd := range cmds {
		if !cmd.hidden {
			listed = append(listed, cmd)
		}
	}
	return listed
}

// lookupChild finds a direct child by name or alias.
func (s *SubCmd) lookupChild(name string) (*SubCmd, bool) {
	if child, ok := s.index[name]; ok {
		return child, true
	}
	for _, child := range s.children {
		if child.matchesAlias(name) {
			return child, true
		}
	}
	return nil, false
}

// matchesAlias reports whether name is one of the command's aliases.
func (s *SubCmd) matchesAlias(name string) bool {
	for _, alias := range s.aliases {
		if alias == name {
			return true
		}
	}
	return false
}

func joinNames(cmds []*SubCmd) string {
	names := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		if cmd.hidden {
			continue
		}
		names = append(names, cmd.name)
	}
	return strings.Join(names, ", ")
}
