package cli

// The `tag` family: the bank's tag inventory with use counts.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

func registerTagCommands(parent *SubCmd) {
	registerTagList(parent.Sub("list", "list a bank's tags"))
}

func registerTagList(parent *SubCmd) {
	f := parent.Flags()
	query := f.String("query", "", "wildcard pattern, e.g. user:*")
	f.StringVar(query, "q", "", "wildcard pattern (short)")
	limit := f.Int("limit", 100, "page size")
	f.IntVar(limit, "l", 100, "page size (short)")
	offset := f.Int("offset", 0, "page offset")
	f.IntVar(offset, "s", 0, "page offset (short)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("tag list", d.Args, 1); err != nil {
			return err
		}
		params := url.Values{}
		if *query != "" {
			params.Set("q", *query)
		}
		params.Set("limit", fmt.Sprintf("%d", *limit))
		params.Set("offset", fmt.Sprintf("%d", *offset))
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", bankPath(d.Args[0], "/tags?"+params.Encode()), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var page struct {
			Items []struct {
				Tag   string `json:"tag"`
				Count int    `json:"count"`
			} `json:"items"`
			Total int `json:"total"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("Tags: %s", d.Args[0])
		if len(page.Items) == 0 {
			ui.Plain("  No tags found.")
			return nil
		}
		for i, item := range page.Items {
			t := 0.0
			if len(page.Items) > 1 {
				t = float64(i) / float64(len(page.Items)-1)
			}
			ui.Plain("  %s %s", ui.gradientAt(item.Tag, t), ui.Dim(fmt.Sprintf("(%d)", item.Count)))
		}
		ui.Plain("")
		ui.Plain("  %s %d total", ui.Dim("Total:"), page.Total)
		return nil
	})
}
