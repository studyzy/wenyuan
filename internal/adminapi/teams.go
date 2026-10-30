package adminapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The team management routes.
//
// A team is a tenant of kind "team" with its own schema, so creating one goes
// through the same provisioning path a tenant does. Its routes live beside the
// tenant routes and answer 501 when the deployment has no team registry — a
// single-tenant process.

// bindTeamRoutes registers the team routes. It is a no-op registration either
// way: every handler checks h.cfg.Teams and answers 501 when it is absent.
func (h *Handler) bindTeamRoutes() {
	h.mux.HandleFunc("GET /admin/v1/teams", h.listTeams)
	h.mux.HandleFunc("POST /admin/v1/teams", h.createTeam)
	h.mux.HandleFunc("GET /admin/v1/teams/{id}", h.getTeam)
	h.mux.HandleFunc("PATCH /admin/v1/teams/{id}", h.patchTeam)
	h.mux.HandleFunc("DELETE /admin/v1/teams/{id}", h.deleteTeam)

	// A team is a tenant, so its credentials are managed through the same key
	// routes the tenant routes serve, reachable under the team's own path.
	h.mux.HandleFunc("GET /admin/v1/teams/{id}/keys", h.teamOnly(h.listKeys))
	h.mux.HandleFunc("POST /admin/v1/teams/{id}/keys", h.teamOnly(h.createKey))
	h.mux.HandleFunc("DELETE /admin/v1/teams/{id}/keys/{keyId}", h.teamOnly(h.revokeKey))

	h.mux.HandleFunc("GET /admin/v1/teams/{id}/members", h.listTeamMembers)
	h.mux.HandleFunc("PUT /admin/v1/teams/{id}/members/{tenant_id}", h.putTeamMember)
	h.mux.HandleFunc("DELETE /admin/v1/teams/{id}/members/{tenant_id}", h.deleteTeamMember)

	h.mux.HandleFunc("GET /admin/v1/teams/{id}/projects", h.listTeamProjects)
	h.mux.HandleFunc("POST /admin/v1/teams/{id}/projects", h.createTeamProject)
	h.mux.HandleFunc("GET /admin/v1/teams/{id}/projects/{project_id}", h.getTeamProject)
	h.mux.HandleFunc("DELETE /admin/v1/teams/{id}/projects/{project_id}", h.deleteTeamProject)
	h.mux.HandleFunc("POST /admin/v1/teams/{id}/projects/{project_id}/adopt-bank", h.adoptProjectBank)

	h.mux.HandleFunc("GET /admin/v1/teams/{id}/projects/{project_id}/members", h.listProjectMembers)
	h.mux.HandleFunc("PUT /admin/v1/teams/{id}/projects/{project_id}/members/{tenant_id}", h.putProjectMember)
	h.mux.HandleFunc("DELETE /admin/v1/teams/{id}/projects/{project_id}/members/{tenant_id}", h.deleteProjectMember)

	h.mux.HandleFunc("POST /admin/v1/teams/{id}/projects/{project_id}/purge-contribution", h.purgeContribution)
	h.mux.HandleFunc("GET /admin/v1/teams/{id}/projects/{project_id}/merge-status", h.getMergeStatus)
	h.mux.HandleFunc("POST /admin/v1/teams/{id}/projects/{project_id}/merge-reset", h.postMergeReset)
}

// teamsAbsent answers the 501 every team route shares. It reports whether it
// handled the request.
func (h *Handler) teamsAbsent(w http.ResponseWriter) bool {
	if h.cfg.Teams == nil {
		writeError(w, http.StatusNotImplemented, "team management is not configured")
		return true
	}
	return false
}

// teamOnly wraps a key handler so it refuses a tenant that is not a team.
//
// The key handlers themselves are the tenant ones: a team's credentials are its
// tenant's credentials, and reusing the handlers keeps one implementation of
// minting and revocation. Only the id in the path differs.
func (h *Handler) teamOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if h.teamsAbsent(w) {
			return
		}
		record, err := h.cfg.Registry.GetTenant(r.Context(), r.PathValue("id"))
		if err != nil {
			writeRegistryError(w, err)
			return
		}
		if !record.IsTeam() {
			writeError(w, http.StatusNotFound, "team not found")
			return
		}
		next(w, r)
	}
}

// teamResponse is a team as the management plane reports it: the tenant shape
// plus the counts the console lists.
type teamResponse struct {
	TenantID    string         `json:"tenant_id"`
	DisplayName string         `json:"display_name"`
	Status      string         `json:"status"`
	Kind        string         `json:"kind"`
	Config      map[string]any `json:"config"`
	MemberCount int            `json:"member_count"`
	// ProjectCount is the number of projects the team owns.
	ProjectCount int        `json:"project_count"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
}

type teamMemberResponse struct {
	TenantID    string    `json:"tenant_id"`
	DisplayName string    `json:"display_name"`
	Role        string    `json:"role"`
	JoinedAt    time.Time `json:"joined_at"`
}

type teamProjectResponse struct {
	ProjectID   string    `json:"project_id"`
	Name        string    `json:"name"`
	BankID      string    `json:"bank_id"`
	MemberCount int       `json:"member_count"`
	CreatedAt   time.Time `json:"created_at"`
}

type projectMemberResponse struct {
	TenantID    string    `json:"tenant_id"`
	DisplayName string    `json:"display_name"`
	AddedAt     time.Time `json:"added_at"`
}

func toTeamResponse(team tenant.Team) teamResponse {
	config := team.Config
	if config == nil {
		config = map[string]any{}
	}
	return teamResponse{
		TenantID:     team.ID,
		DisplayName:  team.DisplayName,
		Status:       string(team.Status),
		Kind:         string(team.KindValue()),
		Config:       config,
		MemberCount:  team.MemberCount,
		ProjectCount: team.ProjectCount,
		CreatedAt:    team.CreatedAt,
		UpdatedAt:    team.UpdatedAt,
		DeletedAt:    team.DeletedAt,
	}
}

func toTeamMemberResponses(members []tenant.TeamMember) []teamMemberResponse {
	out := make([]teamMemberResponse, 0, len(members))
	for _, member := range members {
		out = append(out, teamMemberResponse{
			TenantID:    member.TenantID,
			DisplayName: member.DisplayName,
			Role:        string(member.Role),
			JoinedAt:    member.JoinedAt,
		})
	}
	return out
}

func toTeamProjectResponses(projects []tenant.TeamProject) []teamProjectResponse {
	out := make([]teamProjectResponse, 0, len(projects))
	for _, project := range projects {
		out = append(out, toTeamProjectResponse(project))
	}
	return out
}

func toTeamProjectResponse(project tenant.TeamProject) teamProjectResponse {
	return teamProjectResponse{
		ProjectID:   project.ProjectID,
		Name:        project.Name,
		BankID:      project.BankID,
		MemberCount: project.MemberCount,
		CreatedAt:   project.CreatedAt,
	}
}

func toProjectMemberResponses(members []tenant.ProjectMember) []projectMemberResponse {
	out := make([]projectMemberResponse, 0, len(members))
	for _, member := range members {
		out = append(out, projectMemberResponse{
			TenantID:    member.TenantID,
			DisplayName: member.DisplayName,
			AddedAt:     member.AddedAt,
		})
	}
	return out
}

func (h *Handler) listTeams(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	teams, err := h.cfg.Teams.ListTeams(r.Context(), storage.TenantFilter{
		Query:  r.URL.Query().Get("q"),
		Limit:  parseLimit(r.URL.Query().Get("limit")),
		Offset: parseOffset(r.URL.Query().Get("offset")),
	})
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	out := make([]teamResponse, 0, len(teams))
	for _, team := range teams {
		out = append(out, toTeamResponse(team))
	}
	writeJSON(w, http.StatusOK, map[string]any{"teams": out})
}

// createTeam provisions a team tenant: the registry row, its schema, and one
// credential to start with. The response carries the key's plaintext exactly
// once, the way tenant creation does.
func (h *Handler) createTeam(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	var request struct {
		TenantID    string         `json:"tenant_id"`
		DisplayName string         `json:"display_name"`
		Config      map[string]any `json:"config"`
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
		Kind:        tenant.KindTeam,
		Config:      request.Config,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	// The team row is created through the tenant registry, so the id rules, the
	// duplicate check and the schema name all come from one place. A team never
	// adopts an existing schema: an adopted schema was not created as a team.
	if err := h.cfg.Registry.CreateTenant(r.Context(), record); err != nil {
		writeRegistryError(w, err)
		return
	}
	if err := h.cfg.Provisioner(r.Context(), record.ID, false); err != nil {
		if errors.Is(err, storage.ErrSchemaExists) || errors.Is(err, storage.ErrInvalidSchema) {
			// The schema exists but was never this team's schema: the row would
			// point at something we did not create, so it is rolled back.
			_, _ = h.cfg.Registry.DeleteTenant(r.Context(), record.ID, h.cfg.Now())
			writeRegistryError(w, err)
			return
		}
		writeError(w, http.StatusInternalServerError, "provisioning failed; the team remains provisioning")
		return
	}
	active := tenant.StatusActive
	created, err := h.cfg.Registry.UpdateTenant(r.Context(), record.ID, storage.TenantUpdate{Status: &active})
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	team, err := h.cfg.Teams.GetTeam(r.Context(), created.ID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	// One credential comes with the team, so an operator can hand the team a
	// working key without a second call. Its plaintext exists exactly once.
	plaintext, key, err := h.mintKey(r.Context(), record.ID, "team:initial", false, nil)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"team":      toTeamResponse(team),
		"plaintext": plaintext,
		"key":       toKeyResponse(key),
	})
}

func (h *Handler) getTeam(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	team, err := h.cfg.Teams.GetTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamResponse(team))
}

func (h *Handler) patchTeam(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	var body map[string]any
	if err := decodeJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	// The team route only renames. Anything else is refused by name rather than
	// silently ignored, because a caller that sent it expected it to apply.
	for field := range body {
		if field != "display_name" {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("unsupported field %q: a team patch only changes display_name", field))
			return
		}
	}
	displayName, ok := body["display_name"].(string)
	if !ok || strings.TrimSpace(displayName) == "" {
		writeError(w, http.StatusBadRequest, "display_name is required")
		return
	}
	team, err := h.cfg.Teams.UpdateTeamName(r.Context(), r.PathValue("id"), strings.TrimSpace(displayName))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamResponse(team))
}

func (h *Handler) deleteTeam(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	team, err := h.cfg.Teams.SoftDeleteTeam(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamResponse(team))
}

func (h *Handler) listTeamMembers(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	members, err := h.cfg.Teams.ListTeamMembers(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": toTeamMemberResponses(members)})
}

// putTeamMember adds a member, or updates the role of one already on the team.
// It is idempotent so a retried call cannot fail on its second attempt.
func (h *Handler) putTeamMember(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	var request struct {
		Role string `json:"role"`
	}
	if r.ContentLength != 0 {
		if err := decodeJSON(r, &request); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
	}
	role := tenant.MemberRole(strings.TrimSpace(request.Role))
	if role == "" {
		role = tenant.RoleMember
	}
	if !role.Valid() {
		writeError(w, http.StatusBadRequest, "role must be owner or member")
		return
	}
	if err := h.cfg.Teams.AddTeamMember(r.Context(), r.PathValue("id"), r.PathValue("tenant_id"), role); err != nil {
		h.writeTeamError(w, err, "adding a team member")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) deleteTeamMember(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	tenantID := r.PathValue("tenant_id")
	if err := h.cfg.Teams.RemoveTeamMember(r.Context(), r.PathValue("id"), tenantID); err != nil {
		h.writeTeamError(w, err, "removing a team member")
		return
	}
	// Every project membership went with the team membership, so their reads stop
	// being served from the team at once.
	h.membershipChanged(tenantID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// mintKey mints a credential for a tenant and returns its plaintext once. It is
// the same path createKey and the console session use.
func (h *Handler) mintKey(ctx context.Context, tenantID, label string, ephemeral bool, expiresAt *time.Time) (string, tenant.Key, error) {
	plaintext, key, err := tenant.NewKey(h.cfg.Now())
	if err != nil {
		return "", tenant.Key{}, err
	}
	key.TenantID = tenantID
	key.Label = label
	key.Ephemeral = ephemeral
	key.ExpiresAt = expiresAt
	if err := h.cfg.Registry.CreateKey(ctx, key); err != nil {
		return "", tenant.Key{}, err
	}
	return plaintext, key, nil
}
