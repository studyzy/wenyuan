package adminapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// The management plane's project routes.
//
// A project is a registry row plus the banks its name owns: "<team>::<name>"
// and anything under that prefix. Creating and deleting therefore touch both
// the registry and the team's schema, and the delete is the one place the plane
// removes banks on the operator's behalf.

func (h *Handler) listTeamProjects(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	projects, err := h.cfg.Teams.ListTeamProjects(r.Context(), r.PathValue("id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"projects": toTeamProjectResponses(projects)})
}

// createTeamProject records a project and hands its bank to the team.
//
// An existing bank under the project's id is a conflict unless the caller asked
// to adopt it: a bank somebody wrote to may hold human content, and a project
// record must not silently take it over.
func (h *Handler) createTeamProject(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	var request struct {
		ProjectID string `json:"project_id"`
		Name      string `json:"name"`
		AdoptBank bool   `json:"adopt_bank"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	teamID := r.PathValue("id")
	project := tenant.TeamProject{
		TeamID:    teamID,
		ProjectID: strings.TrimSpace(request.ProjectID),
		Name:      strings.TrimSpace(request.Name),
	}
	if err := tenant.ValidateProjectID(project.ProjectID); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := tenant.ValidateProjectName(project.Name); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	project.BankID = tenant.ProjectBankID(teamID, project.Name)
	if h.cfg.Banks != nil {
		banks, err := h.cfg.Banks.ListBanks(r.Context(), teamID, project.BankID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		for _, bankID := range banks {
			if bankID == project.BankID && !request.AdoptBank {
				writeError(w, http.StatusConflict,
					fmt.Sprintf("bank %q already exists; retry with adopt_bank=true to hand it to the project", bankID))
				return
			}
		}
	}
	if err := h.cfg.Teams.CreateTeamProject(r.Context(), project); err != nil {
		writeRegistryError(w, err)
		return
	}
	stored, err := h.cfg.Teams.GetTeamProject(r.Context(), teamID, project.ProjectID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, toTeamProjectResponse(stored))
}

func (h *Handler) getTeamProject(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	project, err := h.cfg.Teams.GetTeamProject(r.Context(), r.PathValue("id"), r.PathValue("project_id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, toTeamProjectResponse(project))
}

// deleteTeamProject removes a project and every bank it owns.
//
// Deleting the project rows alone would leave banks nobody can reach, so the
// banks go with the project. That makes the delete destructive and irreversible,
// which is why it demands an explicit confirmation: the body must name the
// project and state delete_banks.
func (h *Handler) deleteTeamProject(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	teamID, projectID := r.PathValue("id"), r.PathValue("project_id")
	var request struct {
		Confirm     string `json:"confirm"`
		DeleteBanks *bool  `json:"delete_banks"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body: deleting a project requires {\"confirm\": \"<project_id>\", \"delete_banks\": true}")
		return
	}
	if request.DeleteBanks == nil || !*request.DeleteBanks {
		writeError(w, http.StatusBadRequest, "delete_banks must be true: deleting a project deletes the banks it owns")
		return
	}
	if request.Confirm != projectID {
		writeError(w, http.StatusBadRequest, "confirmation does not match the project id")
		return
	}
	project, err := h.cfg.Teams.GetTeamProject(r.Context(), teamID, projectID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	deleted := []string{}
	if h.cfg.Banks != nil {
		banks, err := h.cfg.Banks.ListBanks(r.Context(), teamID, project.BankID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "internal error")
			return
		}
		// The project's own bank must be among them. When it is not, the registry
		// and the schema disagree about what this project owns, and deleting on
		// that disagreement could take another project's bank with it.
		if !containsBank(banks, project.BankID) {
			writeError(w, http.StatusConflict, fmt.Sprintf(
				"project %q owns bank %q, but no such bank exists in the team's schema; refusing to delete until the registry and the schema agree",
				project.ProjectID, project.BankID))
			return
		}
		for _, bankID := range banks {
			if _, err := h.cfg.Banks.DeleteBank(r.Context(), teamID, bankID); err != nil {
				writeError(w, http.StatusInternalServerError,
					fmt.Sprintf("deleting bank %q failed: the project was not removed and the delete can be retried", bankID))
				return
			}
			deleted = append(deleted, bankID)
		}
	}
	if err := h.cfg.Teams.DeleteTeamProject(r.Context(), teamID, projectID); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "deleted_banks": deleted})
}

// adoptProjectBank creates the bank a project owns when it does not exist yet.
//
// Projects are created with a registry row only; the bank appears when the team
// first writes to it, or here, when an operator wants it ready.
func (h *Handler) adoptProjectBank(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	teamID, projectID := r.PathValue("id"), r.PathValue("project_id")
	project, err := h.cfg.Teams.GetTeamProject(r.Context(), teamID, projectID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	if h.cfg.Banks == nil {
		writeError(w, http.StatusNotImplemented, "bank management is not configured")
		return
	}
	banks, err := h.cfg.Banks.ListBanks(r.Context(), teamID, project.BankID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"project_id": projectID,
		"bank_id":    project.BankID,
		"exists":     containsBank(banks, project.BankID),
	})
}

func (h *Handler) listProjectMembers(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	members, err := h.cfg.Teams.ListProjectMembers(r.Context(), r.PathValue("id"), r.PathValue("project_id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"members": toProjectMemberResponses(members)})
}

func (h *Handler) putProjectMember(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	if err := h.cfg.Teams.AddProjectMember(r.Context(), r.PathValue("id"), r.PathValue("project_id"), r.PathValue("tenant_id")); err != nil {
		h.writeTeamError(w, err, "adding a project member")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) deleteProjectMember(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	if err := h.cfg.Teams.RemoveProjectMember(r.Context(), r.PathValue("id"), r.PathValue("project_id"), r.PathValue("tenant_id")); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// purgeContribution takes a member out of a project.
//
// The merge job that turns a member's facts into the team's bank does not exist
// yet, so there is nothing to rebuild: the call reports rebuild false and only
// removes the membership.
func (h *Handler) purgeContribution(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	var request struct {
		TenantID string `json:"tenant_id"`
	}
	if err := decodeJSON(r, &request); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	member := strings.TrimSpace(request.TenantID)
	if member == "" {
		writeError(w, http.StatusBadRequest, "tenant_id is required")
		return
	}
	if err := h.cfg.Teams.RemoveProjectMember(r.Context(), r.PathValue("id"), r.PathValue("project_id"), member); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":           "purged",
		"rebuild":          false,
		"rebuild_deferred": "the project merge job is not implemented yet; nothing was rebuilt",
	})
}

// writeTeamError maps the refusals a membership change can produce. They are not
// all the registry's own: an inactive or missing member and a non-team tenant
// are conflicts the operator can act on, not server faults.
func (h *Handler) writeTeamError(w http.ResponseWriter, err error, action string) {
	switch {
	case errors.Is(err, tenant.ErrInvalidTransition):
		writeError(w, http.StatusConflict,
			action+" failed: the tenant must exist, be active and already be a member of the team")
	case errors.Is(err, tenant.ErrKindMismatch):
		writeError(w, http.StatusNotFound, "team not found")
	case errors.Is(err, tenant.ErrNotFound):
		writeError(w, http.StatusNotFound, "team, project or tenant not found")
	case errors.Is(err, tenant.ErrExists):
		writeError(w, http.StatusConflict, "already present")
	default:
		writeRegistryError(w, err)
	}
}

// containsBank reports whether ids holds bankID.
func containsBank(ids []string, bankID string) bool {
	for _, id := range ids {
		if id == bankID {
			return true
		}
	}
	return false
}
