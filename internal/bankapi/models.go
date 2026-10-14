package bankapi

import (
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// Disposition is the bank's three bounded integer traits as the profile
// response carries them. Every member is populated, because the stored object
// is normalized before it is rendered.
type Disposition struct {
	Skepticism int `json:"skepticism"`
	Literalism int `json:"literalism"`
	Empathy    int `json:"empathy"`
}

// DispositionInput is the deprecated request object. Its members are pointers
// so an absent member stays distinguishable from an explicit value: the
// reference declares all three as required on the object form, and reports a
// missing member as a missing field rather than as an out-of-range zero. A
// plain int would collapse "not sent" into 0 and turn the omission into a
// range error naming the wrong fault.
type DispositionInput struct {
	Skepticism *int `json:"skepticism"`
	Literalism *int `json:"literalism"`
	Empathy    *int `json:"empathy"`
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

	Disposition *DispositionInput `json:"disposition"`

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
		BankID:      bank.BankID,
		Name:        bank.Name,
		Disposition: dispositionOf(bank),
		Mission:     bank.Mission,
		Background:  bank.Mission,
	}
}

// ToListRow converts a stored bank into a list row, attaching the alias display
// information resolved by the handler.
func ToListRow(bank storage.Bank, displayAlias string, matched []string) BankListRow {
	if matched == nil {
		matched = []string{}
	}
	return BankListRow{
		BankID:         bank.BankID,
		Name:           bank.Name,
		Disposition:    dispositionOf(bank),
		Mission:        bank.Mission,
		Background:     bank.Mission,
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
	disposition := bank.NormalizedDisposition()
	return Disposition{
		Skepticism: disposition[storage.DispositionKeySkepticism],
		Literalism: disposition[storage.DispositionKeyLiteralism],
		Empathy:    disposition[storage.DispositionKeyEmpathy],
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
		BankID: id,
		Name:   r.Name,
		Disposition: map[string]int{
			storage.DispositionKeySkepticism: r.trait(r.DispositionSkepticism, func(d DispositionInput) *int { return d.Skepticism }, storage.DispositionTraitNeutral),
			storage.DispositionKeyLiteralism: r.trait(r.DispositionLiteralism, func(d DispositionInput) *int { return d.Literalism }, storage.DispositionTraitNeutral),
			storage.DispositionKeyEmpathy:    r.trait(r.DispositionEmpathy, func(d DispositionInput) *int { return d.Empathy }, storage.DispositionTraitNeutral),
		},
		Mission: r.missionValue(),
	}
}

// ToPatch converts a partial-update request into a storage patch. Only the
// traits the request actually carried are included in the partial map, so a
// patch never resets a trait the caller left alone.
func (r BankRequest) ToPatch() storage.BankPatch {
	patch := storage.BankPatch{}
	traits := map[string]int{}
	if value := r.traitPtr(r.DispositionSkepticism, func(d DispositionInput) *int { return d.Skepticism }); value != nil {
		traits[storage.DispositionKeySkepticism] = *value
	}
	if value := r.traitPtr(r.DispositionLiteralism, func(d DispositionInput) *int { return d.Literalism }); value != nil {
		traits[storage.DispositionKeyLiteralism] = *value
	}
	if value := r.traitPtr(r.DispositionEmpathy, func(d DispositionInput) *int { return d.Empathy }); value != nil {
		traits[storage.DispositionKeyEmpathy] = *value
	}
	if len(traits) > 0 {
		patch.Disposition = &traits
	}
	if r.Name != "" {
		name := r.Name
		patch.Name = &name
	}
	if mission := r.missionValuePtr(); mission != nil {
		patch.Mission = mission
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
// object member, then the supplied default.
//
// A member the object did not carry is not a value: it falls through to the
// default, exactly as the reference does when its individual field is absent.
// Treating the absent member as zero would both produce a range error and
// overwrite a stored trait with a value the caller never sent.
func (r BankRequest) trait(explicit *int, fromObject func(DispositionInput) *int, fallback int) int {
	if value := r.traitPtr(explicit, fromObject); value != nil {
		return *value
	}
	return fallback
}

func (r BankRequest) traitPtr(explicit *int, fromObject func(DispositionInput) *int) *int {
	if explicit != nil {
		return explicit
	}
	if r.Disposition != nil {
		return fromObject(*r.Disposition)
	}
	return nil
}
