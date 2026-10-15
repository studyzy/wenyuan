package cli

import (
	"context"
	"fmt"
	"strings"
)

// directiveOut is the wire shape of a directive.
type directiveOut struct {
	ID        string   `json:"id" yaml:"id"`
	BankID    string   `json:"bank_id" yaml:"bank_id"`
	Name      string   `json:"name" yaml:"name"`
	Content   string   `json:"content" yaml:"content"`
	Priority  int      `json:"priority" yaml:"priority"`
	IsActive  bool     `json:"is_active" yaml:"is_active"`
	Tags      []string `json:"tags" yaml:"tags"`
	CreatedAt string   `json:"created_at" yaml:"created_at"`
	UpdatedAt string   `json:"updated_at" yaml:"updated_at"`
}

// directiveListOut is the directive listing envelope.
type directiveListOut struct {
	Items  []directiveOut `json:"items" yaml:"items"`
	Total  int            `json:"total" yaml:"total"`
	Limit  int            `json:"limit" yaml:"limit"`
	Offset int            `json:"offset" yaml:"offset"`
}

// directiveCreateBody is the create request body. The CLI always sends
// is_active: true and tags: [].
type directiveCreateBody struct {
	Name     string   `json:"name"`
	Content  string   `json:"content"`
	IsActive bool     `json:"is_active"`
	Priority int      `json:"priority"`
	Tags     []string `json:"tags"`
}

// directiveUpdateBody is the partial-update body; absent fields are omitted.
type directiveUpdateBody struct {
	Name     *string `json:"name,omitempty"`
	Content  *string `json:"content,omitempty"`
	IsActive *bool   `json:"is_active,omitempty"`
	Priority *int    `json:"priority,omitempty"`
}

// RegisterDirectiveFamily wires the directive commands onto the registry.
func RegisterDirectiveFamily(r *Registry) {
	dir := r.Sub("directive", "manage behavioral directives")

	list := dir.Sub("list", "list directives of a bank")
	list.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 1 {
			return NewUsageError("directive list requires exactly one <bank_id>")
		}
		return directiveList(ctx, d, d.Args[0])
	})

	get := dir.Sub("get", "show one directive")
	get.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("directive get requires <bank_id> and <directive_id>")
		}
		return directiveGet(ctx, d, d.Args[0], d.Args[1])
	})

	create := dir.Sub("create", "create a directive")
	priority := create.Flags().Int("priority", 0, "priority (higher first)")
	create.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 3 {
			return NewUsageError("directive create requires <bank_id>, <name> and <content>")
		}
		return directiveCreate(ctx, d, d.Args[0], d.Args[1], d.Args[2], *priority)
	})

	update := dir.Sub("update", "update a directive")
	var uf directiveUpdateFlags
	update.Flags().Var(&uf.name, "name", "new name")
	update.Flags().Var(&uf.content, "content", "new content")
	update.Flags().Var(&uf.isActive, "is-active", "active state (true|false)")
	update.Flags().Var(&uf.priority, "priority", "new priority")
	update.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("directive update requires <bank_id> and <directive_id>")
		}
		return directiveUpdate(ctx, d, d.Args[0], d.Args[1], &uf)
	})

	del := dir.Sub("delete", "delete a directive")
	delYes := del.Flags().Bool("yes", false, "skip the confirmation prompt")
	del.Flags().BoolVar(delYes, "y", false, "skip the confirmation prompt (short)")
	del.Action(func(ctx context.Context, d *Deps) error {
		if len(d.Args) != 2 {
			return NewUsageError("directive delete requires <bank_id> and <directive_id>")
		}
		return directiveDelete(ctx, d, d.Args[0], d.Args[1], *delYes)
	})
}

// directiveUpdateFlags holds the update flag values. Each member is tri-state:
// an absent flag leaves the stored value alone, while an explicit value —
// including "" for a string and 0 for the priority — is sent.
type directiveUpdateFlags struct {
	name     triStateString
	content  triStateString
	isActive triStateBool
	priority triStateInt
}

func (f *directiveUpdateFlags) any() bool {
	return f.name.set || f.content.set || f.isActive.set || f.priority.set
}

// directiveDetail renders the full get-style view of a directive.
func directiveDetail(ui *UI, v directiveOut) {
	ui.Infof("%s", v.Name)
	ui.Plain("  ID: %s", v.ID)
	status := "inactive"
	if v.IsActive {
		status = "active"
	}
	ui.Plain("  Status: %s", status)
	ui.Plain("  Priority: %d", v.Priority)
	if len(v.Tags) > 0 {
		ui.Plain("  Tags: %s", strings.Join(v.Tags, ", "))
	}
	ui.Plain("─── Content ───")
	ui.Plain("%s", v.Content)
}

func directiveList(ctx context.Context, d *Deps, bank string) error {
	var out directiveListOut
	path := fmt.Sprintf("%s/%s/directives", batchDBase, bank)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	ui := d.UI
	ui.Infof("Directives: %s", bank)
	if len(out.Items) == 0 {
		ui.Infof("No directives found.")
		return nil
	}
	for _, v := range out.Items {
		status := "inactive"
		if v.IsActive {
			status = "active"
		}
		ui.Plain("  %s %s [%s]", v.ID, v.Name, status)
		ui.Plain("    %s", Truncate(v.Content, 80))
		ui.Plain("")
	}
	return nil
}

func directiveGet(ctx context.Context, d *Deps, bank, id string) error {
	var out directiveOut
	path := fmt.Sprintf("%s/%s/directives/%s", batchDBase, bank, id)
	if err := d.Client.Do(ctx, "GET", path, nil, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	directiveDetail(d.UI, out)
	return nil
}

func directiveCreate(ctx context.Context, d *Deps, bank, name, content string, priority int) error {
	body := directiveCreateBody{Name: name, Content: content, IsActive: true, Priority: priority, Tags: []string{}}
	var out directiveOut
	path := fmt.Sprintf("%s/%s/directives", batchDBase, bank)
	if err := d.Client.Do(ctx, "POST", path, body, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	d.UI.Infof("Directive '%s' created successfully", out.ID)
	d.UI.Plain("")
	directiveDetail(d.UI, out)
	return nil
}

func directiveUpdate(ctx context.Context, d *Deps, bank, id string, f *directiveUpdateFlags) error {
	if !f.any() {
		return NewCommandError("At least one of --name, --content, --is-active, or --priority must be provided")
	}
	// A plain partial PATCH: every updatable field is optional in the
	// request, so only the passed fields are sent and no read-back happens.
	body := directiveUpdateBody{}
	if f.name.set {
		body.Name = &f.name.value
	}
	if f.content.set {
		body.Content = &f.content.value
	}
	if f.isActive.set {
		v := f.isActive.value
		body.IsActive = &v
	}
	if f.priority.set {
		v := f.priority.value
		body.Priority = &v
	}
	var out directiveOut
	path := fmt.Sprintf("%s/%s/directives/%s", batchDBase, bank, id)
	if err := d.Client.Do(ctx, "PATCH", path, body, &out); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return d.Format.Render(d.UI.Stdout, out)
	}
	d.UI.Infof("Directive '%s' updated successfully", id)
	d.UI.Plain("")
	directiveDetail(d.UI, out)
	return nil
}

func directiveDelete(ctx context.Context, d *Deps, bank, id string, yes bool) error {
	prompt := fmt.Sprintf("Are you sure you want to delete directive '%s'? This cannot be undone.", id)
	proceed, err := confirmDestructiveAction(d, prompt, yes)
	if err != nil || !proceed {
		return err
	}
	if err := d.Client.Do(ctx, "DELETE", fmt.Sprintf("%s/%s/directives/%s", batchDBase, bank, id), nil, nil); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		// The delete acknowledgement is synthesized client-side; the
		// server body is ignored.
		return printSyntheticSuccess(d.UI)
	}
	d.UI.Infof("Directive '%s' deleted successfully", id)
	return nil
}
