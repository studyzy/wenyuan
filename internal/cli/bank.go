package cli

// The `bank` command family: registration, shared response models and the
// helpers the bank actions reuse. The wiring and actions are split by
// command group across the sibling bank_*.go files.

import (
	"context"
	"encoding/json"
	"flag"
	"net/url"
	"strings"
)

// RegisterBankCommands wires the bank resource family onto the registry.
func RegisterBankCommands(r *Registry) {
	bank := r.Sub("bank", "manage memory banks")
	registerBankCore(bank)
	registerBankInspect(bank)
	registerBankAlias(bank)
	registerBankConfigGroup(bank)
	registerBankOps(bank)
	registerBankTemplate(bank)
}

// dispositionTraits are the three personality axes every bank carries. The
// authoritative storage is bank configuration; the create/update endpoints
// still accept and return the object.
type dispositionTraits struct {
	Skepticism int `json:"skepticism" yaml:"skepticism"`
	Literalism int `json:"literalism" yaml:"literalism"`
	Empathy    int `json:"empathy" yaml:"empathy"`
}

// bankListItem is one entry of the bank listing. The machine output renders
// this type alone — the envelope's total/limit/offset are dropped — so the
// field order here pins the documented key order.
type bankListItem struct {
	BankID         string            `json:"bank_id" yaml:"bank_id"`
	Name           *string           `json:"name" yaml:"name"`
	Disposition    dispositionTraits `json:"disposition" yaml:"disposition"`
	Mission        *string           `json:"mission" yaml:"mission"`
	CreatedAt      string            `json:"created_at" yaml:"created_at"`
	UpdatedAt      string            `json:"updated_at" yaml:"updated_at"`
	FactCount      int               `json:"fact_count" yaml:"fact_count"`
	LastDocumentAt string            `json:"last_document_at" yaml:"last_document_at"`
	LastWriteAt    string            `json:"last_write_at" yaml:"last_write_at"`
	DisplayAlias   *string           `json:"display_alias" yaml:"display_alias"`
	MatchedAliases []string          `json:"matched_aliases" yaml:"matched_aliases"`
}

// bankListEnvelope is the collection wrapper the listing endpoint answers
// with.
type bankListEnvelope struct {
	Banks  []bankListItem `json:"banks"`
	Total  int            `json:"total"`
	Limit  int            `json:"limit"`
	Offset int            `json:"offset"`
}

// bankProfileResponse is the create-or-update answer, decoded for the pretty
// disposition view; machine output passes the raw payload through.
type bankProfileResponse struct {
	BankID      string            `json:"bank_id"`
	Name        *string           `json:"name"`
	Disposition dispositionTraits `json:"disposition"`
	Mission     *string           `json:"mission"`
}

// bankWriteRequest is the create/update body. Nil fields marshal as null;
// the disposition object is only sent when at least one trait flag was
// given, with the missing traits defaulted to 3.
type bankWriteRequest struct {
	Name        *string            `json:"name"`
	Mission     *string            `json:"mission"`
	Background  *string            `json:"background"`
	Disposition *dispositionTraits `json:"disposition,omitempty"`
}

// deleteResponse is the destructive-endpoint body, decoded for the pretty
// branches. success=false with HTTP 200 keeps the exit code 0.
type deleteResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	DeletedCount *int   `json:"deleted_count"`
}

// BankConfigResponse carries the fully resolved configuration and the
// bank-specific overrides, both open objects with snake_case keys.
type BankConfigResponse struct {
	BankID    string         `json:"bank_id" yaml:"bank_id"`
	Config    map[string]any `json:"config" yaml:"config"`
	Overrides map[string]any `json:"overrides" yaml:"overrides"`
}

// dispositionView is the shared machine rendering of a bank's disposition.
type dispositionView struct {
	BankID     string `json:"bank_id" yaml:"bank_id"`
	Name       string `json:"name" yaml:"name"`
	Mission    string `json:"mission" yaml:"mission"`
	Skepticism int    `json:"skepticism" yaml:"skepticism"`
	Literalism int    `json:"literalism" yaml:"literalism"`
	Empathy    int    `json:"empathy" yaml:"empathy"`
}

// traitFlags carries the three disposition flag values; zero means absent.
type traitFlags struct {
	skepticism int
	literalism int
	empathy    int
}

// bankWriteFlags carries the create/update flag variables together with the
// set of names actually given, so absent and empty stay distinguishable.
// The flag targets are read at action time, after parsing.
type bankWriteFlags struct {
	given      map[string]bool
	name       *string
	mission    *string
	skepticism *int
	literalism *int
	empathy    *int
}

func (f bankWriteFlags) has(name string) bool { return f.given[name] }

// anyGiven reports whether at least one writable field flag was given.
func (f bankWriteFlags) anyGiven() bool {
	return f.has("name") || f.has("n") || f.has("mission") || f.has("m") ||
		f.has("skepticism") || f.has("literalism") || f.has("empathy")
}

// anyTraitGiven reports whether a disposition trait flag was given.
func (f bankWriteFlags) anyTraitGiven() bool {
	return f.has("skepticism") || f.has("literalism") || f.has("empathy")
}

// validate enforces the client-side value rules: trait flags accept 1-5
// only, checked before any network traffic.
func (f bankWriteFlags) validate() error {
	for _, t := range []struct {
		flag  string
		value *int
		given string
	}{
		{"--skepticism", f.skepticism, "skepticism"},
		{"--literalism", f.literalism, "literalism"},
		{"--empathy", f.empathy, "empathy"},
	} {
		if f.has(t.given) {
			if err := validateTraitRange(t.flag, *t.value); err != nil {
				return err
			}
		}
	}
	return nil
}

// body builds the create/update request body.
func (f bankWriteFlags) body() *bankWriteRequest {
	req := &bankWriteRequest{}
	if f.has("name") || f.has("n") {
		req.Name = f.name
	}
	if f.has("mission") || f.has("m") {
		req.Mission = f.mission
	}
	if f.anyTraitGiven() {
		req.Disposition = &dispositionTraits{
			Skepticism: defaultedTrait(*f.skepticism),
			Literalism: defaultedTrait(*f.literalism),
			Empathy:    defaultedTrait(*f.empathy),
		}
	}
	return req
}

// defaultedTrait maps an absent trait (0) to the neutral default 3.
func defaultedTrait(v int) int {
	if v == 0 {
		return 3
	}
	return v
}

// validateTraitRange checks an explicitly given 1-5 trait value.
func validateTraitRange(flagName string, value int) error {
	if value < 1 || value > 5 {
		return NewUsageError("%s must be between 1 and 5", flagName)
	}
	return nil
}

// givenFlags returns the names of the flags explicitly provided on the
// command line.
func givenFlags(fs *flag.FlagSet) map[string]bool {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	return given
}

// configInt reads an integer configuration value; JSON numbers arrive as
// float64. Missing or non-numeric values yield the default.
func configInt(m map[string]any, key string, def int) int {
	switch n := m[key].(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	case json.Number:
		if i, err := n.Int64(); err == nil {
			return int(i)
		}
	}
	return def
}

// configString reads a string configuration value; missing or non-string
// values yield "".
func configString(m map[string]any, key string) string {
	if s, ok := m[key].(string); ok {
		return s
	}
	return ""
}

// resolveBankName resolves the display name through the listing endpoint
// with an exact-id filter. Any failure is tolerated: the raw id stands in
// for the name.
func resolveBankName(ctx context.Context, c *Client, bankID string) string {
	var env bankListEnvelope
	path := batchDBase + "?limit=100&q=" + url.QueryEscape(bankID)
	if err := c.Do(ctx, "GET", path, nil, &env); err != nil {
		return bankID
	}
	for _, b := range env.Banks {
		if b.BankID != bankID {
			continue
		}
		if b.Name != nil && *b.Name != "" {
			return *b.Name
		}
		break
	}
	return bankID
}

// renderDispositionView prints the shared pretty disposition layout: the
// section header, the resolved name, the optional mission block and the
// trait bars.
func renderDispositionView(ui *UI, v dispositionView) {
	ui.Section("Disposition: " + v.BankID)
	ui.Plain("Name: %s", v.Name)
	ui.Plain("")
	if v.Mission != "" {
		ui.Plain("Mission:")
		for _, line := range strings.Split(v.Mission, "\n") {
			ui.Plain("%s", line)
		}
		ui.Plain("")
	}
	ui.Plain("%s", ui.rule("Disposition Traits"))
	ui.Plain("")
	renderTrait(ui, "Skepticism", v.Skepticism, "1=trusting, 5=skeptical")
	renderTrait(ui, "Literalism", v.Literalism, "1=flexible, 5=literal")
	renderTrait(ui, "Empathy", v.Empathy, "1=detached, 5=empathetic")
}

// renderTrait prints one padded trait line with its 40-character bar —
// filled = (value-1)*10 — and the description line under it.
func renderTrait(ui *UI, name string, value int, description string) {
	filled := (value - 1) * 10
	if filled < 0 {
		filled = 0
	}
	if filled > 40 {
		filled = 40
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", 40-filled)
	ui.Plain("  %-12s [%s] %d/5", name, bar, value)
	ui.Plain("    %s", description)
}

// profileView converts a profile response into the disposition view; null
// fields fall back to the layout's defaults.
func profileView(p bankProfileResponse) dispositionView {
	v := dispositionView{
		BankID:     p.BankID,
		Skepticism: p.Disposition.Skepticism,
		Literalism: p.Disposition.Literalism,
		Empathy:    p.Disposition.Empathy,
	}
	if p.Name != nil && *p.Name != "" {
		v.Name = *p.Name
	}
	if v.Name == "" {
		v.Name = p.BankID
	}
	if p.Mission != nil {
		v.Mission = *p.Mission
	}
	return v
}
