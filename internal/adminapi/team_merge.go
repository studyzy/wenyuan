package adminapi

import (
	"net/http"
	"time"
)

// The management plane's merge routes.
//
// A project's shared content is produced by the background merge job, so the plane
// reports it rather than changing it — except for a reset, which is the operator's
// way out of a circuit breaker that halted a project.

// mergeStatusResponse is the read-only view a console shows.
type mergeStatusResponse struct {
	ProjectID string `json:"project_id"`
	BankID    string `json:"bank_id"`
	// Merged reports whether any round has completed.
	Merged bool `json:"merged"`
	// LastMergedAt is when the most recent round completed, absent when none has.
	LastMergedAt *time.Time `json:"last_merged_at,omitempty"`
	// LastError is the most recent round's failure, empty after a success.
	LastError string `json:"last_error"`
	// Halted reports the circuit breaker being open: the project is skipped until
	// an operator resets it.
	Halted bool `json:"halted"`
	// FailCount is consecutive failed rounds.
	FailCount int `json:"fail_count"`
	// PendingMembers counts members whose contribution is not yet merged.
	PendingMembers int `json:"pending_members"`
	// TeamFacts counts the team bank's merged facts.
	TeamFacts int `json:"team_facts"`
	// UnmanagedFacts counts world facts in the team bank that no merge round
	// wrote. The next round replaces them, so a non-zero value is what tells an
	// operator a team bank is being written directly.
	UnmanagedFacts int `json:"unmanaged_facts"`
}

// getMergeStatus reports one project's merge state.
func (h *Handler) getMergeStatus(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	if h.cfg.Merges == nil {
		writeError(w, http.StatusConflict,
			"the project merge job is not enabled on this deployment")
		return
	}
	status, err := h.cfg.Merges.MergeStatus(r.Context(), r.PathValue("id"), r.PathValue("project_id"))
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, mergeStatusResponse{
		ProjectID:      status.ProjectID,
		BankID:         status.BankID,
		Merged:         status.Merged,
		LastMergedAt:   status.LastMergedAt,
		LastError:      status.LastError,
		Halted:         status.Halted,
		FailCount:      status.FailCount,
		PendingMembers: status.PendingMembers,
		TeamFacts:      status.TeamFacts,
		UnmanagedFacts: status.UnmanagedFacts,
	})
}

// postMergeReset clears a halted project's circuit breaker.
func (h *Handler) postMergeReset(w http.ResponseWriter, r *http.Request) {
	if h.teamsAbsent(w) {
		return
	}
	if h.cfg.Merges == nil {
		writeError(w, http.StatusConflict,
			"the project merge job is not enabled on this deployment")
		return
	}
	teamID, projectID := r.PathValue("id"), r.PathValue("project_id")
	if _, err := h.cfg.Teams.GetTeamProject(r.Context(), teamID, projectID); err != nil {
		writeRegistryError(w, err)
		return
	}
	if err := h.cfg.Merges.ResetMergeState(r.Context(), teamID, projectID); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"project_id": projectID, "halted": false})
}
