package cli

// The `memory` command family: list, get, recall, reflect, retain,
// retain-files, clear, delete, history and clear-observations. Registration
// lives here along with the shared request and rendering helpers the memory
// and read-surface commands use; the heavier actions live in
// memory_recall.go, memory_retain.go and memory_reflect.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"strings"
)

// bankPrefix is the bank-scoped path namespace every memory and read-surface
// command addresses.
const bankPrefix = "/v1/default/banks/"

// bankPath joins a bank id and a route suffix into the request path. Ids are
// not escaped: the reference treats them as path-style values, and document
// ids may legitimately contain slashes.
func bankPath(bankID, suffix string) string {
	return bankPrefix + bankID + suffix
}

// requireArgs enforces an exact positional count with the standard message.
func requireArgs(cmd string, args []string, want int) error {
	if len(args) != want {
		return NewUsageError("%s requires exactly %d argument%s", cmd, want, pluralS(want))
	}
	return nil
}

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// renderRaw writes the server's response in the machine formats. JSON mode
// re-indents the payload as received, so the server's key order survives;
// YAML decodes and re-emits.
func renderRaw(d *Deps, data json.RawMessage) error {
	if d.Format == FormatJSON {
		var out bytes.Buffer
		if err := json.Indent(&out, data, "", "  "); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(data)}
		}
		out.WriteByte('\n')
		_, err := io.WriteString(d.UI.Stdout, out.String())
		return err
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(data)}
	}
	return d.Format.Render(d.UI.Stdout, v)
}

// gradientAt renders s in the brand gradient color at relative position t
// (0.0 through 1.0), for badges whose position encodes the fact type.
func (u *UI) gradientAt(s string, t float64) string {
	if !u.Color || s == "" {
		return s
	}
	start, end := [3]int{0, 116, 217}, [3]int{0, 146, 150}
	rgb := [3]int{}
	for i := range rgb {
		rgb[i] = start[i] + int(t*float64(end[i]-start[i])+0.5)
	}
	return u.paint(s, fmt.Sprintf("%d;%d;%d", rgb[0], rgb[1], rgb[2]))
}

// rule renders a `─── Title ───` section rule.
func (u *UI) rule(title string) string {
	return u.gradient("─── " + title + " ───")
}

// factTypeGradient maps a fact type to its badge gradient position.
func factTypeGradient(factType string) float64 {
	switch factType {
	case "world":
		return 0.0
	case "experience":
		return 0.5
	case "opinion":
		return 1.0
	case "observation":
		return 0.25
	default:
		return 0.5
	}
}

// typeBadge renders the `[<TYPE>]` gradient badge.
func (u *UI) typeBadge(factType string) string {
	return u.gradientAt("["+strings.ToUpper(factType)+"]", factTypeGradient(factType))
}

// commaList splits a comma-separated flag value. Empty entries are dropped,
// so "" yields no items at all.
func commaList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// confirm prints the prompt and reads the answer, accepting y/yes
// case-insensitively. Anything else, including an empty answer or EOF, is a
// no.
func confirm(ui *UI, stdin io.Reader, prompt string) bool {
	_, _ = fmt.Fprintf(ui.Stdout, "%s [y/N]: ", prompt)
	answer := strings.ToLower(readLine(stdin))
	return answer == "y" || answer == "yes"
}

// RegisterMemoryCommands wires the memory command family.
func RegisterMemoryCommands(r *Registry) {
	memory := r.Sub("memory", "write, search and manage memories")
	registerMemoryList(memory.Sub("list", "list the memories of a bank"))
	registerMemoryGet(memory.Sub("get", "show one memory"))
	registerMemoryRecall(memory.Sub("recall", "search a bank's memories"))
	registerMemoryReflect(memory.Sub("reflect", "answer a question from a bank's memories"))
	registerMemoryRetain(memory.Sub("retain", "store text as memories"))
	registerMemoryRetainFiles(memory.Sub("retain-files", "import files into a bank"))
	registerMemoryClear(memory.Sub("clear", "clear a bank's memories"))
	registerMemoryDelete(memory.Sub("delete", "delete one memory"))
	registerMemoryHistory(memory.Sub("history", "show a memory's history"))
	registerMemoryClearObservations(memory.Sub("clear-observations", "clear a memory's observations"))
}

func registerMemoryList(parent *SubCmd) {
	factType := parent.Flags().String("fact-type", "", "filter by fact type")
	parent.Flags().StringVar(factType, "t", "", "filter by fact type (short)")
	query := parent.Flags().String("query", "", "full-text filter")
	parent.Flags().StringVar(query, "q", "", "full-text filter (short)")
	limit := parent.Flags().Int("limit", 100, "page size")
	parent.Flags().IntVar(limit, "l", 100, "page size (short)")
	offset := parent.Flags().Int("offset", 0, "page offset")
	parent.Flags().IntVar(offset, "s", 0, "page offset (short)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("memory list", d.Args, 1); err != nil {
			return err
		}
		return memoryList(ctx, d, d.Args[0], *factType, *query, *limit, *offset)
	})
}

// memoryList renders one page of GET .../memories/list. The list item's type
// field is fact_type, unlike the detail endpoint's type.
func memoryList(ctx context.Context, d *Deps, bankID, factType, query string, limit, offset int) error {
	params := url.Values{}
	if factType != "" {
		params.Set("type", factType)
	}
	if query != "" {
		params.Set("q", query)
	}
	params.Set("limit", fmt.Sprintf("%d", limit))
	params.Set("offset", fmt.Sprintf("%d", offset))
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/memories/list?"+params.Encode()), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var page struct {
		Items []struct {
			ID         string  `json:"id"`
			Text       string  `json:"text"`
			FactType   *string `json:"fact_type"`
			DocumentID *string `json:"document_id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Plain("Memories: %s (showing %d-%d)", bankID, offset+1, offset+len(page.Items))
	if len(page.Items) == 0 {
		ui.Plain("  %s", ui.Dim("No memories found."))
		return nil
	}
	for _, item := range page.Items {
		ft := ""
		if item.FactType != nil {
			ft = *item.FactType
		}
		line := ui.typeBadge(ft) + " " + ui.Dim(item.ID)
		ui.Plain("%s", line)
		if item.Text != "" {
			ui.Plain("%s", Truncate(item.Text, 100))
		}
		if item.DocumentID != nil && *item.DocumentID != "" {
			ui.Plain("  %s", ui.Dim("doc: "+*item.DocumentID))
		}
		ui.Plain("")
	}
	ui.Plain("  %s %d total", ui.Dim("Total:"), pageTotal(raw))
	return nil
}

// pageTotal reads the envelope total without a second decode pass.
func pageTotal(raw json.RawMessage) int {
	var env struct {
		Total int `json:"total"`
	}
	_ = json.Unmarshal(raw, &env)
	return env.Total
}

func registerMemoryGet(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("memory get", d.Args, 2); err != nil {
			return err
		}
		return memoryGet(ctx, d, d.Args[0], d.Args[1])
	})
}

// memoryGet renders GET .../memories/{id}. The detail's type field is `type`,
// not the list endpoint's fact_type, and entities are plain names.
func memoryGet(ctx context.Context, d *Deps, bankID, memoryID string) error {
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "GET", bankPath(bankID, "/memories/"+memoryID), nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var body struct {
		ID            string   `json:"id"`
		Text          string   `json:"text"`
		Context       *string  `json:"context"`
		Type          *string  `json:"type"`
		OccurredStart *string  `json:"occurred_start"`
		OccurredEnd   *string  `json:"occurred_end"`
		Entities      []string `json:"entities"`
		DocumentID    *string  `json:"document_id"`
		Tags          []string `json:"tags"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	ui := d.UI
	ui.Plain("Memory: %s", memoryID)
	factType := ""
	if body.Type != nil {
		factType = *body.Type
	}
	ui.Plain("  Type: %s", ui.typeBadge(factType))
	ui.Plain("  ID: %s", body.ID)
	if body.DocumentID != nil && *body.DocumentID != "" {
		ui.Plain("  Document: %s", *body.DocumentID)
	}
	if body.Context != nil && *body.Context != "" {
		ui.Plain("  Context: %s", *body.Context)
	}
	ui.Plain("")
	ui.Plain("%s", ui.rule("Content"))
	ui.Plain("")
	ui.Plain("%s", body.Text)
	if body.OccurredStart != nil || body.OccurredEnd != nil {
		ui.Plain("")
		ui.Plain("%s", ui.rule("Temporal"))
		if body.OccurredStart != nil {
			ui.Plain("  Start: %s", *body.OccurredStart)
		}
		if body.OccurredEnd != nil {
			ui.Plain("  End: %s", *body.OccurredEnd)
		}
	}
	if len(body.Entities) > 0 {
		ui.Plain("")
		ui.Plain("%s", ui.rule("Entities"))
		for _, name := range body.Entities {
			ui.Plain("  • %s", name)
		}
	}
	if len(body.Tags) > 0 {
		ui.Plain("")
		ui.Plain("%s", ui.rule("Tags"))
		ui.Plain("  %s", strings.Join(body.Tags, ", "))
	}
	ui.Plain("")
	return nil
}

func registerMemoryClear(parent *SubCmd) {
	factType := parent.Flags().String("fact-type", "", "restrict the clear to one fact type")
	parent.Flags().StringVar(factType, "t", "", "restrict the clear to one fact type (short)")
	yes := parent.Flags().Bool("yes", false, "skip the confirmation prompt")
	parent.Flags().BoolVar(yes, "y", false, "skip the confirmation prompt (short)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("memory clear", d.Args, 1); err != nil {
			return err
		}
		ft := ""
		if flagWasProvided(d.Flags, "fact-type") || flagWasProvided(d.Flags, "t") {
			ft = *factType
			switch ft {
			case "world", "experience", "observation":
			default:
				return NewUsageError("--fact-type must be one of world, experience, observation")
			}
		}
		return memoryClear(ctx, d, d.Args[0], ft, *yes)
	})
}

// flagWasProvided reports whether a flag of either spelling was set.
func flagWasProvided(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// flagWasProvidedAny reports whether any of the spellings of one logical flag
// was given; a short and its long form are registered as separate names bound
// to the same variable, so presence must be tested across both.
func flagWasProvidedAny(fs *flag.FlagSet, names ...string) bool {
	for _, name := range names {
		if flagWasProvided(fs, name) {
			return true
		}
	}
	return false
}

// memoryClear sends DELETE .../memories. The confirmation runs only in pretty
// mode without --yes; machine formats send the destructive request directly.
func memoryClear(ctx context.Context, d *Deps, bankID, factType string, yes bool) error {
	ui := d.UI
	if !d.Format.IsMachine() && !yes {
		var prompt string
		if factType != "" {
			prompt = fmt.Sprintf("Are you sure you want to clear all '%s' memories for bank '%s'? This cannot be undone.", factType, bankID)
		} else {
			prompt = fmt.Sprintf("Are you sure you want to clear ALL memories for bank '%s'? This cannot be undone.", bankID)
		}
		if !confirm(ui, d.Stdin, prompt) {
			ui.Infof("Operation cancelled")
			return nil
		}
	}
	path := bankPath(bankID, "/memories")
	if factType != "" {
		path += "?type=" + url.QueryEscape(factType)
	}
	var raw json.RawMessage
	if err := d.Client.Do(ctx, "DELETE", path, nil, &raw); err != nil {
		return err
	}
	if d.Format.IsMachine() {
		return renderRaw(d, raw)
	}
	var body struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return &DecodeError{Reason: err.Error(), Body: string(raw)}
	}
	if !body.Success {
		ui.Errorf("Failed to clear memories")
		return nil
	}
	if factType != "" {
		ui.Infof("Memories cleared successfully")
	} else {
		ui.Infof("All memories cleared successfully")
	}
	return nil
}

// memoryDeleteRefusal is the exact client-side wording; the sentence-final
// period is part of the contract, so it is a variable to keep the literal
// from tripping the error-string lint.
var memoryDeleteRefusal = "Individual memory deletion is no longer supported." +
	" Use 'memory clear' to clear all memories."

func registerMemoryDelete(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("memory delete", d.Args, 2); err != nil {
			return err
		}
		// The deliberate refusal never reaches the network.
		return errors.New(memoryDeleteRefusal)
	})
}

func registerMemoryHistory(parent *SubCmd) {
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("memory history", d.Args, 2); err != nil {
			return err
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "GET", bankPath(d.Args[0], "/memories/"+d.Args[1]+"/history"), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		// The history route answers a bare array; pretty mode just
		// pretty-prints it.
		var out bytes.Buffer
		if err := json.Indent(&out, raw, "", "  "); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		out.WriteByte('\n')
		_, err := io.WriteString(d.UI.Stdout, out.String())
		return err
	})
}

func registerMemoryClearObservations(parent *SubCmd) {
	yes := parent.Flags().Bool("yes", false, "skip the confirmation prompt")
	parent.Flags().BoolVar(yes, "y", false, "skip the confirmation prompt (short)")
	parent.Action(func(ctx context.Context, d *Deps) error {
		if err := requireArgs("memory clear-observations", d.Args, 2); err != nil {
			return err
		}
		bankID, memoryID := d.Args[0], d.Args[1]
		if !d.Format.IsMachine() && !*yes {
			prompt := fmt.Sprintf("Clear observations for memory '%s'? They will be re-derived on next consolidation.", memoryID)
			if !confirm(d.UI, d.Stdin, prompt) {
				d.UI.Infof("Operation cancelled")
				return nil
			}
		}
		var raw json.RawMessage
		if err := d.Client.Do(ctx, "DELETE", bankPath(bankID, "/memories/"+memoryID+"/observations"), nil, &raw); err != nil {
			return err
		}
		if d.Format.IsMachine() {
			return renderRaw(d, raw)
		}
		d.UI.Infof("Cleared observations for memory '%s'", memoryID)
		d.UI.Plain("")
		var out bytes.Buffer
		if err := json.Indent(&out, raw, "", "  "); err != nil {
			return &DecodeError{Reason: err.Error(), Body: string(raw)}
		}
		out.WriteByte('\n')
		_, err := io.WriteString(d.UI.Stdout, out.String())
		return err
	})
}
