package bankadmin

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/studyzy/wenyuan/internal/storage"
)

// RenameFlags is the parsed administrative rename command surface.
type RenameFlags struct {
	Source string
	Target string
	DryRun bool
}

// ParseRename parses a bank rename command. Flags may be supplied as
// --source/--to/--dry-run or with the short aliases -s/-t/-n.
func ParseRename(args []string) (RenameFlags, error) {
	fs := flag.NewFlagSet("bank rename", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags RenameFlags
	fs.StringVar(&flags.Source, "source", "", "source bank identifier")
	fs.StringVar(&flags.Source, "s", "", "source bank identifier (short)")
	fs.StringVar(&flags.Target, "to", "", "target bank identifier")
	fs.StringVar(&flags.Target, "t", "", "target bank identifier (short)")
	fs.BoolVar(&flags.DryRun, "dry-run", false, "report the plan without changing anything")
	fs.BoolVar(&flags.DryRun, "n", false, "report the plan without changing anything (short)")
	if err := fs.Parse(args); err != nil {
		return RenameFlags{}, err
	}
	if flags.Source == "" || flags.Target == "" {
		return RenameFlags{}, fmt.Errorf("both a source and a target identifier are required")
	}
	return flags, nil
}

// ArchiveFlags is the parsed administrative archive command surface.
type ArchiveFlags struct {
	// Action is "export" or "import".
	Action string
	// Archive is the archive file path.
	Archive string
	// TargetID is the target identifier for an import.
	TargetID string
}

// ParseArchive parses a bank archive command.
func ParseArchive(action string, args []string) (ArchiveFlags, error) {
	fs := flag.NewFlagSet("bank archive "+action, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var flags ArchiveFlags
	flags.Action = action
	fs.StringVar(&flags.Archive, "archive", "", "archive file path")
	fs.StringVar(&flags.Archive, "a", "", "archive file path (short)")
	fs.StringVar(&flags.TargetID, "target", "", "target bank identifier for an import")
	fs.StringVar(&flags.TargetID, "t", "", "target bank identifier for an import (short)")
	if err := fs.Parse(args); err != nil {
		return ArchiveFlags{}, err
	}
	if flags.Archive == "" {
		return ArchiveFlags{}, fmt.Errorf("an archive path is required")
	}
	if action == "import" && flags.TargetID == "" {
		return ArchiveFlags{}, fmt.Errorf("a target identifier is required for an import")
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

// RunArchive executes a parsed archive command against a store.
func RunArchive(ctx context.Context, store storage.Store, flags ArchiveFlags, out io.Writer) error {
	exporter := Exporter{Store: store}
	switch flags.Action {
	case "export":
		data, err := exporter.Export(ctx, flags.TargetID)
		if err != nil {
			return err
		}
		if err := os.WriteFile(flags.Archive, data, 0o600); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "exported %d bytes to %s\n", len(data), flags.Archive)
		return nil
	case "import":
		data, err := os.ReadFile(flags.Archive)
		if err != nil {
			return err
		}
		if err := exporter.Import(ctx, data, flags.TargetID); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "imported %s as %q\n", flags.Archive, flags.TargetID)
		return nil
	default:
		return fmt.Errorf("unknown archive action %q", flags.Action)
	}
}
