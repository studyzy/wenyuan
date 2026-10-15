package cli

// The `entity` family: list, get and regenerate.

import (
	"context"
	"encoding/json"
	"fmt"
)

func registerEntityCommands(parent *SubCmd) {
	registerEntityList(parent.Sub("list", "list a bank's entities"))
	registerEntityGet(parent.Sub("get", "show one entity"))
	registerEntityRegenerate(parent.Sub("regenerate", "rebuild an entity's observations"))
}

// entityFields are the fields the list and get renderings share.
type entityFields struct {
	ID            string  `json:"id"`
	CanonicalName string  `json:"canonical_name"`
	MentionCount  int     `json:"mention_count"`
	FirstSeen     *string `json:"first_seen"`
	LastSeen      *string `json:"last_seen"`
}

func registerEntityList(parent *SubCmd) {
	limit := parent.Flags().Int("limit", 100, "page size")
	parent.Flags().IntVar(limit, "l", 100, "page size (short)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("entity list", d.Args, 1); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", bankPath(d.Args[0], "/entities?limit="+fmt.Sprintf("%d", *limit)), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var page struct {
			Items []entityFields `json:"items"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("Entities for Bank: %s", d.Args[0])
		if len(page.Items) == 0 {
			ui.Warningf("No entities found")
			return nil
		}
		ui.Plain("Total entities: %d", len(page.Items))
		for _, entity := range page.Items {
			renderEntityFields(ui, entity)
			ui.Plain("")
		}
		return nil
	})
}

// renderEntityFields draws the shared ID/Name/Mentions/seen lines.
func renderEntityFields(ui *UI, entity entityFields) {
	ui.Plain("ID: %s", entity.ID)
	ui.Plain("  Name: %s", entity.CanonicalName)
	ui.Plain("  Mentions: %d", entity.MentionCount)
	if entity.FirstSeen != nil && *entity.FirstSeen != "" {
		ui.Plain("  First seen: %s", *entity.FirstSeen)
	}
	if entity.LastSeen != nil && *entity.LastSeen != "" {
		ui.Plain("  Last seen: %s", *entity.LastSeen)
	}
}

func registerEntityGet(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("entity get", d.Args, 2); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", bankPath(d.Args[0], "/entities/"+d.Args[1]), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var entity entityFields
		if err := json.Unmarshal(raw, &entity); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("Entity: %s", d.Args[1])
		renderEntityFields(ui, entity)
		ui.Plain("")
		return nil
	})
}

func registerEntityRegenerate(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("entity regenerate", d.Args, 2); err != nil {
			return err
		}
		// A POST with no body: the server rebuilds from stored mentions.
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "POST", bankPath(d.Args[0], "/entities/"+d.Args[1]+"/regenerate"), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var body struct {
			entityFields
			Observations []json.RawMessage `json:"observations"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Infof("Successfully regenerated observations for entity: %s", d.Args[1])
		ui.Plain("")
		ui.Plain("Updated entity:")
		ui.Plain("  Name: %s", body.CanonicalName)
		ui.Plain("  Mentions: %d", body.MentionCount)
		ui.Plain("  Observations: %d", len(body.Observations))
		return nil
	})
}
