// Command wenyuan is the client CLI for the memory API. It dispatches
// through a Registry of resource families; this batch builds the
// infrastructure and the system commands (configure, profile, health,
// metrics, version), with later batches registering the remaining families.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/studyzy/wenyuan/internal/cli"
	"github.com/studyzy/wenyuan/internal/cli/explore"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr, os.Stdin))
}

// decorationsForOS resolves the pretty-mode decoration policy (help wordmark
// and progress line) for the current run. Tests replace it to drive the
// policy without depending on the harness's terminal.
var decorationsForOS = cli.TTYEnvForOS

// globals holds the flags accepted before or after any subcommand, at any
// depth.
type globals struct {
	output  string
	verbose bool
	profile string
	help    bool
	version bool
}

// parseGlobals extracts the global flags from anywhere in the argument list
// and returns the remaining positional arguments. Values may be attached
// (-o=json) or separate (-o json).
//
// The command path is walked as it is found, because a command may declare
// its own spelling of a global short: `bank export-template` defines -o for
// its output file, and the reference gives the command's own definition
// precedence once that command is entered. So -o after the command path is
// left for the command, while --output stays global everywhere (no command
// declares it).
func parseGlobals(r *cli.Registry, args []string) (globals, []string, error) {
	var g globals
	rest := make([]string, 0, len(args))
	var node *cli.SubCmd
	// shadows reports whether the command currently being entered declares
	// one of the spellings itself, which takes precedence over the global.
	shadows := func(short, long string) bool {
		if node == nil {
			return false
		}
		flags := node.Flags()
		return (short != "" && flags.Lookup(short) != nil) || (long != "" && flags.Lookup(long) != nil)
	}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			if child, ok := childFor(r, node, arg); ok {
				node = child
			}
			rest = append(rest, arg)
			continue
		}
		switch {
		case (arg == "-o" || arg == "--output") && !shadows("o", "output"):
			if i+1 >= len(args) {
				return g, nil, fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			g.output = args[i]
		case hasValueArg(arg, "-o", "--output") && !shadows("o", "output"):
			g.output = valueArg(arg)
		case (arg == "-v" || arg == "--verbose") && !shadows("v", "verbose"):
			g.verbose = true
		case (arg == "-p" || arg == "--profile") && !shadows("p", "profile"):
			if i+1 >= len(args) {
				return g, nil, fmt.Errorf("flag %s needs a value", arg)
			}
			i++
			g.profile = args[i]
		case hasValueArg(arg, "-p", "--profile") && !shadows("p", "profile"):
			g.profile = valueArg(arg)
		case (arg == "-h" || arg == "--help") && !shadows("h", "help"):
			g.help = true
		case (arg == "-V" || arg == "--version") && !shadows("V", "version"):
			g.version = true
		default:
			rest = append(rest, arg)
		}
	}
	return g, rest, nil
}

// childFor finds the next command on the path: a top-level command when no
// command has been entered yet, otherwise a direct child.
func childFor(r *cli.Registry, node *cli.SubCmd, name string) (*cli.SubCmd, bool) {
	if node == nil {
		return r.Lookup(name)
	}
	return node.Child(name)
}

// hasValueArg reports whether arg is an attached-value spelling of the flag,
// like "-o=json" or "--profile=dev".
func hasValueArg(arg, short, long string) bool {
	if s := len(short) + 1; len(arg) > s && arg[:s] == short+"=" {
		return true
	}
	if l := len(long) + 1; len(arg) > l && arg[:l] == long+"=" {
		return true
	}
	return false
}

// valueArg returns the part of arg after the first '='.
func valueArg(arg string) string {
	for i := 0; i < len(arg); i++ {
		if arg[i] == '=' {
			return arg[i+1:]
		}
	}
	return arg
}

// run executes one CLI invocation and returns the process exit code:
// 0 success, 1 runtime errors, 2 argument errors.
func run(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	registry := newRegistry()
	// The command path steers global-flag extraction: a command's own
	// spelling of a global short wins once that command is entered.
	g, rest, err := parseGlobals(registry, args)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 2
	}
	if g.version {
		_, _ = fmt.Fprintf(stdout, "wenyuan-admin %s\n", cli.Version)
		return 0
	}
	format := cli.FormatPretty
	if g.output != "" {
		if format, err = cli.ParseFormat(g.output); err != nil {
			_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
			return 2
		}
	}
	ttyEnv := decorationsForOS(format == cli.FormatPretty)
	decoration := ttyEnv.DecorationsEnabled()

	if len(rest) == 0 {
		if g.help {
			printWordmark(stdout, decoration)
			printUsage(stdout, registry)
			printAfterHelp(stdout)
			return 0
		}
		printUsage(stderr, registry)
		return 2
	}

	if g.help {
		printWordmark(stdout, decoration)
		return printHelp(stdout, stderr, registry, rest)
	}

	cmd, restArgs, err := registry.Resolve(rest)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 2
	}

	positionals, err := cmd.ParseArgs(restArgs)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
		return 2
	}

	deps := &cli.Deps{
		Ctx:     context.Background(),
		Args:    positionals,
		Flags:   cmd.Flags(),
		Stdout:  stdout,
		Stderr:  stderr,
		Stdin:   stdin,
		Format:  format,
		Verbose: g.verbose,
		Profile: g.profile,
		UI:      cli.NewUI(stdout, stderr, cli.ColorEnvForOS()),
	}

	// configure and profile run before any configuration load; everything
	// else resolves the config, then builds the client from it.
	if cmd.NeedsConfig() {
		cfg, err := cli.Load(cli.LoadOptions{Profile: g.profile})
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "error: Configuration error: %s\n", err)
			cli.ConfigHelpBlock(stderr, deps.UI.Color)
			return 1
		}
		deps.Config = cfg
		client := cli.NewClient(cfg.APIURL, cfg.APIKey)
		if g.verbose {
			client.Debug = stderr
		}
		deps.Client = client
	}

	// The progress line covers an API command while it is in flight: pretty
	// mode on a terminal only, on stderr so the result on stdout stays
	// clean. Long-running/TUI commands opt out, and the line is erased
	// before the result prints.
	exec := func() error { return cmd.Execute(deps.Ctx, deps) }
	var execErr error
	if cmd.NeedsConfig() && cmd.UsesSpinner() {
		execErr = cli.WithSpinner(stderr, ttyEnv, "Loading...", exec)
	} else {
		execErr = exec()
	}

	if err := execErr; err != nil {
		var usage *cli.UsageError
		var command *cli.CommandError
		var classified *cli.ClassifiedError
		switch {
		case errors.As(err, &usage):
			_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
			return 2
		case errors.As(err, &command):
			_, _ = fmt.Fprintln(stderr, command.Error())
			return 1
		case errors.As(err, &classified):
			_, _ = fmt.Fprintln(stderr, classified.Error())
			return 1
		case cmd.NeedsConfig():
			// Any failure from a command that talks to the API goes through
			// the classifier; unmatched texts land in the fallback class.
			_, _ = fmt.Fprintln(stderr, cli.Classify(err, deps.Config.APIURL).Error())
			return 1
		default:
			_, _ = fmt.Fprintf(stderr, "error: %s\n", err)
			return 1
		}
	}
	return 0
}

// newRegistry wires every command family onto a fresh registry. It runs
// before the arguments are split so global-flag extraction can see which
// spellings the target command claims for itself.
func newRegistry() *cli.Registry {
	registry := cli.NewRegistry()
	registerSystemCommands(registry)
	cli.RegisterOperationFamily(registry)
	cli.RegisterMentalModelFamily(registry)
	cli.RegisterDirectiveFamily(registry)
	cli.RegisterWebhookFamily(registry)
	cli.RegisterAuditFamily(registry)
	cli.RegisterMemoryCommands(registry)
	cli.RegisterReadCommands(registry)
	cli.RegisterBankCommands(registry)
	cli.RegisterTenantCommands(registry)
	cli.RegisterKBCommands(registry)
	cli.RegisterFSCommands(registry)
	cli.RegisterUICommand(registry)
	explore.Register(registry)
	return registry
}

// registerSystemCommands wires the commands that need no resource family.
// configure and profile run before the configuration loads; health, metrics
// and version go through the API client.
func registerSystemCommands(r *cli.Registry) {
	configure := r.Sub("configure", "set the API URL and key").SkipConfig()
	configureURL := configure.Flags().String("api-url", "", "new API URL")
	configureKey := configure.Flags().String("api-key", "", "new API key (default keeps the stored one)")
	configure.Action(func(ctx context.Context, d *cli.Deps) error {
		return cli.Configure(d, *configureURL, *configureKey)
	})

	profile := r.Sub("profile", "manage named profiles").SkipConfig()

	create := profile.Sub("create", "save a named profile")
	createURL := create.Flags().String("api-url", "", "API URL to store (required)")
	createKey := create.Flags().String("api-key", "", "API key to store")
	create.Action(func(ctx context.Context, d *cli.Deps) error {
		if len(d.Args) != 1 {
			return cli.NewUsageError("profile create requires exactly one profile name")
		}
		if *createURL == "" {
			return cli.NewUsageError("profile create requires --api-url")
		}
		home, err := cli.UserHome()
		if err != nil {
			return err
		}
		return cli.ProfileCreate(d.UI, d.Format, home, d.Args[0], *createURL, *createKey)
	})

	list := profile.Sub("list", "list saved profiles")
	list.Action(func(ctx context.Context, d *cli.Deps) error {
		home, err := cli.UserHome()
		if err != nil {
			return err
		}
		return cli.ProfileList(d.UI, d.Format, home)
	})

	show := profile.Sub("show", "show one profile")
	show.Action(func(ctx context.Context, d *cli.Deps) error {
		if len(d.Args) != 1 {
			return cli.NewUsageError("profile show requires exactly one profile name")
		}
		home, err := cli.UserHome()
		if err != nil {
			return err
		}
		return cli.ProfileShow(d.UI, d.Format, home, d.Args[0])
	})

	del := profile.Sub("delete", "delete a profile")
	delYes := del.Flags().Bool("yes", false, "skip the confirmation prompt")
	del.Flags().BoolVar(delYes, "y", false, "skip the confirmation prompt (short)")
	del.Action(func(ctx context.Context, d *cli.Deps) error {
		if len(d.Args) != 1 {
			return cli.NewUsageError("profile delete requires exactly one profile name")
		}
		home, err := cli.UserHome()
		if err != nil {
			return err
		}
		return cli.ProfileDelete(d.UI, d.Format, home, d.Stdin, d.Args[0], *delYes)
	})

	r.Sub("health", "check API health").Action(func(ctx context.Context, d *cli.Deps) error {
		return cli.Health(ctx, d)
	})
	r.Sub("metrics", "print Prometheus metrics").Action(func(ctx context.Context, d *cli.Deps) error {
		return cli.Metrics(ctx, d)
	})
	r.Sub("version", "print the API version").Action(func(ctx context.Context, d *cli.Deps) error {
		return cli.VersionInfo(ctx, d)
	})
}

// printHelp renders the help for the command path in args. A path that stops
// at a group lists that group's subcommands, matching the reference's help at
// every level; a path that resolves to a leaf prints the command's usage and
// its own flags. An unresolvable path is an argument error.
func printHelp(stdout, stderr io.Writer, registry *cli.Registry, path []string) int {
	var node *cli.SubCmd
	resolved := make([]string, 0, len(path))
	for _, token := range path {
		var child *cli.SubCmd
		var ok bool
		if node == nil {
			child, ok = registry.Lookup(token)
		} else {
			child, ok = node.Child(token)
		}
		if !ok {
			_, _ = fmt.Fprintf(stderr, "error: unknown command %q\n", strings.Join(append(resolved, token), " "))
			return 2
		}
		node = child
		resolved = append(resolved, token)
	}

	if node == nil {
		printUsage(stdout, registry)
		printAfterHelp(stdout)
		return 0
	}

	name := strings.Join(resolved, " ")
	_, _ = fmt.Fprintf(stdout, "Usage: wenyuan-admin %s [OPTIONS]\n\n%s\n", name, node.Summary())
	children := node.Listed()
	if len(children) == 0 {
		if flags := node.Flags(); flags != nil {
			flags.SetOutput(stdout)
			flags.PrintDefaults()
			flags.SetOutput(io.Discard)
		}
		return 0
	}
	_, _ = fmt.Fprint(stdout, "\nSubcommands:\n")
	for _, child := range children {
		_, _ = fmt.Fprintf(stdout, "  %-16s %s\n", child.Name(), child.Summary())
	}
	return 0
}

// printWordmark writes the ASCII wordmark ahead of help output when pretty
// decoration is allowed; machine formats and non-terminals get nothing.
func printWordmark(w io.Writer, decoration bool) {
	if !decoration {
		return
	}
	_, _ = fmt.Fprintln(w, cli.Wordmark)
	_, _ = fmt.Fprintln(w)
}

func printUsage(w io.Writer, r *cli.Registry) {
	_, _ = fmt.Fprint(w, `wenyuan-admin is the command-line client for the wenyuan memory API.

Usage: wenyuan-admin [OPTIONS] <COMMAND>

Commands:
`)
	for _, cmd := range r.Listed() {
		_, _ = fmt.Fprintf(w, "  %-16s %s\n", cmd.Name(), cmd.Summary())
	}
	_, _ = fmt.Fprint(w, `
Options:
  -o, --output <FORMAT>  Output format: pretty, json, or yaml [default: pretty]
  -v, --verbose          Show full requests and responses
  -p, --profile <NAME>   Use a named profile
  -h, --help             Print help
  -V, --version          Print version
`)
}

// printAfterHelp appends the resolved API URL and its source. The load is
// lenient: a failure falls back to the default rather than failing --help.
func printAfterHelp(w io.Writer) {
	cfg, err := cli.Load(cli.LoadOptions{})
	if err != nil {
		cfg = cli.Config{APIURL: cli.DefaultAPIURL, Source: cli.Source{Kind: cli.SourceDefault}}
	}
	_, _ = fmt.Fprintf(w, "\nCurrent API URL: %s (from %s)\n\n", cfg.APIURL, cfg.Source)
	_, _ = fmt.Fprintln(w, "Run 'wenyuan-admin configure' to change the API URL.")
}
