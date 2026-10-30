package adminapi

import (
	"net/http"
	"strings"
	"time"
)

func (h *Handler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.cfg.Registry.ListKeys(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	response := make([]keyResponse, 0, len(keys))
	for _, key := range keys {
		response = append(response, toKeyResponse(key))
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": response})
}

func (h *Handler) createKey(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Label     string  `json:"label"`
		ExpiresAt *string `json:"expires_at"`
		Ephemeral bool    `json:"ephemeral"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	var expiresAt *time.Time
	if request.ExpiresAt != nil && strings.TrimSpace(*request.ExpiresAt) != "" {
		parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(*request.ExpiresAt))
		if err != nil {
			writeError(w, http.StatusBadRequest, "expires_at must be RFC3339")
			return
		}
		expiresAt = &parsed
	}
	h.issueKey(w, r, request.Label, request.Ephemeral, expiresAt)
}

func (h *Handler) sessionKey(w http.ResponseWriter, r *http.Request) {
	expiresAt := h.cfg.Now().Add(10 * time.Minute)
	h.issueKey(w, r, "console:"+h.cfg.Actor, true, &expiresAt)
}

// issueKey mints a credential and returns its plaintext exactly once.
func (h *Handler) issueKey(w http.ResponseWriter, r *http.Request, label string, ephemeral bool, expiresAt *time.Time) {
	tenantID := r.PathValue("id")
	if strings.TrimSpace(label) == "" {
		label = "key"
	}
	plaintext, key, err := h.mintKey(r.Context(), tenantID, label, ephemeral, expiresAt)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"plaintext": plaintext,
		"key":       toKeyResponse(key),
	})
}

func (h *Handler) revokeKey(w http.ResponseWriter, r *http.Request) {
	if err := h.cfg.Registry.RevokeKey(r.Context(), r.PathValue("id"), r.PathValue("keyId"), h.cfg.Now()); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})
}
