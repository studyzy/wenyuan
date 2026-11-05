package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/projectmerge"
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
//
// It holds no model: a round copies the members' facts, so the merge's progress
// does not depend on a chat provider being reachable. That dependency is what
// stalled a project for hours while its model calls failed.
type mergeRunner struct {
	merges storage.MergeStore
	store  storage.Store
	cfg    config.MaintenanceConfig
}

// runProjectMerges brings every project up to date, one round after another.
//
// A round is tens of milliseconds: it reads a batch, writes it, commits. Waiting
// the job interval between rounds would leave the pass idle 99% of the time and
// make a member's backlog take hours instead of minutes, so a project keeps
// running rounds until it returns nothing left to carry or the tick's budget runs
// out. The budget is what keeps one project from starving the rest.
func (r *mergeRunner) runProjectMerges(ctx context.Context) error {
	projects, err := r.merges.ListMergeProjects(ctx)
	if err != nil {
		return fmt.Errorf("discover merge projects: %w", err)
	}
	deadline := roundDeadline(r.cfg.ProjectMergeInterval)
	budgetCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()
	for _, project := range projects {
		if err := budgetCtx.Err(); err != nil {
			return nil
		}
		// One project's failure must not stop the rest: the others still have
		// contributions waiting, and this one is retried next tick.
		if err := r.runProjectUntilCaughtUp(budgetCtx, project, deadline); err != nil {
			slog.Default().Warn("project merge round failed",
				"team_id", project.TeamID, "project_id", project.ProjectID, "error", err)
		}
	}
	return nil
}

// runProjectUntilCaughtUp runs rounds for one project until it has nothing left.
//
// A round reports whether it consumed a full batch; a short batch is the end of
// what the project holds, and stopping there is what keeps an idle project from
// spending its tick on empty rounds.
func (r *mergeRunner) runProjectUntilCaughtUp(ctx context.Context, project storage.MergeProject, deadline time.Duration) error {
	// The round cap is a safety net, not a budget: a round that advances the
	// cursor ends the loop long before it, and the cap only stops a store whose
	// cursor does not move from spinning forever inside one tick.
	maxRounds := 1000
	for round := 0; round < maxRounds; round++ {
		if err := ctx.Err(); err != nil {
			// The tick's budget is spent; the next tick continues from the cursor
			// this pass left behind.
			return nil
		}
		more, err := r.runMergeRound(ctx, project, deadline)
		if err != nil {
			return err
		}
		if !more {
			return nil
		}
	}
	slog.Default().Warn("project merge round cap reached; the project continues next tick",
		"team_id", project.TeamID, "project_id", project.ProjectID, "rounds", maxRounds)
	return nil
}

// runMergeRound runs one round for one project, reporting whether it filled its
// batch — that is, whether more work is waiting behind it.
func (r *mergeRunner) runMergeRound(
	ctx context.Context, project storage.MergeProject, deadline time.Duration,
) (more bool, err error) {
	merges, cfg := r.merges, r.cfg
	roundCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	var input storage.MergeStanzaInput
	var stamp time.Time
	ran, err := merges.BeginMergeStanza(roundCtx, project.TeamID, project.ProjectID,
		storage.MergeRoundOptions{
			Batch:             cfg.ProjectMergeBatchSize,
			StaleAfter:        deadline,
			IncludeExperience: cfg.ProjectMergeIncludeExperience,
			FailThreshold:     cfg.ProjectMergeFailThreshold,
			RecoveryInterval:  cfg.ProjectMergeRecovery,
		},
		func(stanza storage.MergeStanza) error {
			input = stanza.Input()
			if claimer, ok := stanza.(interface{ ClaimStamp() time.Time }); ok {
				stamp = claimer.ClaimStamp()
			}
			return nil
		})
	if err != nil {
		return false, err
	}
	if !ran {
		// Another process is merging this project, or it is halted.
		return false, nil
	}

	plan, err := planRound(project, input, cfg)
	if err != nil {
		// A refusal is recorded, not counted: the round did not fail, it found
		// the project outside what a round may do.
		return false, merges.AbandonMergeStanza(ctx, project.TeamID, project.ProjectID,
			storage.MergeStateFailure{Error: err.Error()})
	}
	if plan.skip {
		return false, merges.AbandonMergeStanza(ctx, project.TeamID, project.ProjectID,
			storage.MergeStateFailure{Error: plan.reason})
	}
	if !plan.merge {
		// Nothing to carry: record the heartbeat and write nothing.
		return false, merges.AbandonMergeStanza(ctx, project.TeamID, project.ProjectID, storage.MergeStateFailure{})
	}

	req := buildFinishRequest(project, input, plan, stamp)
	if err := merges.FinishMergeStanza(roundCtx, req); err != nil {
		if err == storage.ErrMergeClaimLost {
			// Another process took the round over; its result is the one that
			// counts, and this round's failure would be a lie.
			return false, nil
		}
		return false, err
	}
	if len(req.Replace.Insert) > 0 || req.Reconcile {
		r.enqueueTeamConsolidation(ctx, project)
	}
	// A full batch means the member's bank held at least that many facts; the next
	// round continues from the cursor and may find more.
	return len(plan.fresh) >= cfg.ProjectMergeBatchSize, nil
}

// roundPlan is what one round will do.
type roundPlan struct {
	// merge reports whether a model call is needed at all.
	merge bool
	// skip reports the project must not merge at all this round.
	skip   bool
	reason string
	// present is the team content the dedupe pass compares against.
	present []projectmerge.TeamFact
	// fresh is the new input, in a stable order.
	fresh []projectmerge.MemberFact
	// links are the links between the facts this round read, endpoints named
	// by merge key so the write can resolve them to the team bank.
	links []storage.MergeLink
	// cursor maps each member to the position its consumed facts reach.
	cursor map[string]storage.MergeCursor
	// rebuild reports the round acts as a rebuild rather than an incremental
	// round: it replays the members' facts from the beginning.
	rebuild bool
	// rebuildDone reports the replay reached the end of every member's bank, so
	// the flag can be cleared. A rebuild over more facts than one batch spans
	// takes several rounds, and clearing the flag on the first would leave the
	// remainder unread — the segment boundary is what a partial replay cannot
	// represent, because a later incremental round resumes at the cursor.
	rebuildDone bool
}

// planRound decides what this round does, without calling the model.
func planRound(project storage.MergeProject, input storage.MergeStanzaInput, cfg config.MaintenanceConfig) (roundPlan, error) {
	plan := roundPlan{cursor: map[string]storage.MergeCursor{}}
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
				FactType: string(fact.FactType), Tags: fact.Tags,
			})
			total++
		}
		plan.links = append(plan.links, member.Links...)
	}
	plan.rebuild = rebuild
	// The team's previous content is what the pass compares against and, on a
	// rebuild, what it must reconcile with: every previous row is removed unless a
	// member still contributes its text. Both rounds therefore read it — what
	// differs is what the write does with it, not whether it is seen.
	for _, fact := range input.Previous {
		plan.present = append(plan.present, projectmerge.TeamFact{
			ID: fact.ID, Text: fact.Text, MergeKey: fact.MergeKey,
			FactType: string(fact.FactType),
		})
	}
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
	} else if len(plan.present) > cfg.ProjectMergeMaxTeamFacts {
		// An oversized project is a capacity problem, not a privacy one: skip
		// rather than fail closed, because nothing a member deleted is at stake.
		plan.skip = true
		plan.reason = fmt.Sprintf(
			"team content of %d facts exceeds the %d-fact limit; raise %s or split the project",
			len(plan.present), cfg.ProjectMergeMaxTeamFacts, config.EnvProjectMergeMaxTeamFacts)
		return plan, nil
	}
	plan.merge = len(plan.fresh) > 0 || rebuild
	if !plan.merge {
		return plan, nil
	}
	// A rebuild completes only when no member filled its batch: a member whose
	// read hit the batch limit may still hold facts the next round must replay.
	plan.rebuildDone = true
	for _, member := range input.Members {
		if !member.BankExists {
			continue
		}
		if len(member.Fresh) >= cfg.ProjectMergeBatchSize {
			plan.rebuildDone = false
		}
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

// buildFinishRequest turns the round's carry-over into the store's write.
//
// The write is the members' facts themselves: each fresh fact becomes one team
// fact under its own text and kind. What the model contributed — the duplicate
// labels — lands in metadata, never in the text, so a wrong label costs a
// misleading pointer and cannot lose a fact.
//
// A fact whose text is already in the team bank keeps the existing row rather
// than being inserted twice, which is what makes a repeated round idempotent and
// keeps the row's observations attached.
func buildFinishRequest(
	project storage.MergeProject,
	input storage.MergeStanzaInput,
	plan roundPlan,
	stamp time.Time,
) storage.FinishMergeRequest {
	planned := projectmerge.PlanBatch(plan.present, plan.fresh)

	// PlanBatch already dropped every fact the bank holds and every repeat inside
	// the batch, so what is left is exactly what needs a row.
	insert := make([]storage.TeamFactRow, 0, len(planned.Facts))
	sources := make(map[string][]storage.MergeSourceRow, len(input.Members))
	for _, plannedFact := range planned.Facts {
		key := projectmerge.MergeKey(plannedFact.Fact.Text)
		insert = append(insert, storage.TeamFactRow{
			Text:     plannedFact.Fact.Text,
			MergeKey: key,
			FactType: storage.MemoryUnitType(plannedFact.Fact.FactType),
			Tags:     plannedFact.Fact.Tags,
		})
	}
	// Every member's fact is a ledger row, whether or not the bank needed a new
	// row for its text: provenance is per member, and delete detection reads it per
	// member. A fact two members both hold therefore supports the one row twice,
	// once under each member.
	for _, fact := range plan.fresh {
		key := projectmerge.MergeKey(fact.Text)
		sources[fact.TenantID] = append(sources[fact.TenantID],
			storage.MergeSourceRow{UnitID: fact.UnitID, MergeKey: key})
	}

	req := storage.FinishMergeRequest{
		TeamID:     project.TeamID,
		ProjectID:  project.ProjectID,
		TeamSchema: project.TeamSchema,
		BankID:     project.BankID,
		Stamp:      stamp,
		Replace: storage.MergeReplaceResult{
			Insert:  insert,
			Links:   plan.links,
			Sources: sources,
		},
		Cursors:        plan.cursor,
		Rebuild:        plan.rebuild,
		RebuildDone:    plan.rebuildDone,
		Reconcile:      plan.rebuild && plan.rebuildDone,
		UnmanagedFacts: input.UnmanagedFacts,
	}
	for _, member := range input.Members {
		req.Members = append(req.Members, member.Member.TenantID)
	}
	return req
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
func mergeEnabled(cfg config.Config, merges storage.MergeStore) bool {
	// No provider check: the round copies facts and calls no model, so a
	// deployment with no chat provider still shares memory between members.
	return merges != nil && cfg.TenantMode == config.TenantModeMulti
}
