// Package explore implements the `wenyuan-admin explore` command: an interactive
// full-screen explorer over the memory HTTP API, rendered with bubbletea.
// Five views (banks, memories, entities, documents, query) share one model,
// a 100 ms frame tick, a 5 s auto-refresh timer, and a view-history stack;
// only query execution runs asynchronously, keyed by a monotonically
// increasing query id so a new submit supersedes an in-flight one.
package explore

import (
	"context"
	"fmt"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/studyzy/wenyuan/internal/cli"
)

// Register wires the explore command into the registry. The command takes
// no arguments: the API base URL and optional bearer key come from the
// shared configuration. `tui` is an accepted spelling of the command.
func Register(r *cli.Registry) {
	r.Sub("explore", "open the interactive explorer").Alias("tui").Action(run)
}

// run builds the data layer over the shared client, performs the first bank
// load synchronously before the first frame (failure leaves the app open on
// an empty Banks list), and enters the alternate screen. The command always
// exits 0; an internal failure prints a debug line after the terminal is
// restored.
func run(ctx context.Context, d *cli.Deps) error {
	m := newModel(apiClient{c: d.Client}, ctx)
	m.loadBanks(false)
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithMouseCellMotion())
	if _, err := p.Run(); err != nil {
		_, _ = fmt.Fprintf(d.UI.Stdout, "Error: %v\n", err)
	}
	return nil
}
