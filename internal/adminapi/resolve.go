package adminapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// resolveTenantKey maps a tenant credential to the tenant it belongs to.
//
// The console holds a tenant key, not a tenant id: it keeps the key in an
// encrypted cookie and renews the short-lived session through
// /tenants/{id}/session, which needs the id. The check is delegated to the same
// resolver the data plane authenticates with, so a key this reports as valid is
// one the data plane accepts — a deployment that resolves here but rejects it
// there (single-tenant mode) cannot hand out a session that works.
func (h *Handler) resolveTenantKey(w http.ResponseWriter, r *http.Request) {
	if h.cfg.ResolveKey == nil {
		writeError(w, http.StatusNotImplemented, "tenant key resolution is not configured")
		return
	}
	var request struct {
		Key string `json:"key"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	credential := strings.TrimSpace(request.Key)
	if credential == "" {
		writeError(w, http.StatusBadRequest, "key is required")
		return
	}
	tenantID, err := h.cfg.ResolveKey(r.Context(), credential)
	if err != nil {
		var authErr *tenant.AuthenticationError
		if errors.As(err, &authErr) {
			writeError(w, http.StatusUnauthorized, "authentication failed")
			return
		}
		writeError(w, http.StatusServiceUnavailable, "tenant resolution unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"tenant_id": tenantID})
}
