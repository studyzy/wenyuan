package bankapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// GetBankConfig serves the always-available configuration read route. It
// returns the fully resolved configuration together with the bank-specific
// overrides.
func (h *Handlers) GetBankConfig(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	bank, err := h.Store.GetBank(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	h.writeConfigResponse(w, r, bankID, bank)
}

// configAPIDisabledDetail is the reference's body for a config write while the
// enable flag is off.
const configAPIDisabledDetail = "Bank configuration API is disabled. Set HINDSIGHT_API_ENABLE_BANK_CONFIG_API=true to re-enable."

// UpdateBankConfig serves the configuration write route, gated by the bank
// configuration feature flag. The retired-route messages direct clients here.
func (h *Handlers) UpdateBankConfig(w http.ResponseWriter, r *http.Request) {
	if !h.ConfigEnabled {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, configAPIDisabledDetail))
		return
	}
	// The bank's config is bank-wide, so a tag-scoped caller may not change it.
	if httpx.RefuseBankWideIfScoped(w, r, "change the bank's config") {
		return
	}
	bankID, err := h.Store.ResolveBankID(r.Context(), r.PathValue("bank_id"))
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return
	}
	var req ConfigWriteRequest
	object, err := decodeRequest(r, &req)
	if err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if raw, present := object["updates"]; !present {
		httpx.WriteValidationFailure(w, updatesRequiredFailure(object))
		return
	} else if raw == nil {
		// The reference declares updates a required dict: an explicit null is a
		// dict_type item, not the missing-field one an absent key yields.
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValidationItem("dict_type", []string{"body", "updates"}, "Input should be a valid dictionary", nil, nil),
		}})
		return
	}
	// The override map is taken from the number-preserving decode rather than
	// the typed request: a configuration value keeps the written form of its
	// number (see decodeRawObject), which the patch's type checks read.
	updates, _ := object["updates"].(map[string]any)
	patch, httpErr := configPatch(updates)
	if httpErr != nil {
		httpx.WriteErrorFor(w, r, httpErr)
		return
	}
	// The reference's config write creates the bank when it is absent, so a
	// caller can configure a bank before it holds anything. Validation above
	// ran first, so a rejected update never leaves an empty bank behind, and a
	// name the create path refuses is refused here too rather than reported as
	// a missing bank.
	if err := h.Store.EnsureBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, h.mapWriteError(err))
		return
	}
	bank, err := h.Store.UpdateBank(r.Context(), bankID, patch)
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	h.writeConfigResponse(w, r, bankID, bank)
}

// ResetBankConfig serves the configuration reset route. It removes every
// bank-specific override, leaving the bank on the global configuration, and
// reports the resulting state. It is gated by the same feature flag as the
// write route, because it is a write.
func (h *Handlers) ResetBankConfig(w http.ResponseWriter, r *http.Request) {
	if !h.ConfigEnabled {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, configAPIDisabledDetail))
		return
	}
	if httpx.RefuseBankWideIfScoped(w, r, "reset the bank's config") {
		return
	}
	// The id is resolved for aliases but not required to exist: the reference's
	// reset is a single unconditional update of the bank row's config column,
	// so it answers 200 with the caller's resolved defaults for a bank that is
	// not there, and resolveCanonical's existence check would answer 404
	// instead. ResolveBankID passes an unknown name through unchanged, so an
	// absent bank keeps its own id in the response.
	bankID, err := h.Store.ResolveBankID(r.Context(), r.PathValue("bank_id"))
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return
	}
	// A reset removes every override the bank holds in its config column, so
	// the bank reads the process defaults again. It does not touch the legacy
	// disposition and mission columns: the reference's reset writes only
	// `config = '{}'`, and no route ever writes those two columns.
	bank, err := h.Store.UpdateBank(r.Context(), bankID, storage.BankPatch{
		ClearConfig: true,
	})
	if errors.Is(err, storage.ErrBankNotFound) {
		// A reset is an unconditional clear in the reference: it updates the
		// bank row's config column and reports the resulting state, so a bank
		// that does not exist is a no-op that still answers 200 with the
		// defaults the caller would get from any bank without overrides.
		// Answering 404 instead would make "clear my overrides" fail on a bank
		// that has nothing to clear, and it must not create the row either —
		// the reference's statement updates nothing.
		h.writeConfigResponse(w, r, bankID, storage.Bank{BankID: bankID})
		return
	}
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	h.writeConfigResponse(w, r, bankID, bank)
}

// writeConfigResponse renders the fully resolved configuration and the
// bank-specific overrides for a bank row.
func (h *Handlers) writeConfigResponse(w http.ResponseWriter, r *http.Request, bankID string, bank storage.Bank) {
	overrides := bankOverrides(bank)
	resolved := resolvedConfig(overrides, r, h.ConfigDefaults)
	// The reference validates the resolved configuration on every read
	// (resolve_full_config), and its handler lets a validation fault escape as
	// an internal error rather than answering with a bank that could not retain.
	if detail := validateResolvedConfig(resolved); detail != "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, detail))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, ConfigResponse{
		BankID:    bankID,
		Config:    resolved,
		Overrides: overrides,
	})
}

// validateResolvedConfig reproduces the guard the reference runs on every
// resolved bank configuration: the retain chunking sizes and the per-chunk
// attachment count must be integers of at least one. It returns the reference's
// message for the first offending field, and the empty string when the
// configuration is valid.
//
// A field absent from the resolved map is skipped rather than rejected: the
// process layer always supplies these three, so an absent one means the caller
// resolved no defaults at all (a bare handler), not that the bank stored a null.
func validateResolvedConfig(resolved map[string]any) string {
	if value, ok := resolved["retain_chunk_size"]; ok {
		if detail := validateChunkingInt("retain_chunk_size", value, false); detail != "" {
			return detail
		}
	}
	if value, ok := resolved["retain_structured_chunk_size"]; ok {
		if detail := validateChunkingInt("retain_structured_chunk_size", value, true); detail != "" {
			return detail
		}
	}
	if value, ok := resolved["retain_max_attachments_per_chunk"]; ok {
		if detail := validateChunkingInt("retain_max_attachments_per_chunk", value, false); detail != "" {
			return detail
		}
	}
	return ""
}

// validateChunkingInt checks one size field, returning the reference's message
// (`{name} must be an integer, got {value!r}` / `{name} must be >= 1, got
// {value}`) when it is not an integer of at least one. nullable marks the field
// the reference skips when it is absent rather than rejecting.
func validateChunkingInt(name string, value any, nullable bool) string {
	if value == nil && nullable {
		return ""
	}
	number, ok := asInt(value)
	if !ok {
		return fmt.Sprintf("%s must be an integer, got %s", name, pyRepr(value))
	}
	if number < 1 {
		return fmt.Sprintf("%s must be >= 1, got %d", name, number)
	}
	return ""
}

// updatesRequiredFailure reports the required updates field of the config write
// body, which the reference declares without a default.
func updatesRequiredFailure(input any) *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Missing([]string{"body", "updates"}, input),
	}}
}

// bankOverrides returns the bank's own overrides: exactly the keys stored in
// the bank's config JSONB column, normalized to the internal field form and
// filtered the way the reference's active-override read does. It is the top
// layer of the resolution, so it names only what the bank itself overrides and
// never a shipped default.
//
// The reference reads the config column and nothing else: the legacy
// disposition and mission columns are consulted by the profile and list paths,
// not by the configuration endpoint, so a bank whose columns hold the schema
// default reports no disposition override here. A key the bank stored but the
// reference does not consider configurable is filtered out, and a value whose
// shape does not match the field is dropped (a string field keeps its JSON
// encoding) so a pre-validation row falls back to the server default instead of
// breaking the read.
func bankOverrides(bank storage.Bank) map[string]any {
	overrides := make(map[string]any, len(bank.Config))
	for rawKey, value := range bank.Config {
		key := normalizeConfigKey(rawKey)
		kind, configurable := configurableFields[key]
		if !configurable || value == nil {
			continue
		}
		if kindMatches(kind, value) {
			overrides[key] = value
			continue
		}
		// A string field whose stored value is a container keeps the author's
		// intent as its JSON encoding; anything else is dropped so the bank
		// falls back rather than wedging every later read.
		if kind == kindString {
			if encoded, err := json.Marshal(value); err == nil {
				overrides[key] = string(encoded)
			}
		}
	}
	return overrides
}

// overlayDispositionMission resolves the disposition and mission a bank reports:
// the bank's own resolved configuration when it names a trait or a mission, and
// the bank's stored columns otherwise.
//
// The reference overlays its resolved config on the legacy columns for exactly
// this pair (“_overlay_bank_config_disposition_mission“), and shares the
// helper between the single-bank and list paths so both answer identically.
func overlayDispositionMission(resolved map[string]any, bank storage.Bank) (map[string]int, string) {
	disposition := bank.NormalizedDisposition()
	for field, trait := range dispositionFieldKeys {
		value, ok := resolved[field]
		if !ok || value == nil {
			continue
		}
		if level, ok := intish(value); ok {
			disposition[trait] = level
		}
	}
	mission := bank.Mission
	if value, ok := resolved["reflect_mission"].(string); ok && value != "" {
		mission = value
	}
	return disposition, mission
}

// intish reads a configuration number, which arrives as a JSON number through
// the bank's stored config and as an int through the resolved defaults.
func intish(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	}
	return 0, false
}

// resolvedConfig layers the bank's overrides over the caller's tenant overrides
// and the process defaults, using the same resolver the write paths use. The
// result is the fully resolved configuration, not a fixed set of keys, so every
// field the bank or its tenant overrides appears with its effective value.
func resolvedConfig(overrides map[string]any, r *http.Request, defaults map[string]any) map[string]any {
	return config.ResolveLayers(overrides, httpx.TenantOverridesFrom(r), defaults)
}
