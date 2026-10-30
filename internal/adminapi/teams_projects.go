package adminapi

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
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

// createTeamProject records a project and creates the bank it owns.
//
// An existing bank under the project's id is a conflict unless the caller asked
// to adopt it: a bank somebody wrote to may hold human content, and a project
// record must not silently take it over. The registry row goes in last, so a
// bank that could not be created leaves no project behind — and the bank that
// was created for a row that then failed is removed again.
func (h *Handler) createTeamProject(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	if h.cfg.Banks == nil {
		writeError(w, http.StatusNotImplemented, "bank management is not configured")
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
	project.BankID = tenant.ProjectBankID(teamID, project.ProjectID)
	banks, err := h.cfg.Banks.ListBanks(r.Context(), teamID, project.BankID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal error")
		return
	}
	existing := containsBank(banks, project.BankID)
	if existing && !request.AdoptBank {
		writeError(w, http.StatusConflict,
			fmt.Sprintf("bank %q already exists; retry with adopt_bank=true to hand it to the project", project.BankID))
		return
	}
	created := false
	if !existing {
		if err := h.cfg.Banks.CreateBank(r.Context(), teamID, project.BankID, project.Name); err != nil {
			if errors.Is(err, storage.ErrBankExists) {
				writeError(w, http.StatusConflict,
					fmt.Sprintf("bank %q already exists; retry with adopt_bank=true to hand it to the project", project.BankID))
				return
			}
			writeError(w, http.StatusInternalServerError, "creating the project's bank failed")
			return
		}
		created = true
	}
	if err := h.cfg.Teams.CreateTeamProject(r.Context(), project); err != nil {
		if created {
			h.dropProjectBank(r.Context(), teamID, project.BankID)
		}
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

// dropProjectBank removes the bank a refused project creation just made, so a
// registry refusal does not leave a bank behind for a project that never was.
//
// The project row is the only thing that names the bank, so nothing would reach
// a bank left here. The compensation is best-effort: a failed delete is logged
// and the response still reports why the project was refused.
func (h *Handler) dropProjectBank(ctx context.Context, teamID, bankID string) {
	if _, err := h.cfg.Banks.DeleteBank(ctx, teamID, bankID); err != nil {
		slog.Default().Error("rolling back the bank of a refused project failed",
			"team_id", teamID, "bank_id", bankID, "error", err)
	}
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

// adoptProjectBank reports whether the bank a project owns is in the team's
// schema.
//
// Creating a project creates its bank, so this is the read that notices the two
// having drifted apart: a bank deleted behind the plane's back, or a project
// recorded before creation made one. It reports and changes nothing.
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
	teamID, projectID, tenantID := r.PathValue("id"), r.PathValue("project_id"), r.PathValue("tenant_id")
	if err := h.cfg.Teams.AddProjectMember(r.Context(), teamID, projectID, tenantID); err != nil {
		h.writeTeamError(w, err, "adding a project member")
		return
	}
	// The member's own bank is now an input to the project, so the merge is
	// asked for immediately rather than at the next scheduled tick: a member
	// added to a project expects the project's shared content to include what
	// they already knew. The read cache is dropped first, so their next read
	// does not answer from the moment before they joined.
	h.membershipChanged(tenantID)
	if h.cfg.Merges != nil {
		h.cfg.Merges.TriggerMerge(r.Context(), teamID, projectID)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// deleteProjectMember takes a member out of a project.
//
// The member's merge ledger goes with them. The ledger's foreign keys hang off
// team_member and team_project, not team_project_member, so a member removed from
// one project would otherwise leave rows behind — and a row whose member no longer
// contributes can never match, which would rebuild the project on every round
// forever.
//
// The content merged while they were on the project stays: it was shared then, and
// removing it is the separate, explicit purge-contribution operation.
func (h *Handler) deleteProjectMember(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	teamID, projectID, tenantID := r.PathValue("id"), r.PathValue("project_id"), r.PathValue("tenant_id")
	if err := h.cfg.Teams.RemoveProjectMember(r.Context(), teamID, projectID, tenantID); err != nil {
		writeRegistryError(w, err)
		return
	}
	h.clearMemberLedger(r.Context(), teamID, projectID, tenantID)
	// Their reads stop being served from the project immediately, rather than
	// after the cache's expiry.
	h.membershipChanged(tenantID)
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed"})
}

// membershipChanged tells the read path that one tenant's membership changed.
//
// It runs whether or not the deployment can merge: the membership cache is what
// decides which bank a read is served from, and that decision is wrong the moment
// the registry changes, merge job or no merge job.
func (h *Handler) membershipChanged(tenantID string) {
	if h.cfg.MembershipChanged == nil {
		return
	}
	h.cfg.MembershipChanged(tenantID)
}

// clearMemberLedger drops one member's merge ledger for one project.
//
// It is best-effort by design: the membership row is already gone, which is what
// the caller asked for, and a ledger left behind costs a redundant rebuild rather
// than a wrong answer. The next round's delete detection would also notice the
// member is no longer a contributor, so this is an optimization that keeps that
// round cheap — but it must be reported when it fails, because a silent failure
// here looks exactly like a correct one.
func (h *Handler) clearMemberLedger(ctx context.Context, teamID, projectID, tenantID string) {
	if h.cfg.Merges == nil {
		return
	}
	if err := h.cfg.Merges.ClearMemberProject(ctx, teamID, projectID, tenantID); err != nil {
		slog.Default().Warn("could not clear a removed member's merge ledger",
			"team_id", teamID, "project_id", projectID, "tenant_id", tenantID, "error", err)
	}
}

// purgeContribution withdraws one member's contribution from a project.
//
// It removes the membership, clears the member's merge ledger and asks for a
// rebuild — the three steps that together make the member's facts leave the team's
// shared content. It is refused outright (409) when the deployment cannot merge,
// because a removal alone would leave the content standing.
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
	teamID, projectID := r.PathValue("id"), r.PathValue("project_id")
	// A purge that cannot rebuild is refused before anything is removed. Taking
	// the member out and leaving their content in is not a purge: it is a removal
	// with the opposite of the requested effect.
	if h.cfg.Merges == nil {
		writeError(w, http.StatusConflict,
			"the project merge job is not enabled on this deployment, so a contribution cannot be withdrawn; remove the member instead")
		return
	}
	if err := h.cfg.Teams.RemoveProjectMember(r.Context(), teamID, projectID, member); err != nil {
		writeRegistryError(w, err)
		return
	}
	h.clearMemberLedger(r.Context(), teamID, projectID, member)
	h.membershipChanged(member)
	// The rebuild is what actually withdraws the content: the next round reads
	// only the members still on the project, so the removed member's facts are
	// dropped from the team's bank.
	if err := h.cfg.Merges.RequestRebuild(r.Context(), teamID, projectID); err != nil {
		// The membership is already gone, so the member is out; the content
		// withdraws on the next round's delete detection instead. Reporting the
		// failure keeps the operator from believing it already happened.
		writeError(w, http.StatusInternalServerError,
			fmt.Sprintf("the member was removed but the rebuild could not be requested: %v; their content withdraws on the next merge round", err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": "purged", "rebuild": true})
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
