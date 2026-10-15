package bankapi

import (
	"errors"
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
	bankID, err := h.Store.ResolveBankID(r.Context(), r.PathValue("bank_id"))
	if err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return
	}
	var req ConfigWriteRequest
	if err := httpx.DecodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if req.Updates == nil {
		httpx.WriteValidationFor(w, r, updatesRequiredFailure())
		return
	}
	patch, httpErr := configPatch(*req.Updates)
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
	// A reset removes every override the bank holds — the configuration keys,
	// the disposition traits and the reflect mission alike — so the bank reads
	// the process defaults again. The disposition and mission live on the bank
	// row rather than in the override map, so they are cleared explicitly.
	empty := ""
	bank, err := h.Store.UpdateBank(r.Context(), bankID, storage.BankPatch{
		ClearDisposition: true,
		Mission:          &empty,
		ClearConfig:      true,
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
	httpx.WriteJSONFor(w, r, http.StatusOK, ConfigResponse{
		BankID:    bankID,
		Config:    resolvedConfig(overrides, r, h.ConfigDefaults),
		Overrides: overrides,
	})
}

// updatesRequiredFailure reports the required updates field of the config write
// body, which the reference declares without a default.
func updatesRequiredFailure() *httpx.ValidationFailure {
	return &httpx.ValidationFailure{Errors: []httpx.ValidationError{
		httpx.Missing([]string{"body", "updates"}, map[string]any{}),
	}}
}

// bankOverrides returns the bank's own overrides: every configuration key the
// bank has set, plus the disposition traits and reflect mission that live in
// the bank's own columns. It is the top layer of the resolution, so it names
// only what the bank itself overrides and never a shipped default — a trait the
// bank never set, and an empty mission, are absent rather than materialized.
func bankOverrides(bank storage.Bank) map[string]any {
	overrides := make(map[string]any, len(bank.Config)+4)
	for key, value := range bank.Config {
		overrides[key] = value
	}
	for trait, key := range dispositionFieldKeys {
		if value, ok := bank.Disposition[key]; ok {
			overrides[trait] = value
		}
	}
	if bank.Mission != "" {
		overrides["reflect_mission"] = bank.Mission
	}
	return overrides
}

// resolvedConfig layers the bank's overrides over the caller's tenant overrides
// and the process defaults, using the same resolver the write paths use. The
// result is the fully resolved configuration, not a fixed set of keys, so every
// field the bank or its tenant overrides appears with its effective value.
func resolvedConfig(overrides map[string]any, r *http.Request, defaults map[string]any) map[string]any {
	return config.ResolveLayers(overrides, httpx.TenantOverridesFrom(r), defaults)
}
