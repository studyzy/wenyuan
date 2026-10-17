package bankapi

import (
	"github.com/studyzy/wenyuan/internal/httpx"
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
// alias display information. They carry no background: the reference's
// BankListItem has no such field, unlike the profile response.
type BankListRow struct {
	BankID         string      `json:"bank_id"`
	Name           string      `json:"name"`
	Disposition    Disposition `json:"disposition"`
	Mission        string      `json:"mission"`
	CreatedAt      string      `json:"created_at"`
	UpdatedAt      string      `json:"updated_at"`
	FactCount      int         `json:"fact_count"`
	LastDocumentAt *string     `json:"last_document_at"`
	LastWriteAt    *string     `json:"last_write_at"`
	// DisplayAlias names the promoted alias the bank is presented under, or
	// null when no alias is promoted and the bank is shown under its own id.
	DisplayAlias   *string  `json:"display_alias"`
	MatchedAliases []string `json:"matched_aliases"`
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
// deprecated background alias. The operational fields below are applied through
// the same configuration path the config write route uses.
type BankRequest struct {
	// Name is a pointer so an absent field is distinguishable from an explicit
	// empty string: the reference declares it `str | None`, and a supplied
	// empty string is a value that clears the stored name.
	Name *string `json:"name"`

	Disposition *DispositionInput `json:"disposition"`

	DispositionSkepticism *int `json:"disposition_skepticism"`
	DispositionLiteralism *int `json:"disposition_literalism"`
	DispositionEmpathy    *int `json:"disposition_empathy"`

	Mission        *string `json:"mission"`
	ReflectMission *string `json:"reflect_mission"`
	Background     *string `json:"background"`

	// Operational configuration, applied via the config resolver.
	RetainMission                *string `json:"retain_mission"`
	RetainExtractionMode         *string `json:"retain_extraction_mode"`
	RetainCustomInstructions     *string `json:"retain_custom_instructions"`
	RetainChunkSize              *int    `json:"retain_chunk_size"`
	RetainStructuredChunkSize    *int    `json:"retain_structured_chunk_size"`
	RetainMaxAttachmentsPerChunk *int    `json:"retain_max_attachments_per_chunk"`
	EnableObservations           *bool   `json:"enable_observations"`
	ObservationsMission          *string `json:"observations_mission"`
	EnableTextSearch             *bool   `json:"enable_text_search"`
	EnableTemporalRetrieval      *bool   `json:"enable_temporal_retrieval"`
	EnableGraphRetrieval         *bool   `json:"enable_graph_retrieval"`
	EnableReranking              *bool   `json:"enable_reranking"`
}

// configUpdates returns only the operational configuration fields the request
// explicitly set, in the same map form the config write route accepts. It
// mirrors the reference's get_config_updates for these fields.
func (r BankRequest) configUpdates() map[string]any {
	updates := map[string]any{}
	if r.RetainMission != nil {
		updates["retain_mission"] = *r.RetainMission
	}
	if r.RetainExtractionMode != nil {
		updates["retain_extraction_mode"] = *r.RetainExtractionMode
	}
	if r.RetainCustomInstructions != nil {
		updates["retain_custom_instructions"] = *r.RetainCustomInstructions
	}
	if r.RetainChunkSize != nil {
		updates["retain_chunk_size"] = *r.RetainChunkSize
	}
	if r.RetainStructuredChunkSize != nil {
		updates["retain_structured_chunk_size"] = *r.RetainStructuredChunkSize
	}
	if r.RetainMaxAttachmentsPerChunk != nil {
		updates["retain_max_attachments_per_chunk"] = *r.RetainMaxAttachmentsPerChunk
	}
	if r.EnableObservations != nil {
		updates["enable_observations"] = *r.EnableObservations
	}
	if r.ObservationsMission != nil {
		updates["observations_mission"] = *r.ObservationsMission
	}
	if r.EnableTextSearch != nil {
		updates["enable_text_search"] = *r.EnableTextSearch
	}
	if r.EnableTemporalRetrieval != nil {
		updates["enable_temporal_retrieval"] = *r.EnableTemporalRetrieval
	}
	if r.EnableGraphRetrieval != nil {
		updates["enable_graph_retrieval"] = *r.EnableGraphRetrieval
	}
	if r.EnableReranking != nil {
		updates["enable_reranking"] = *r.EnableReranking
	}
	// The reflect mission and the disposition traits are configuration overrides
	// as well as bank-row values: the reference routes them through
	// get_config_updates, so `GET /config` reports them in `overrides` and the
	// resolved config carries them. Each is resolved the same way the patch
	// resolves it (flat field over the deprecated object member).
	if mission := r.missionValuePtr(); mission != nil {
		updates["reflect_mission"] = *mission
	}
	if value := r.traitPtr(r.DispositionSkepticism, func(d DispositionInput) *int { return d.Skepticism }); value != nil {
		updates["disposition_skepticism"] = *value
	}
	if value := r.traitPtr(r.DispositionLiteralism, func(d DispositionInput) *int { return d.Literalism }); value != nil {
		updates["disposition_literalism"] = *value
	}
	if value := r.traitPtr(r.DispositionEmpathy, func(d DispositionInput) *int { return d.Empathy }); value != nil {
		updates["disposition_empathy"] = *value
	}
	return updates
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
	// Alias is a pointer so an absent field is the framework's missing-field
	// item rather than an empty-string identifier.
	Alias   *string `json:"alias"`
	Primary bool    `json:"primary"`
}

// AliasUpdateRequest is the alias display body. Primary is a pointer so an
// absent field is distinguishable from an explicit false: the reference
// declares it required, so a body that omits it is a missing-field error
// rather than a silent demotion.
type AliasUpdateRequest struct {
	Primary *bool `json:"primary"`
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
	BankID    string         `json:"bank_id"`
	Config    map[string]any `json:"config"`
	Overrides map[string]any `json:"overrides"`
}

// ConfigWriteRequest is the configuration write body carrying the override map.
// Updates is a pointer so an absent field is distinguishable from an explicit
// empty object: the reference declares it required, so a body that omits it is
// a missing-field error.
type ConfigWriteRequest struct {
	Updates *map[string]any `json:"updates"`
}

// ToResponse converts a stored bank into its public profile representation with
// an already-resolved disposition and mission. The reference builds its profile
// response from the read that overlays the bank's configuration on the legacy
// columns, so the caller resolves that pair (overlayDispositionMission) before
// rendering rather than reporting the raw columns.
func ToResponse(bank storage.Bank, disposition map[string]int, mission string) BankResponse {
	return BankResponse{
		BankID:      bank.BankID,
		Name:        bank.Name,
		Disposition: dispositionFromMap(disposition),
		Mission:     mission,
		Background:  mission,
	}
}

// ToListRow converts a stored bank into a list row with the bank's stored
// disposition and mission. A caller that resolved the bank's configuration
// overlays it with ToListRowWithDisposition instead.
func ToListRow(bank storage.Bank, displayAlias *string, matched []string) BankListRow {
	disposition, mission := overlayDispositionMission(nil, bank)
	return ToListRowWithDisposition(bank, displayAlias, matched, disposition, mission)
}

// ToListRowWithDisposition converts a stored bank into a list row with an
// already-resolved disposition and mission.
func ToListRowWithDisposition(bank storage.Bank, displayAlias *string, matched []string, disposition map[string]int, mission string) BankListRow {
	if matched == nil {
		matched = []string{}
	}
	return BankListRow{
		BankID:         bank.BankID,
		Name:           bank.Name,
		Disposition:    dispositionFromMap(disposition),
		Mission:        mission,
		CreatedAt:      httpx.FormatTimestamp(bank.CreatedAt),
		UpdatedAt:      httpx.FormatTimestamp(bank.UpdatedAt),
		FactCount:      bank.FactCount,
		LastDocumentAt: httpx.FormatTimestampPtr(bank.LastDocumentAt),
		LastWriteAt:    httpx.FormatTimestampPtr(bank.LastWriteAt),
		DisplayAlias:   displayAlias,
		MatchedAliases: matched,
	}
}

// dispositionFromMap renders a trait map as the public disposition.
func dispositionFromMap(disposition map[string]int) Disposition {
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
//
// Only the disposition traits the request actually named are stored. A trait
// the caller left alone is absent rather than written as the neutral value, so
// the bank's own overrides name exactly what it set — which is what the
// configuration read and the template export report.
func (r BankRequest) ToStorage(id string) storage.Bank {
	disposition := map[string]int{}
	if value := r.traitPtr(r.DispositionSkepticism, func(d DispositionInput) *int { return d.Skepticism }); value != nil {
		disposition[storage.DispositionKeySkepticism] = *value
	}
	if value := r.traitPtr(r.DispositionLiteralism, func(d DispositionInput) *int { return d.Literalism }); value != nil {
		disposition[storage.DispositionKeyLiteralism] = *value
	}
	if value := r.traitPtr(r.DispositionEmpathy, func(d DispositionInput) *int { return d.Empathy }); value != nil {
		disposition[storage.DispositionKeyEmpathy] = *value
	}
	name := id
	if r.Name != nil && *r.Name != "" {
		name = *r.Name
	}
	return storage.Bank{
		BankID:      id,
		Name:        name,
		Disposition: disposition,
		Mission:     r.missionValue(),
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
	if r.Name != nil {
		patch.Name = r.Name
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

// traitPtr resolves a trait with the explicit individual field taking
// precedence over the deprecated disposition object member, returning nil when
// neither named it.
//
// A member the object did not carry is not a value: it stays nil rather than
// falling through to a default, so the caller can tell "left alone" from
// "explicitly set", which is what keeps a partial write partial.
func (r BankRequest) traitPtr(explicit *int, fromObject func(DispositionInput) *int) *int {
	if explicit != nil {
		return explicit
	}
	if r.Disposition != nil {
		return fromObject(*r.Disposition)
	}
	return nil
}
