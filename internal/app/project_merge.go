package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/projectmerge"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The project merge job: it turns the members' facts about one project into the
// team's shared content.
//
// A round runs in two transactions with the model call between them. The first
// takes the project's claim and reads what the round needs; the model call then
// happens outside any transaction, because a slow model holding a database
// transaction would block the members writing to their own banks; the second
// writes the result, fenced on the claim stamp so a round that another process
// took over cannot apply a stale answer.

// roundDeadline bounds one project's whole round. It is the merge lock's staleness
// threshold: a claim older than this is a crashed round and is taken over.
func roundDeadline(interval time.Duration) time.Duration {
	deadline := 10 * interval
	if deadline > 5*time.Minute {
		deadline = 5 * time.Minute
	}
	if deadline < time.Minute {
		deadline = time.Minute
	}
	return deadline
}

// mergeRunner carries what one tick's rounds need.
type mergeRunner struct {
	merges storage.MergeStore
	store  storage.Store
	model  provider.Provider
	cfg    config.MaintenanceConfig
}

// runProjectMerges runs one round for every project that needs one.
func (r *mergeRunner) runProjectMerges(ctx context.Context) error {
	projects, err := r.merges.ListMergeProjects(ctx)
	if err != nil {
		return fmt.Errorf("discover merge projects: %w", err)
	}
	deadline := roundDeadline(r.cfg.ProjectMergeInterval)
	for _, project := range projects {
		if err := ctx.Err(); err != nil {
			return err
		}
		// One project's failure must not stop the rest: the others still have
		// contributions waiting, and this one is retried next tick.
		if err := r.runMergeRound(ctx, project, deadline); err != nil {
			slog.Default().Warn("project merge round failed",
				"team_id", project.TeamID, "project_id", project.ProjectID, "error", err)
		}
	}
	return nil
}

// runMergeRound runs one round for one project.
func (r *mergeRunner) runMergeRound(ctx context.Context, project storage.MergeProject, deadline time.Duration) error {
	merges, model, cfg := r.merges, r.model, r.cfg
	roundCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var input storage.MergeStanzaInput
	var stamp time.Time
	ran, err := merges.BeginMergeStanza(roundCtx, project.TeamID, project.ProjectID,
		cfg.ProjectMergeBatchSize, deadline, func(stanza storage.MergeStanza) error {
			input = stanza.Input()
			if claimer, ok := stanza.(interface{ ClaimStamp() time.Time }); ok {
				stamp = claimer.ClaimStamp()
			}
			return nil
		})
	if err != nil {
		return err
	}
	if !ran {
		// Another process is merging this project, or it is halted.
		return nil
	}

	plan, err := planRound(project, input, cfg)
	if err != nil {
		// A refusal is recorded, not counted: the round did not fail, it found
		// the project outside what a round may do.
		return merges.AbandonMergeStanza(ctx, project.TeamID, project.ProjectID,
			storage.MergeStateFailure{Error: err.Error()})
	}
	if plan.skip {
		return merges.AbandonMergeStanza(ctx, project.TeamID, project.ProjectID,
			storage.MergeStateFailure{Error: plan.reason})
	}
	if !plan.merge {
		// Nothing new and no stale content: record the heartbeat and spend no
		// model call.
		return merges.AbandonMergeStanza(ctx, project.TeamID, project.ProjectID, storage.MergeStateFailure{})
	}

	answers, err := mergeWithModel(roundCtx, model, plan)
	if err != nil {
		return merges.AbandonMergeStanza(ctx, project.TeamID, project.ProjectID,
			storage.MergeStateFailure{Error: err.Error(), Counting: true})
	}
	req := buildFinishRequest(project, input, plan, answers, stamp)
	if err := merges.FinishMergeStanza(roundCtx, req); err != nil {
		if err == storage.ErrMergeClaimLost {
			// Another process took the round over; its result is the one that
			// counts, and this round's failure would be a lie.
			return nil
		}
		return err
	}
	if len(req.Replace.Insert) > 0 || len(req.Replace.Remove) > 0 {
		r.enqueueTeamConsolidation(ctx, project)
	}
	return nil
}

// roundPlan is what one round will do.
type roundPlan struct {
	// merge reports whether a model call is needed at all.
	merge bool
	// skip reports the project must not merge at all this round.
	skip   bool
	reason string
	// previous is the team content carried into the prompt.
	previous []projectmerge.TeamFact
	// fresh is the new input, in a stable order.
	fresh []projectmerge.MemberFact
	// cursor maps each member to the position its consumed facts reach.
	cursor map[string]storage.MergeCursor
	// rebuild reports the round recomputes the whole project.
	rebuild bool
}

// planRound decides what this round does, without calling the model.
func planRound(project storage.MergeProject, input storage.MergeStanzaInput, cfg config.MaintenanceConfig) (roundPlan, error) {
	plan := roundPlan{cursor: map[string]storage.MergeCursor{}}
	for _, fact := range input.Previous {
		plan.previous = append(plan.previous, projectmerge.TeamFact{
			ID: fact.ID, Text: fact.Text, MergeKey: fact.MergeKey,
		})
	}
	rebuild := false
	total := 0
	for _, member := range input.Members {
		if !member.BankExists {
			// A member with no bank yet has contributed nothing; the team's
			// content is kept rather than rebuilt (the onboarding path).
			continue
		}
		if member.Member.State.RebuildRequested {
			rebuild = true
		}
		if member.Deleted {
			rebuild = true
		}
		for _, fact := range member.Fresh {
			plan.fresh = append(plan.fresh, projectmerge.MemberFact{
				TenantID: fact.TenantID, UnitID: fact.UnitID, Text: fact.Text,
			})
			total++
		}
	}
	plan.rebuild = rebuild
	if rebuild {
		// A rebuild reads every member's facts rather than the delta, so its
		// input is bounded separately and failing closed is the right answer
		// when it will not fit: leaving deleted facts in the team's content is
		// the thing the rebuild exists to prevent.
		if total > cfg.ProjectMergeRebuildMaxUnits {
			return plan, fmt.Errorf(
				"rebuild input of %d units exceeds the %d-unit limit; raise %s or split the project",
				total, cfg.ProjectMergeRebuildMaxUnits, config.EnvProjectMergeRebuildMaxUnits)
		}
	} else if len(plan.previous) > cfg.ProjectMergeMaxTeamFacts {
		// An oversized project is a capacity problem, not a privacy one: skip
		// rather than fail closed, because nothing a member deleted is at stake.
		plan.skip = true
		plan.reason = fmt.Sprintf(
			"team content of %d facts exceeds the %d-fact limit; raise %s or split the project",
			len(plan.previous), cfg.ProjectMergeMaxTeamFacts, config.EnvProjectMergeMaxTeamFacts)
		return plan, nil
	}
	plan.merge = len(plan.fresh) > 0 || rebuild
	if !plan.merge {
		return plan, nil
	}
	// The cursor advances to the last consumed (updated_at, id) per member, so a
	// fact not reached this round is read next round rather than skipped.
	for _, member := range input.Members {
		consumed := len(member.Fresh)
		if consumed == 0 {
			continue
		}
		last := member.Fresh[consumed-1]
		plan.cursor[member.Member.TenantID] = storage.MergeCursor{
			UpdatedAt: last.UpdatedAt,
			UnitID:    last.UnitID,
		}
	}
	return plan, nil
}

// mergeWithModel asks the model for the project's new content.
func mergeWithModel(ctx context.Context, model provider.Provider, plan roundPlan) ([]projectmerge.Answer, error) {
	messages := projectmerge.Prompt("", plan.previous, plan.fresh)
	result, err := model.Complete(ctx, provider.CompletionRequest{Messages: messages})
	if err != nil {
		return nil, fmt.Errorf("merge model call: %w", err)
	}
	answers, err := projectmerge.ParseAnswer(result.Content, len(plan.fresh))
	if err != nil {
		return nil, err
	}
	return answers, nil
}

// buildFinishRequest turns the round's answer into the store's write.
func buildFinishRequest(
	project storage.MergeProject,
	input storage.MergeStanzaInput,
	plan roundPlan,
	answers []projectmerge.Answer,
	stamp time.Time,
) storage.FinishMergeRequest {
	diff := projectmerge.Diff(plan.previous, answers)
	sourceKeys := diff.SourceMergeKeys(len(plan.fresh))

	// The ledger is keyed by the member a source came from, and the fresh slice
	// is ordered member by member, so each index maps back to its owner.
	sources := make(map[string][]storage.MergeSourceRow, len(input.Members))
	for index, fact := range plan.fresh {
		for _, key := range sourceKeys[index] {
			sources[fact.TenantID] = append(sources[fact.TenantID],
				storage.MergeSourceRow{UnitID: fact.UnitID, MergeKey: key})
		}
	}

	req := storage.FinishMergeRequest{
		TeamID:     project.TeamID,
		ProjectID:  project.ProjectID,
		TeamSchema: project.TeamSchema,
		BankID:     project.BankID,
		Stamp:      stamp,
		Replace: storage.MergeReplaceResult{
			Remove:  toFactRows(diff.Remove),
			Insert:  toFactRows(diff.Insert),
			Sources: sources,
		},
		Cursors: plan.cursor,
		Rebuild: plan.rebuild,
	}
	if plan.rebuild {
		// A rebuild recomputes everything from the members' current facts, so
		// the ledger for each contributing member is replaced rather than
		// appended to: a row left from the old content would keep claiming a
		// provenance the new content no longer has.
		for _, member := range input.Members {
			if member.BankExists {
				req.Replace.RebuildMembers = append(req.Replace.RebuildMembers, member.Member.TenantID)
			}
			req.Members = append(req.Members, member.Member.TenantID)
		}
	} else {
		for _, member := range input.Members {
			req.Members = append(req.Members, member.Member.TenantID)
		}
	}
	return req
}

// toFactRows converts an engine fact to the store's row shape.
func toFactRows(facts []projectmerge.TeamFact) []storage.TeamFactRow {
	if len(facts) == 0 {
		return nil
	}
	out := make([]storage.TeamFactRow, 0, len(facts))
	for _, fact := range facts {
		out = append(out, storage.TeamFactRow{ID: fact.ID, Text: fact.Text, MergeKey: fact.MergeKey})
	}
	return out
}

// enqueueTeamConsolidation asks for a consolidation on the team bank, so the
// team's observations follow its facts.
//
// It is best-effort: the facts are already committed, and the consolidation
// reconciler sweeps stranded facts on its own, so a failure here delays the
// observations rather than losing them.
func (r *mergeRunner) enqueueTeamConsolidation(ctx context.Context, project storage.MergeProject) {
	if r.store == nil {
		return
	}
	teamCtx, err := storage.WithSchema(ctx, project.TeamSchema)
	if err != nil {
		return
	}
	enqueuer, ok := r.store.(storage.OperationEnqueuer)
	if !ok {
		return
	}
	if _, err := enqueuer.EnqueueOperation(teamCtx, storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           project.BankID,
			OperationType:    storage.OperationTypeConsolidate,
			SerializationKey: storage.ConsolidationSerializationKey(project.BankID),
		},
	}); err != nil {
		slog.Default().Warn("could not schedule consolidation of merged team content",
			"bank_id", project.BankID, "team_id", project.TeamID, "error", err)
	}
}

// mergeEnabled reports whether the deployment can merge at all.
//
// A single-tenant process has no merge store, and a deployment with no chat
// provider would only write an error every round — the same reason reflect
// refuses without a model.
func mergeEnabled(cfg config.Config, merges storage.MergeStore, model provider.Provider) bool {
	if merges == nil || cfg.TenantMode != config.TenantModeMulti {
		return false
	}
	if model == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(model.Name())) {
	case "", "none", "noop", "noop-provider":
		return false
	}
	return true
}
