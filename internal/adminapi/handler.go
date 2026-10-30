package adminapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// Config is what the admin handler needs from its deployment.
type Config struct {
	// Registry stores tenants and their keys.
	Registry storage.TenantRegistry
	// OperatorKey authenticates the management plane. It never enters the
	// database and is separate from every tenant key.
	OperatorKey string
	// Actor names the operator in audit-shaped labels such as session keys.
	// It defaults to "operator".
	Actor string
	// Provisioner creates or adopts a tenant's schema. A nil value treats
	// provisioning as a no-op, which is what the in-memory control plane needs.
	Provisioner func(ctx context.Context, tenantID string, adopt bool) error
	// Migrator brings one tenant schema to the current series. Nil answers 501.
	Migrator func(ctx context.Context, tenantID string) error
	// Dropper removes a soft-deleted tenant's schema. Nil answers 501.
	Dropper func(ctx context.Context, tenantID string) error
	// ResolveKey maps a tenant credential to its tenant id using the same
	// checks the data plane applies. The console needs the id, not the key: it
	// renews its short-lived session through /tenants/{id}/session. Nil answers
	// 501, which is what a single-tenant deployment gets.
	ResolveKey func(ctx context.Context, credential string) (string, error)
	// Now supplies the clock. It defaults to the wall clock in UTC.
	Now func() time.Time
	// Personal, when non-nil, serves the personal aggregate routes. It is a
	// top-level field rather than part of TeamConfig because the aggregate exists
	// without teams: a single-tenant deployment has it too.
	Personal PersonalAdmin

	TeamConfig
}

// Handler serves the management plane. It is a separate mux from the data
// plane: its routes never appear in /openapi.json and its credential is the
// operator key, not a tenant key.
type Handler struct {
	cfg Config
	mux *http.ServeMux
}

// New builds the management handler and its route table.
func New(cfg Config) (*Handler, error) {
	if cfg.Registry == nil {
		return nil, errors.New("adminapi: registry is required")
	}
	if strings.TrimSpace(cfg.OperatorKey) == "" {
		return nil, errors.New("adminapi: operator key is required")
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Actor == "" {
		cfg.Actor = "operator"
	}
	if cfg.Provisioner == nil {
		cfg.Provisioner = func(context.Context, string, bool) error { return nil }
	}
	handler := &Handler{cfg: cfg, mux: http.NewServeMux()}
	handler.mux.HandleFunc("GET /admin/v1/tenants", handler.listTenants)
	handler.mux.HandleFunc("POST /admin/v1/tenants", handler.createTenant)
	handler.mux.HandleFunc("GET /admin/v1/tenants/{id}", handler.getTenant)
	handler.mux.HandleFunc("PATCH /admin/v1/tenants/{id}", handler.patchTenant)
	handler.mux.HandleFunc("DELETE /admin/v1/tenants/{id}", handler.deleteTenant)
	handler.mux.HandleFunc("POST /admin/v1/tenants/{id}/purge", handler.purgeTenant)
	handler.mux.HandleFunc("POST /admin/v1/tenants/{id}/migrate", handler.migrateTenant)
	handler.mux.HandleFunc("GET /admin/v1/tenants/{id}/keys", handler.listKeys)
	handler.mux.HandleFunc("POST /admin/v1/tenants/{id}/keys", handler.createKey)
	handler.mux.HandleFunc("DELETE /admin/v1/tenants/{id}/keys/{keyId}", handler.revokeKey)
	handler.mux.HandleFunc("POST /admin/v1/tenants/{id}/session", handler.sessionKey)
	handler.mux.HandleFunc("POST /admin/v1/tenants/resolve", handler.resolveTenantKey)
	handler.mux.HandleFunc("GET /admin/v1/overview", handler.overview)
	handler.mux.HandleFunc("GET /admin/v1/tenants/{id}/aggregate", handler.getPersonalAggregateStatus)
	handler.mux.HandleFunc("POST /admin/v1/tenants/{id}/aggregate/reset", handler.postPersonalAggregateReset)
	handler.mux.HandleFunc("POST /admin/v1/tenants/{id}/aggregate/rebuild", handler.postPersonalAggregateRebuild)
	handler.bindTeamRoutes()
	return handler, nil
}

// ServeHTTP authenticates the operator before dispatching.
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		writeError(w, http.StatusUnauthorized, "operator credential required")
		return
	}
	h.mux.ServeHTTP(w, r)
}

func (h *Handler) authorized(r *http.Request) bool {
	header := strings.TrimSpace(r.Header.Get("Authorization"))
	const scheme = "Bearer "
	if !strings.HasPrefix(header, scheme) {
		return false
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, scheme))
	return subtle.ConstantTimeCompare([]byte(provided), []byte(h.cfg.OperatorKey)) == 1
}

// tenantResponse is the management plane's tenant representation. It is not
// the data plane's bank shape and carries no credential material.
type tenantResponse struct {
	TenantID    string         `json:"tenant_id"`
	DisplayName string         `json:"display_name"`
	Status      string         `json:"status"`
	Kind        string         `json:"kind"`
	Config      map[string]any `json:"config"`
	CreatedAt   time.Time      `json:"created_at"`
	UpdatedAt   time.Time      `json:"updated_at"`
	DeletedAt   *time.Time     `json:"deleted_at,omitempty"`
}

// keyResponse lists a credential without its hash or plaintext.
type keyResponse struct {
	KeyID      string     `json:"key_id"`
	Prefix     string     `json:"prefix"`
	Label      string     `json:"label"`
	Ephemeral  bool       `json:"ephemeral"`
	ExpiresAt  *time.Time `json:"expires_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	LastUsedAt *time.Time `json:"last_used_at,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
}

func toTenantResponse(record tenant.Record) tenantResponse {
	config := record.Config
	if config == nil {
		config = map[string]any{}
	}
	return tenantResponse{
		TenantID:    record.ID,
		DisplayName: record.DisplayName,
		Status:      string(record.Status),
		Kind:        string(record.KindValue()),
		Config:      config,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
		DeletedAt:   record.DeletedAt,
	}
}

func toKeyResponse(key tenant.Key) keyResponse {
	return keyResponse{
		KeyID:      key.ID,
		Prefix:     key.Prefix,
		Label:      key.Label,
		Ephemeral:  key.Ephemeral,
		ExpiresAt:  key.ExpiresAt,
		RevokedAt:  key.RevokedAt,
		LastUsedAt: key.LastUsedAt,
		CreatedAt:  key.CreatedAt,
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

// writeRegistryError maps the registry's sentinels onto status codes. A
// storage failure is a 500 and its detail stays in the log, not the response.
func writeRegistryError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, tenant.ErrInvalidID):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, tenant.ErrExists):
		writeError(w, http.StatusConflict, "tenant or key already exists")
	case errors.Is(err, tenant.ErrNotFound):
		writeError(w, http.StatusNotFound, "tenant or key not found")
	case errors.Is(err, tenant.ErrInvalidTransition):
		writeError(w, http.StatusConflict, err.Error())
	case errors.Is(err, tenant.ErrKindMismatch):
		writeError(w, http.StatusNotFound, "team not found")
	case errors.Is(err, tenant.ErrInvalidProject):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, storage.ErrSchemaExists):
		writeError(w, http.StatusConflict, "schema already exists; retry with adopt=true")
	case errors.Is(err, storage.ErrInvalidSchema):
		writeError(w, http.StatusBadRequest, err.Error())
	case errors.Is(err, storage.ErrSchemaProtected):
		writeError(w, http.StatusConflict, err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal error")
	}
}

func decodeJSON(r *http.Request, target any) error {
	decoder := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	// The body must be one JSON value. A second value after it, or any trailing
	// garbage, is a malformed request rather than a value to ignore: a caller
	// that sent two objects does not get the first one applied silently.
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("unexpected data after the JSON value")
	}
	return nil
}

func parseStatus(raw string) (tenant.Status, bool) {
	switch status := tenant.Status(strings.TrimSpace(raw)); status {
	case tenant.StatusProvisioning, tenant.StatusActive, tenant.StatusSuspended, tenant.StatusDeleted:
		return status, true
	}
	return "", false
}

func parseLimit(raw string) int {
	if raw == "" {
		return 0
	}
	limit, err := strconv.Atoi(raw)
	if err != nil || limit <= 0 {
		return 0
	}
	if limit > 200 {
		return 200
	}
	return limit
}

func parseOffset(raw string) int {
	offset, err := strconv.Atoi(raw)
	if err != nil || offset < 0 {
		return 0
	}
	return offset
}
