package adminapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

func (h *Handler) listTenants(w http.ResponseWriter, r *http.Request) {
	filter := storage.TenantFilter{
		Query:  r.URL.Query().Get("q"),
		Limit:  parseLimit(r.URL.Query().Get("limit")),
		Offset: parseOffset(r.URL.Query().Get("offset")),
	}
	if raw := r.URL.Query().Get("status"); raw != "" {
		status, ok := parseStatus(raw)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown tenant status")
			return
		}
		filter.Status = status
	}
	records, err := h.cfg.Registry.ListTenants(r.Context(), filter)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	tenants := make([]tenantResponse, 0, len(records))
	for _, record := range records {
		tenants = append(tenants, toTenantResponse(record))
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenants": tenants})
}

func (h *Handler) createTenant(w http.ResponseWriter, r *http.Request) {
	var request struct {
		TenantID    string         `json:"tenant_id"`
		DisplayName string         `json:"display_name"`
		Config      map[string]any `json:"config"`
		Adopt       bool           `json:"adopt"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	displayName := strings.TrimSpace(request.DisplayName)
	if displayName == "" {
		displayName = request.TenantID
	}
	now := h.cfg.Now()
	record := tenant.Record{
		ID:          request.TenantID,
		DisplayName: displayName,
		Status:      tenant.StatusProvisioning,
		Config:      request.Config,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := h.cfg.Registry.CreateTenant(r.Context(), record); err != nil {
		writeRegistryError(w, err)
		return
	}
	if err := h.cfg.Provisioner(r.Context(), record.ID, request.Adopt); err != nil {
		if errors.Is(err, storage.ErrSchemaExists) || errors.Is(err, storage.ErrInvalidSchema) {
			writeRegistryError(w, err)
			return
		}
		writeError(w, http.StatusInternalServerError, "provisioning failed; the tenant remains provisioning")
		return
	}
	active := tenant.StatusActive
	created, err := h.cfg.Registry.UpdateTenant(r.Context(), record.ID, storage.TenantUpdate{Status: &active})
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTenantResponse(created))
}

func (h *Handler) getTenant(w http.ResponseWriter, r *http.Request) {
	record, err := h.cfg.Registry.GetTenant(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTenantResponse(record))
}

func (h *Handler) patchTenant(w http.ResponseWriter, r *http.Request) {
	var request struct {
		DisplayName *string        `json:"display_name"`
		Config      map[string]any `json:"config"`
		Status      *string        `json:"status"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	update := storage.TenantUpdate{DisplayName: request.DisplayName, Config: request.Config}
	if request.Status != nil {
		status, ok := parseStatus(*request.Status)
		if !ok {
			writeError(w, http.StatusBadRequest, "unknown tenant status")
			return
		}
		update.Status = &status
	}
	record, err := h.cfg.Registry.UpdateTenant(r.Context(), r.PathValue("id"), update)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTenantResponse(record))
}

func (h *Handler) deleteTenant(w http.ResponseWriter, r *http.Request) {
	record, err := h.cfg.Registry.DeleteTenant(r.Context(), r.PathValue("id"), h.cfg.Now())
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTenantResponse(record))
}

// purgeTenant drops a soft-deleted tenant's schema. The confirmation must name
// the tenant, and the registry row must already be deleted: a purge is a
// destructive, one-way action and never follows directly from an active tenant.
func (h *Handler) purgeTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	var request struct {
		Confirm string `json:"confirm"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	if request.Confirm != tenantID {
		writeError(w, http.StatusBadRequest, "confirmation does not match the tenant id")
		return
	}
	record, err := h.cfg.Registry.GetTenant(r.Context(), tenantID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	if record.Status != tenant.StatusDeleted {
		writeError(w, http.StatusConflict, "tenant must be deleted before purge")
		return
	}
	if h.cfg.Dropper == nil {
		writeError(w, http.StatusNotImplemented, "purge is not configured")
		return
	}
	if err := h.cfg.Dropper(r.Context(), tenantID); err != nil {
		if errors.Is(err, storage.ErrSchemaProtected) || errors.Is(err, storage.ErrInvalidSchema) {
			writeRegistryError(w, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"tenant_id": tenantID, "purged": true})
}

func (h *Handler) migrateTenant(w http.ResponseWriter, r *http.Request) {
	tenantID := r.PathValue("id")
	record, err := h.cfg.Registry.GetTenant(r.Context(), tenantID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	if record.Status == tenant.StatusDeleted {
		writeError(w, http.StatusConflict, "a deleted tenant cannot be migrated; purge it or recreate it")
		return
	}
	if h.cfg.Migrator == nil {
		writeError(w, http.StatusNotImplemented, "tenant migrations are not configured")
		return
	}
	if err := h.cfg.Migrator(r.Context(), tenantID); err != nil {
		if errors.Is(err, storage.ErrSchemaProtected) || errors.Is(err, storage.ErrInvalidSchema) {
			writeRegistryError(w, err)
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "migrated"})
}

func (h *Handler) overview(w http.ResponseWriter, r *http.Request) {
	records, err := h.cfg.Registry.ListTenants(r.Context(), storage.TenantFilter{})
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	counts := map[string]int{"total": len(records)}
	for _, record := range records {
		counts[string(record.Status)]++
	}
	writeJSON(w, http.StatusOK, counts)
}
