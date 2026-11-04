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

	answers, err := dedupeWithModel(roundCtx, model, plan.present, plan.fresh)
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
	// present is the team content the dedupe pass compares against.
	present []projectmerge.TeamFact
	// fresh is the new input, in a stable order.
	fresh []projectmerge.MemberFact
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
				FactType: string(fact.FactType),
			})
			total++
		}
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

// dedupeWithModel asks the model which of this round's facts repeat an existing
// one.
//
// It never asks the model to produce the team's content. Every member fact is
// written verbatim (see projectmerge.PlanBatch); the model only labels duplicates,
// a judgement it can make without being able to lose a fact.
func dedupeWithModel(ctx context.Context, model provider.Provider, present []projectmerge.TeamFact,
	fresh []projectmerge.MemberFact) ([]projectmerge.Answer, error) {
	messages := projectmerge.Prompt(present, fresh)
	result, err := model.Complete(ctx, provider.CompletionRequest{Messages: messages})
	if err != nil {
		return nil, fmt.Errorf("dedupe model call: %w", err)
	}
	answers, err := projectmerge.ParseAnswer(result.Content, len(fresh), len(present))
	if err != nil {
		return nil, err
	}
	return answers, nil
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
	answers []projectmerge.Answer,
	stamp time.Time,
) storage.FinishMergeRequest {
	planned := projectmerge.PlanBatch(
		projectmerge.Batch{Present: plan.present, Fresh: plan.fresh}, answers)

	// The keys already in the bank: a fact whose key is here is the row the round
	// already has, so it is neither inserted again nor removed.
	existing := make(map[string]bool, len(plan.present))
	for _, fact := range plan.present {
		existing[fact.MergeKey] = true
	}
	// A rebuild reconciles the bank against what the members hold now, so the
	// previous rows are removed and re-inserted only for facts a member still
	// contributes. An incremental round removes nothing on this basis: the pass is
	// additive, so a fact no member restates stays where it is.
	//
	// The removal is computed only on the round that completes the rebuild, and
	// only over the rows this round actually compared against: plan.present is
	// bounded by the batch, so rows older than that window are never candidates.
	// A rebuild spanning several rounds sees one batch of the members' banks at a
	// time, so removing every row a batch does not reproduce would delete the
	// content of the batches not yet reached.
	remove := make([]storage.TeamFactRow, 0)
	if plan.rebuild && plan.rebuildDone {
		for _, fact := range plan.present {
			remove = append(remove, storage.TeamFactRow{
				ID: fact.ID, Text: fact.Text, MergeKey: fact.MergeKey,
				FactType: storage.MemoryUnitType(fact.FactType),
			})
		}
	}

	insert := make([]storage.TeamFactRow, 0, len(planned.Facts))
	sources := make(map[string][]storage.MergeSourceRow, len(input.Members))
	written := make(map[string]bool, len(planned.Facts))
	for _, plannedFact := range planned.Facts {
		key := projectmerge.MergeKey(plannedFact.Fact.Text)
		source := storage.MergeSourceRow{
			UnitID: plannedFact.Fact.UnitID, MergeKey: key,
			DuplicateOf: plannedFact.DuplicateOf,
		}
		sources[plannedFact.Fact.TenantID] = append(sources[plannedFact.Fact.TenantID], source)
		if written[key] {
			// Two members contributed the same text; the bank holds it once.
			continue
		}
		written[key] = true
		if existing[key] {
			// The bank already holds this text, so its row stays exactly where it
			// is — same id, same observations. Inserting a second row for the same
			// fact would double the bank and orphan whatever hung off the first.
			continue
		}
		insert = append(insert, storage.TeamFactRow{
			Text: plannedFact.Fact.Text, MergeKey: key,
			FactType:    storage.MemoryUnitType(plannedFact.Fact.FactType),
			DuplicateOf: plannedFact.DuplicateOf,
		})
	}
	if plan.rebuild {
		// A rebuild removes the rows no member contributes any more: `written`
		// holds exactly the texts this round replays, so everything else in
		// `existing` is a row nothing still supports.
		remove = prunedRemovals(remove, written)
	}

	req := storage.FinishMergeRequest{
		TeamID:     project.TeamID,
		ProjectID:  project.ProjectID,
		TeamSchema: project.TeamSchema,
		BankID:     project.BankID,
		Stamp:      stamp,
		Replace: storage.MergeReplaceResult{
			Remove:  remove,
			Insert:  insert,
			Sources: sources,
		},
		Cursors:        plan.cursor,
		Rebuild:        plan.rebuild,
		RebuildDone:    plan.rebuildDone,
		UnmanagedFacts: input.UnmanagedFacts,
	}
	for _, member := range input.Members {
		if plan.rebuild && member.BankExists {
			req.Replace.RebuildMembers = append(req.Replace.RebuildMembers, member.Member.TenantID)
		}
		req.Members = append(req.Members, member.Member.TenantID)
	}
	return req
}

// prunedRemovals drops the removals whose text is still present in the bank: a
// rebuild removes only the rows no member contributes any more, so whatever hangs
// off a surviving row keeps it.
func prunedRemovals(remove []storage.TeamFactRow, kept map[string]bool) []storage.TeamFactRow {
	out := make([]storage.TeamFactRow, 0, len(remove))
	for _, fact := range remove {
		if kept[fact.MergeKey] {
			continue
		}
		out = append(out, fact)
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
