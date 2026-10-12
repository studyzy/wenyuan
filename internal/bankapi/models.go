package bankapi

import (
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// Disposition is the bank's three bounded integer traits, carried as an object
// in the profile response and accepted as a deprecated request object.
type Disposition struct {
	Skepticism int `json:"skepticism"`
	Literalism int `json:"literalism"`
	Empathy    int `json:"empathy"`
}

// BankResponse is the public bank profile representation. Every field name
// here is part of the compatibility surface. The mission is echoed under the
// deprecated background field for backward compatibility.
type BankResponse struct {
	BankID      string      `json:"bank_id"`
	Name        string      `json:"name"`
	Disposition Disposition `json:"disposition"`
	Mission     string      `json:"mission"`
	Background  string      `json:"background"`
}

// BankListRow is one bank row in the collection envelope. List rows add the
// fact count, the last-document-ingestion time, the last-write time and the
// alias display information.
type BankListRow struct {
	BankID         string      `json:"bank_id"`
	Name           string      `json:"name"`
	Disposition    Disposition `json:"disposition"`
	Mission        string      `json:"mission"`
	Background     string      `json:"background"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
	FactCount      int         `json:"fact_count"`
	LastDocumentAt *time.Time  `json:"last_document_at"`
	LastWriteAt    *time.Time  `json:"last_write_at"`
	DisplayAlias   string      `json:"display_alias"`
	MatchedAliases []string    `json:"matched_aliases"`
}

// BankListResponse is the collection envelope for the bank listing route.
type BankListResponse struct {
	Banks  []BankListRow `json:"banks"`
	Total  int           `json:"total"`
	Limit  int           `json:"limit"`
	Offset int           `json:"offset"`
}

// BankRequest is the create-or-replace and partial-update body. The individual
// disposition_* fields take precedence over the deprecated disposition object;
// reflect_mission takes precedence over mission which takes precedence over the
// deprecated background alias.
type BankRequest struct {
	Name string `json:"name"`

	Disposition *Disposition `json:"disposition"`

	DispositionSkepticism *int `json:"disposition_skepticism"`
	DispositionLiteralism *int `json:"disposition_literalism"`
	DispositionEmpathy    *int `json:"disposition_empathy"`

	Mission        *string `json:"mission"`
	ReflectMission *string `json:"reflect_mission"`
	Background     *string `json:"background"`
}

// AliasEntry is one alias in an alias response.
type AliasEntry struct {
	Alias   string `json:"alias"`
	Primary bool   `json:"primary"`
}

// AliasResponse carries a bank's own identifier and its full alias list.
type AliasResponse struct {
	BankID  string       `json:"bank_id"`
	Aliases []AliasEntry `json:"aliases"`
}

// AliasRequest is the alias creation body: the extra identifier and an optional
// flag to also display the bank under it.
type AliasRequest struct {
	Alias   string `json:"alias"`
	Primary bool   `json:"primary"`
}

// AliasUpdateRequest is the alias display body.
type AliasUpdateRequest struct {
	Primary bool `json:"primary"`
}

// DeleteResponse is the bank deletion envelope.
type DeleteResponse struct {
	Success      bool   `json:"success"`
	Message      string `json:"message"`
	DeletedCount int    `json:"deleted_count"`
}

// ConfigResponse carries the fully resolved configuration and the bank-specific
// overrides.
type ConfigResponse struct {
	Config    map[string]any `json:"config"`
	Overrides map[string]any `json:"overrides"`
}

// ConfigWriteRequest is the configuration write body carrying the override map.
type ConfigWriteRequest struct {
	Updates map[string]any `json:"updates"`
}

// ToResponse converts a stored bank into its public profile representation.
func ToResponse(bank storage.Bank) BankResponse {
	return BankResponse{
		BankID: bank.ID,
		Name:   bank.Name,
		Disposition: Disposition{
			Skepticism: bank.DispositionSkepticism,
			Literalism: bank.DispositionLiteralism,
			Empathy:    bank.DispositionEmpathy,
		},
		Mission:    bank.ReflectMission,
		Background: bank.ReflectMission,
	}
}

// ToListRow converts a stored bank into a list row, attaching the alias display
// information resolved by the handler.
func ToListRow(bank storage.Bank, displayAlias string, matched []string) BankListRow {
	if matched == nil {
		matched = []string{}
	}
	return BankListRow{
		BankID:         bank.ID,
		Name:           bank.Name,
		Disposition:    dispositionOf(bank),
		Mission:        bank.ReflectMission,
		Background:     bank.ReflectMission,
		CreatedAt:      bank.CreatedAt,
		UpdatedAt:      bank.UpdatedAt,
		FactCount:      bank.FactCount,
		LastDocumentAt: bank.LastDocumentAt,
		LastWriteAt:    bank.LastWriteAt,
		DisplayAlias:   displayAlias,
		MatchedAliases: matched,
	}
}

func dispositionOf(bank storage.Bank) Disposition {
	return Disposition{
		Skepticism: bank.DispositionSkepticism,
		Literalism: bank.DispositionLiteralism,
		Empathy:    bank.DispositionEmpathy,
	}
}

// ToAliasResponse converts stored aliases into the public alias response.
func ToAliasResponse(bankID string, aliases []storage.BankAlias) AliasResponse {
	entries := make([]AliasEntry, 0, len(aliases))
	for _, alias := range aliases {
		entries = append(entries, AliasEntry{Alias: alias.Alias, Primary: alias.Primary})
	}
	return AliasResponse{BankID: bankID, Aliases: entries}
}

// ToStorage converts a create-or-replace request into a stored bank with the
// documented precedence and neutral defaults.
func (r BankRequest) ToStorage(id string) storage.Bank {
	return storage.Bank{
		ID:                    id,
		Name:                  r.Name,
		DispositionSkepticism: r.trait(r.DispositionSkepticism, func(d Disposition) int { return d.Skepticism }, storage.DispositionTraitNeutral),
		DispositionLiteralism: r.trait(r.DispositionLiteralism, func(d Disposition) int { return d.Literalism }, storage.DispositionTraitNeutral),
		DispositionEmpathy:    r.trait(r.DispositionEmpathy, func(d Disposition) int { return d.Empathy }, storage.DispositionTraitNeutral),
		ReflectMission:        r.missionValue(),
	}
}

// ToPatch converts a partial-update request into a storage patch.
func (r BankRequest) ToPatch() storage.BankPatch {
	patch := storage.BankPatch{
		DispositionSkepticism: r.traitPtr(r.DispositionSkepticism, func(d Disposition) int { return d.Skepticism }),
		DispositionLiteralism: r.traitPtr(r.DispositionLiteralism, func(d Disposition) int { return d.Literalism }),
		DispositionEmpathy:    r.traitPtr(r.DispositionEmpathy, func(d Disposition) int { return d.Empathy }),
	}
	if r.Name != "" {
		name := r.Name
		patch.Name = &name
	}
	if mission := r.missionValuePtr(); mission != nil {
		patch.ReflectMission = mission
	}
	return patch
}

// missionValue resolves the reflect_mission > mission > background precedence.
func (r BankRequest) missionValue() string {
	if value := r.missionValuePtr(); value != nil {
		return *value
	}
	return ""
}

func (r BankRequest) missionValuePtr() *string {
	if r.ReflectMission != nil {
		return r.ReflectMission
	}
	if r.Mission != nil {
		return r.Mission
	}
	return r.Background
}

// trait prefers the explicit individual field, then the deprecated disposition
// object trait, then the supplied default.
func (r BankRequest) trait(explicit *int, fromObject func(Disposition) int, fallback int) int {
	if explicit != nil {
		return *explicit
	}
	if r.Disposition != nil {
		return fromObject(*r.Disposition)
	}
	return fallback
}

func (r BankRequest) traitPtr(explicit *int, fromObject func(Disposition) int) *int {
	if explicit != nil {
		return explicit
	}
	if r.Disposition != nil {
		value := fromObject(*r.Disposition)
		return &value
	}
	return nil
}
