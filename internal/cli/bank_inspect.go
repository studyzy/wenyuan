package cli

// Bank read-only views: disposition, stats and graph.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
)

// registerBankInspect wires the read-only bank views.
func registerBankInspect(bank *SubCmd) {
	disp := bank.Sub("disposition", "show a bank's disposition")
	disp.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank disposition", d.Args, 1); err != nil {
			return err
		}
		return bankDisposition(ctx, d, d.Args[0])
	})

	stats := bank.Sub("stats", "show bank statistics")
	stats.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank stats", d.Args, 1); err != nil {
			return err
		}
		return bankStats(ctx, d, d.Args[0])
	})

	graph := bank.Sub("graph", "show the bank memory graph")
	factType := graph.Flags().String("fact-type", "", "filter by fact type: world, experience, observation")
	graph.Flags().StringVar(factType, "t", "", "filter by fact type (short)")
	limit := graph.Flags().Int("limit", 1000, "maximum nodes and edges to fetch")
	graph.Flags().IntVar(limit, "l", 1000, "maximum nodes and edges (short)")
	graph.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("bank graph", d.Args, 1); err != nil {
			return err
		}
		return bankGraph(ctx, d, d.Args[0], *factType, *limit)
	})
}

// bankStatsResponse is the subset of the statistics endpoint the CLI
// surfaces. The payload's remaining fields — operations_by_status,
// last_consolidated_at, last_memory_write_at, pending_consolidation,
// failed_consolidation, total_observations — are dropped from both the
// pretty and the machine output.
type bankStatsResponse struct {
	BankID            string                    `json:"bank_id" yaml:"bank_id"`
	TotalNodes        int                       `json:"total_nodes" yaml:"total_nodes"`
	TotalLinks        int                       `json:"total_links" yaml:"total_links"`
	TotalDocuments    int                       `json:"total_documents" yaml:"total_documents"`
	NodesByFactType   map[string]int            `json:"nodes_by_fact_type" yaml:"nodes_by_fact_type"`
	LinksByLinkType   map[string]int            `json:"links_by_link_type" yaml:"links_by_link_type"`
	LinksByFactType   map[string]int            `json:"links_by_fact_type" yaml:"links_by_fact_type"`
	LinksBreakdown    map[string]map[string]int `json:"links_breakdown" yaml:"links_breakdown"`
	PendingOperations int                       `json:"pending_operations" yaml:"pending_operations"`
	FailedOperations  int                       `json:"failed_operations" yaml:"failed_operations"`
}

// graphDataResponse is the memory-graph payload, decoded for the pretty
// sample; machine output passes the raw payload through. Node and edge data
// are Cytoscape envelopes whose data objects carry camelCase link fields.
type graphDataResponse struct {
	Nodes []graphNodeEnvelope `json:"nodes"`
	Edges []graphEdgeEnvelope `json:"edges"`
}

type graphNodeEnvelope struct {
	Data graphNodeData `json:"data"`
}

type graphNodeData struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Text     string   `json:"text"`
	Date     string   `json:"date"`
	Context  string   `json:"context"`
	Entities []string `json:"entities"`
	Color    string   `json:"color"`
}

type graphEdgeEnvelope struct {
	Data graphEdgeData `json:"data"`
}

type graphEdgeData struct {
	ID         string  `json:"id"`
	Source     string  `json:"source"`
	Target     string  `json:"target"`
	LinkType   string  `json:"linkType"`
	Weight     float64 `json:"weight"`
	EntityName string  `json:"entityName"`
	Color      string  `json:"color"`
	LineStyle  string  `json:"lineStyle"`
}

// readDisposition renders the disposition view from a freshly read config
// plus a display-name lookup. It is shared by `bank disposition` and
// `bank set-disposition`.
func readDisposition(ctx context.Context, d *Deps, bankID string) (dispositionView, error) {
	var cfg BankConfigResponse
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/config"), nil, &cfg); err != nil {
		return dispositionView{}, err
	}
	return dispositionView{
		BankID:     bankID,
		Name:       resolveBankName(ctx, d.Client, bankID),
		Mission:    configString(cfg.Config, "reflect_mission"),
		Skepticism: configInt(cfg.Config, "disposition_skepticism", 3),
		Literalism: configInt(cfg.Config, "disposition_literalism", 3),
		Empathy:    configInt(cfg.Config, "disposition_empathy", 3),
	}, nil
}

// bankDisposition implements `bank disposition`.
func bankDisposition(ctx context.Context, d *Deps, bankID string) error {
	v, err := readDisposition(ctx, d, bankID)
	if err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, v)
	}
	renderDispositionView(d.UI, v)
	return nil
}

// bankStats implements `bank stats` against the cached statistics (the
// refresh query parameter is not sent).
func bankStats(ctx context.Context, d *Deps, bankID string) error {
	var s bankStatsResponse
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/stats"), nil, &s); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, s)
	}
	ui := d.UI
	ui.Section("Statistics: " + bankID)
	ui.Plain("  memory units: %d", s.TotalNodes)
	ui.Plain("  links: %d", s.TotalLinks)
	ui.Plain("  documents: %d", s.TotalDocuments)
	ui.Plain("")
	statsSection(ui, "Memory Units by Type", s.NodesByFactType)
	statsSection(ui, "Links by Type", s.LinksByLinkType)
	statsSection(ui, "Links by Fact Type", s.LinksByFactType)
	if len(s.LinksBreakdown) > 0 {
		ui.Plain("%s", ui.rule("Detailed Link Breakdown"))
		for _, fact := range sortedIntKeys(s.LinksBreakdown) {
			ui.Plain("  %s", fact)
			links := s.LinksBreakdown[fact]
			for _, link := range sortedIntKeys(links) {
				ui.Plain("    %-10s %d", link, links[link])
			}
		}
		ui.Plain("")
	}
	if s.PendingOperations > 0 || s.FailedOperations > 0 {
		ui.Plain("%s", ui.rule("Operations"))
		if s.PendingOperations > 0 {
			ui.Plain("  pending: %d", s.PendingOperations)
		}
		if s.FailedOperations > 0 {
			ui.Plain("  failed: %d", s.FailedOperations)
		}
	}
	return nil
}

// statsSection prints one `─── <title> ───` block with sorted key lines and
// a trailing blank line.
func statsSection(ui *UI, title string, counts map[string]int) {
	ui.Plain("%s", ui.rule(title))
	for _, k := range sortedIntKeys(counts) {
		ui.Plain("  %-10s %d", k, counts[k])
	}
	ui.Plain("")
}

// sortedIntKeys returns the map's keys ascending, for deterministic output.
func sortedIntKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// bankGraph implements `bank graph`.
func bankGraph(ctx context.Context, d *Deps, bankID, factType string, limit int) error {
	path := bankPath(bankID, fmt.Sprintf("/graph?limit=%d", limit))
	if factType != "" {
		path += "&type=" + url.QueryEscape(factType)
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", path, nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var g graphDataResponse
	if err := json.Unmarshal(raw, &g); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Section("Memory Graph: " + bankID)
	ui.Plain("  Nodes: %d", len(g.Nodes))
	ui.Plain("  Edges: %d", len(g.Edges))
	ui.Plain("")
	if len(g.Nodes) > 0 {
		ui.Plain("%s", ui.rule("Sample Nodes"))
		shown := len(g.Nodes)
		if shown > 5 {
			shown = 5
		}
		for _, n := range g.Nodes[:shown] {
			ui.Plain("  %s", n.Data.ID)
			if n.Data.Text != "" {
				ui.Plain("    %s", Truncate(n.Data.Text, 60))
			}
		}
		if len(g.Nodes) > 5 {
			ui.Plain("  + %d more...", len(g.Nodes)-5)
		}
		ui.Plain("")
	}
	ui.Plain("Use JSON output for full graph data: -o json")
	return nil
}
