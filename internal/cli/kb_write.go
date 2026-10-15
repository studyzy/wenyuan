package cli

// The write-side knowledge-base actions: create-folder, create-page,
// get-page, update and delete. The create-page trigger composition rule is
// the subtle part: a trigger object is sent only when --mode or
// --fact-types was given, and because a supplied trigger replaces the
// server's page defaults instead of merging, a partial override restates
// the other side.

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/cli/fsengine"
)

// kbFolderRequest is the create-folder body; parent_id is omitted when the
// flag is absent.
type kbFolderRequest struct {
	Name     string  `json:"name"`
	ParentID *string `json:"parent_id,omitempty"`
}

func registerKBCreateFolder(kb *SubCmd) {
	parentID := kb.Flags().String("parent-id", "", "parent folder id")
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base create-folder", d.Args, 2); err != nil {
			return err
		}
		body := kbFolderRequest{Name: d.Args[1]}
		if *parentID != "" {
			body.ParentID = parentID
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "POST", kbBase(d.Args[0])+"/folders", body, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var node kbNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		d.UI.Infof("Folder created: %s", node.ID)
		return nil
	})
}

// kbTrigger is the create-page trigger object. Its presence replaces the
// server's page defaults wholesale, so mode and fact_types always travel
// together with the two fixed booleans.
type kbTrigger struct {
	Mode                      string   `json:"mode"`
	FactTypes                 []string `json:"fact_types"`
	RefreshAfterConsolidation bool     `json:"refresh_after_consolidation"`
	ExcludeMentalModels       bool     `json:"exclude_mental_models"`
}

// kbPageRequest is the create-page body; absent flags are omitted.
type kbPageRequest struct {
	Name        string     `json:"name"`
	SourceQuery string     `json:"source_query"`
	ParentID    *string    `json:"parent_id,omitempty"`
	Tags        []string   `json:"tags,omitempty"`
	MaxTokens   *int       `json:"max_tokens,omitempty"`
	Trigger     *kbTrigger `json:"trigger,omitempty"`
}

func registerKBCreatePage(kb *SubCmd) {
	parentID := kb.Flags().String("parent-id", "", "parent folder id")
	tags := kb.Flags().String("tags", "", "comma-separated tags")
	maxTokens := kb.Flags().Int("max-tokens", 0, "maximum generated tokens")
	mode := kb.Flags().String("mode", "", "generation mode: full or delta")
	factTypes := kb.Flags().String("fact-types", "", "comma-separated fact types: world, experience, observation")
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base create-page", d.Args, 3); err != nil {
			return err
		}
		body := kbPageRequest{Name: d.Args[1], SourceQuery: d.Args[2]}
		if *parentID != "" {
			body.ParentID = parentID
		}
		if *tags != "" {
			body.Tags = commaList(*tags)
		}
		if flagWasProvided(d.Flags, "max-tokens") {
			body.MaxTokens = maxTokens
		}
		modeGiven := flagWasProvided(d.Flags, "mode")
		factTypesGiven := flagWasProvided(d.Flags, "fact-types")
		if modeGiven || factTypesGiven {
			m := "delta"
			if modeGiven {
				m = strings.ToLower(*mode)
				switch m {
				case "full", "delta":
				default:
					return NewCommandError("--mode must be one of full, delta")
				}
			}
			types := []string{"observation"}
			if factTypesGiven {
				types = nil
				for _, t := range commaList(*factTypes) {
					lower := strings.ToLower(t)
					switch lower {
					case "world", "experience", "observation":
						types = append(types, lower)
					default:
						return NewCommandError("--fact-types must be one of world, experience, observation")
					}
				}
			}
			body.Trigger = &kbTrigger{
				Mode:                      m,
				FactTypes:                 types,
				RefreshAfterConsolidation: true,
				ExcludeMentalModels:       true,
			}
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "POST", kbBase(d.Args[0])+"/pages", body, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var page struct {
			PageID        string  `json:"page_id"`
			MentalModelID *string `json:"mental_model_id"`
			OperationID   *string `json:"operation_id"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Infof("Page created: %s", page.PageID)
		if page.OperationID != nil && *page.OperationID != "" {
			ui.Plain("  Operation: %s", *page.OperationID)
			ui.Plain("")
			// The hint names this CLI, so the upstream wording is adapted
			// to the wenyuan spelling.
			ui.Plain("Content is generated in the background. Use 'wenyuan-admin operation get' to track it.")
		}
		return nil
	})
}

func registerKBGetPage(kb *SubCmd) {
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base get-page", d.Args, 2); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", kbBase(d.Args[0])+"/pages/"+fsengineEscape(d.Args[1]), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var page struct {
			ID          string   `json:"id"`
			Name        string   `json:"name"`
			Type        string   `json:"type"`
			Description *string  `json:"description"`
			Timestamp   *string  `json:"timestamp"`
			Tags        []string `json:"tags"`
			Body        *string  `json:"body"`
		}
		if err := json.Unmarshal(raw, &page); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Plain("%s", page.Name)
		ui.Plain("  ID: %s", page.ID)
		ui.Plain("  Type: %s", page.Type)
		if page.Description != nil {
			ui.Plain("  Question: %s", *page.Description)
		}
		if page.Timestamp != nil {
			ui.Plain("  Last refresh: %s", *page.Timestamp)
		}
		if len(page.Tags) > 0 {
			ui.Plain("  Tags: %s", strings.Join(page.Tags, ", "))
		}
		ui.Plain("")
		if page.Body != nil {
			ui.Plain("%s", *page.Body)
		}
		ui.Plain("")
		return nil
	})
}

// kbUpdateRequest is the update body: only the supplied flags travel, and
// the trigger is deliberately never sent.
type kbUpdateRequest struct {
	Name        *string  `json:"name,omitempty"`
	ParentID    *string  `json:"parent_id,omitempty"`
	SourceQuery *string  `json:"source_query,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	MaxTokens   *int     `json:"max_tokens,omitempty"`
}

func registerKBUpdate(kb *SubCmd) {
	name := kb.Flags().String("name", "", "new node name")
	parentID := kb.Flags().String("parent-id", "", "new parent folder id")
	sourceQuery := kb.Flags().String("source-query", "", "new page source query")
	tags := kb.Flags().String("tags", "", "comma-separated tags")
	maxTokens := kb.Flags().Int("max-tokens", 0, "maximum generated tokens")
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base update", d.Args, 2); err != nil {
			return err
		}
		provided := flagWasProvided(d.Flags, "name") || flagWasProvided(d.Flags, "parent-id") ||
			flagWasProvided(d.Flags, "source-query") || flagWasProvided(d.Flags, "tags") ||
			flagWasProvided(d.Flags, "max-tokens")
		if !provided {
			return NewCommandError("knowledge-base update requires at least one of --name, --parent-id, --source-query, --tags, or --max-tokens")
		}
		body := kbUpdateRequest{}
		if flagWasProvided(d.Flags, "name") {
			body.Name = name
		}
		if flagWasProvided(d.Flags, "parent-id") {
			body.ParentID = parentID
		}
		if flagWasProvided(d.Flags, "source-query") {
			body.SourceQuery = sourceQuery
		}
		if flagWasProvided(d.Flags, "tags") {
			body.Tags = commaList(*tags)
		}
		if flagWasProvided(d.Flags, "max-tokens") {
			body.MaxTokens = maxTokens
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "PATCH", kbBase(d.Args[0])+"/nodes/"+fsengineEscape(d.Args[1]), body, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		var node kbNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		ui := d.UI
		ui.Infof("Node '%s' updated successfully", node.ID)
		kbRenderNodes(ui, []kbNode{node}, 1)
		return nil
	})
}

func registerKBDelete(kb *SubCmd) {
	yes := kb.Flags().Bool("yes", false, "skip the confirmation prompt")
	kb.Flags().BoolVar(yes, "y", false, "skip the confirmation prompt (short)")
	kb.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("knowledge-base delete", d.Args, 2); err != nil {
			return err
		}
		bankID, nodeID := d.Args[0], d.Args[1]
		if !d.Format.IsMachine() && !*yes {
			prompt := fmt.Sprintf("Are you sure you want to delete '%s' and everything under it? This cannot be undone.", nodeID)
			if !confirm(d.UI, d.Stdin, prompt) {
				d.UI.Infof("Operation cancelled")
				return nil
			}
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "DELETE", kbBase(bankID)+"/nodes/"+fsengineEscape(nodeID), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			// The CLI prints its own literal instead of the server body.
			_, _ = fmt.Fprintln(d.UI.Stdout, `{"success": true}`)
			return nil
		}
		d.UI.Infof("Node '%s' deleted successfully", nodeID)
		return nil
	})
}

// fsengineEscape percent-encodes one path segment for the request line.
func fsengineEscape(s string) string {
	return fsengine.EncodePathSegment(s)
}
