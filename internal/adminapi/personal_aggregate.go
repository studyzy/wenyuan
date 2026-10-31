package adminapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// The management plane's personal aggregate routes.
//
// The aggregate bank `<tenant>::myself` is produced and managed by the background
// rollup, so the plane reports it rather than writing into it — except for a
// reset, which is the operator's way out of a halted tenant, and a rebuild, which
// asks the next round to ignore its watermarks.

// PersonalAdmin is the management plane's handle on a tenant's aggregate.
//
// It answers the team case as well as the personal one: a team has no aggregate of
// its own, but its members do, and a team page that showed nothing would read as
// "this team's memory is broken".
type PersonalAdmin interface {
	// PersonalAggregateStatus reports a tenant's aggregate state. It reports
	// ErrNotPersonal for a team tenant, which the handler answers from the team
	// plane instead.
	PersonalAggregateStatus(ctx context.Context, tenantID string) (storage.PersonalStatus, error)
	// TeamMembers returns the member tenants of one team, in join order.
	TeamMembers(ctx context.Context, teamID string) ([]string, error)
	// ResetPersonalAggregate clears a halted tenant's circuit breaker.
	ResetPersonalAggregate(ctx context.Context, tenantID string) error
	// RebuildPersonalAggregate asks the next round to recompute everything.
	RebuildPersonalAggregate(ctx context.Context, tenantID string) error
}

// ErrNotPersonal reports that a tenant takes no aggregate of its own.
var ErrNotPersonal = errors.New("tenant has no aggregate of its own")

// personalAggregateResponse is the read-only view a console shows.
type personalAggregateResponse struct {
	TenantID string `json:"tenant_id"`
	// Kind is the tenant's kind. A team reports "team" and no aggregate of its
	// own.
	Kind string `json:"kind"`
	// Aggregate reports whether this tenant takes an aggregate of its own.
	Aggregate bool `json:"aggregate"`
	// BankID is the aggregate bank, absent when the tenant has none.
	BankID string `json:"bank_id,omitempty"`
	// SharedSchema reports that every credential resolves to one schema, so this
	// aggregate belongs to the deployment rather than to one person.
	SharedSchema bool `json:"shared_schema"`
	// LastRoundAt is when a round last ran, absent when none has.
	LastRoundAt *time.Time `json:"last_round_at,omitempty"`
	// LastError is the most recent round's failure, empty after a success.
	LastError string `json:"last_error"`
	// Halted reports the circuit breaker being open.
	Halted     bool `json:"halted"`
	FailCount  int  `json:"fail_count"`
	Rebuilding bool `json:"rebuilding"`
	// AdoptedPreexisting reports the aggregate bank held content before the
	// rollup first wrote it; that content was replaced.
	AdoptedPreexisting bool `json:"adopted_preexisting"`
	// SourceBanks counts the banks a round reads.
	SourceBanks int `json:"source_banks"`
	// PendingBanks counts the source banks a round has not yet reached.
	PendingBanks int `json:"pending_banks"`
	// AggregateFacts counts the rows the rollup wrote; UserFacts counts the rows
	// in the aggregate bank it did not write, which the next round replaces.
	AggregateFacts int `json:"aggregate_facts"`
	UserFacts      int `json:"user_facts"`
	// Links and Topics count the navigation index.
	Links  int `json:"links"`
	Topics int `json:"topics"`
	// Members lists each team member's own aggregate. It is always present for a
	// team tenant — an empty list means the team has no members yet — and absent
	// for a person.
	Members []personalMemberResponse `json:"members,omitempty"`
	// Reason explains an absent aggregate, present only for a team tenant.
	Reason string `json:"reason,omitempty"`
}

// personalMemberResponse is one team member's aggregate.
//
// It is not summed into a total: each member's aggregate is theirs, and a team
// reading their counts must still read as "per member".
type personalMemberResponse struct {
	TenantID     string     `json:"tenant_id"`
	BankID       string     `json:"bank_id"`
	LastRoundAt  *time.Time `json:"last_round_at,omitempty"`
	LastError    string     `json:"last_error"`
	Halted       bool       `json:"halted"`
	AggregateNum int        `json:"aggregate_facts"`
}

// getPersonalAggregateStatus reports one tenant's aggregate state.
func (h *Handler) getPersonalAggregateStatus(w http.ResponseWriter, r *http.Request) {
	if h.personalAbsent(w) {
		return
	}
	tenantID := r.PathValue("id")
	status, err := h.cfg.Personal.PersonalAggregateStatus(r.Context(), tenantID)
	if errors.Is(err, ErrNotPersonal) {
		h.writeTeamAggregates(w, r, tenantID)
		return
	}
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, personalAggregateResponse{
		TenantID:           tenantID,
		Kind:               status.Kind,
		Aggregate:          status.Aggregate,
		BankID:             status.BankID,
		SharedSchema:       status.SharedSchema,
		LastRoundAt:        status.LastRoundAt,
		LastError:          status.LastError,
		Halted:             status.Halted,
		FailCount:          status.FailCount,
		Rebuilding:         status.RebuildRequested,
		AdoptedPreexisting: status.AdoptedPreexisting,
		SourceBanks:        status.SourceBanks,
		PendingBanks:       status.PendingBanks,
		AggregateFacts:     status.AggregateFacts,
		UserFacts:          status.UserFacts,
		Links:              status.Links,
		Topics:             status.Topics,
	})
}

// writeTeamAggregates answers a team tenant with its members' aggregates.
func (h *Handler) writeTeamAggregates(w http.ResponseWriter, r *http.Request, teamID string) {
	members, err := h.cfg.Personal.TeamMembers(r.Context(), teamID)
	if err != nil {
		writeRegistryError(w, err)
		return
	}
	response := personalAggregateResponse{
		TenantID:  teamID,
		Kind:      "team",
		Aggregate: false,
		Reason:    "a team tenant takes no aggregate of its own; its members' aggregates are listed below",
		Members:   make([]personalMemberResponse, 0, len(members)),
	}
	for _, member := range members {
		status, err := h.cfg.Personal.PersonalAggregateStatus(r.Context(), member)
		if err != nil {
			// One member's aggregate failing to read must not hide the others.
			response.Members = append(response.Members, personalMemberResponse{
				TenantID: member, LastError: err.Error(),
			})
			continue
		}
		response.Members = append(response.Members, personalMemberResponse{
			TenantID:     member,
			BankID:       status.BankID,
			LastRoundAt:  status.LastRoundAt,
			LastError:    status.LastError,
			Halted:       status.Halted,
			AggregateNum: status.AggregateFacts,
		})
	}
	writeJSON(w, http.StatusOK, response)
}

// postPersonalAggregateReset clears a halted tenant's circuit breaker.
//
// The job has no off switch, so a reset is the operator's only lever when a
// tenant stops on repeated model failures.
func (h *Handler) postPersonalAggregateReset(w http.ResponseWriter, r *http.Request) {
	if h.personalAbsent(w) {
		return
	}
	if err := h.cfg.Personal.ResetPersonalAggregate(r.Context(), r.PathValue("id")); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reset"})
}

// postPersonalAggregateRebuild asks the next round to ignore its watermarks.
func (h *Handler) postPersonalAggregateRebuild(w http.ResponseWriter, r *http.Request) {
	if h.personalAbsent(w) {
		return
	}
	if err := h.cfg.Personal.RebuildPersonalAggregate(r.Context(), r.PathValue("id")); err != nil {
		writeRegistryError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "rebuild_requested"})
}

// personalAbsent answers the 501 the aggregate routes share when the deployment
// has no aggregate store, and reports whether it handled the request.
func (h *Handler) personalAbsent(w http.ResponseWriter) bool {
	if h.cfg.Personal == nil {
		writeError(w, http.StatusNotImplemented, "the personal aggregate is not configured on this deployment")
		return true
	}
	return false
}
