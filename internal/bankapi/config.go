package bankapi

import (
	"net/http"

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
	overrides := bankOverrides(bank)
	httpx.WriteJSONFor(w, r, http.StatusOK, ConfigResponse{
		Config:    resolvedConfig(overrides),
		Overrides: overrides,
	})
}

// UpdateBankConfig serves the configuration write route, gated by the bank
// configuration feature flag. The retired-route messages direct clients here.
func (h *Handlers) UpdateBankConfig(w http.ResponseWriter, r *http.Request) {
	if !h.ConfigEnabled {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailUnknownBank))
		return
	}
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	var req ConfigWriteRequest
	if err := decodeBody(r, &req); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "request body is not valid JSON"))
		return
	}
	patch, failure := configPatch(req.Updates)
	if failure != nil {
		httpx.WriteValidationFor(w, r, failure)
		return
	}
	bank, err := h.Store.UpdateBank(r.Context(), bankID, patch)
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	overrides := bankOverrides(bank)
	httpx.WriteJSONFor(w, r, http.StatusOK, ConfigResponse{
		Config:    resolvedConfig(overrides),
		Overrides: overrides,
	})
}

// ResetBankConfig serves the configuration reset route. It removes every
// bank-specific override, leaving the bank on the global configuration, and
// reports the resulting state. It is gated by the same feature flag as the
// write route, because it is a write.
func (h *Handlers) ResetBankConfig(w http.ResponseWriter, r *http.Request) {
	if !h.ConfigEnabled {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, httpx.DetailUnknownBank))
		return
	}
	bankID, ok := h.resolveCanonical(w, r)
	if !ok {
		return
	}
	neutral := storage.DispositionTraitNeutral
	traits := map[string]int{
		storage.DispositionKeySkepticism: neutral,
		storage.DispositionKeyLiteralism: neutral,
		storage.DispositionKeyEmpathy:    neutral,
	}
	empty := ""
	bank, err := h.Store.UpdateBank(r.Context(), bankID, storage.BankPatch{
		Disposition: &traits,
		Mission:     &empty,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, validateBankExists(err))
		return
	}
	overrides := bankOverrides(bank)
	httpx.WriteJSONFor(w, r, http.StatusOK, ConfigResponse{
		Config:    resolvedConfig(overrides),
		Overrides: overrides,
	})
}

// bankOverrides extracts only the bank-specific trait and mission overrides.
// The wire keys stay in the flat four-key form the client expects even though
// storage keeps the traits as one object.
func bankOverrides(bank storage.Bank) map[string]any {
	disposition := bank.NormalizedDisposition()
	return map[string]any{
		"disposition_skepticism": disposition[storage.DispositionKeySkepticism],
		"disposition_literalism": disposition[storage.DispositionKeyLiteralism],
		"disposition_empathy":    disposition[storage.DispositionKeyEmpathy],
		"reflect_mission":        bank.Mission,
	}
}

// resolvedConfig merges the bank overrides over a resolved base configuration.
func resolvedConfig(overrides map[string]any) map[string]any {
	base := map[string]any{
		"disposition_skepticism": storage.DispositionTraitNeutral,
		"disposition_literalism": storage.DispositionTraitNeutral,
		"disposition_empathy":    storage.DispositionTraitNeutral,
		"reflect_mission":        "",
	}
	for key, value := range overrides {
		base[key] = value
	}
	return base
}
