package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/personalrollup"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The personal rollup job: it projects every world and experience fact of the
// banks one person owns into that person's aggregate bank, `<tenant>::myself`.
//
// A row of the aggregate bank is a verbatim copy of exactly one source fact. The
// job spends no model call: consolidation reads those projected rows exactly as
// it would read any bank's own memories, and synthesizes the observations from
// them. That is why the aggregate is a materialized view rather than a summary:
// a fact edited in its source bank replaces its row, and the observation resting
// on it is rebuilt by the next consolidation rather than by this job.
//
// A round runs in two transactions with nothing between them. The first takes
// the tenant's claim and reads the watermark's worth of new facts; the second
// writes the result, fenced on the claim stamp so a round that another process
// took over cannot apply a stale answer. There is no switch: a deployment with
// a chat model projects in either tenant mode, single-tenant or not.

// personalRoundDeadline bounds one tenant's whole round. It is also the claim's
// staleness threshold: a claim older than this is a crashed round and is taken
// over.
func personalRoundDeadline(interval time.Duration) time.Duration {
	deadline := 2 * interval
	if deadline < 5*time.Minute {
		deadline = 5 * time.Minute
	}
	if deadline > 30*time.Minute {
		deadline = 30 * time.Minute
	}
	return deadline
}

// personalRunner carries what one tick's rounds need.
type personalRunner struct {
	store storage.PersonalStore
	// models resolves the chat model for a tenant. The projection itself calls no
	// model, but a deployment without one cannot consolidate the projected rows
	// into observations, so a round is still gated on a usable model.
	models func(tenantID string) provider.Provider
	cfg    config.MaintenanceConfig
}

// runPersonalRounds runs one round for every personal tenant that needs one.
func (r *personalRunner) runPersonalRounds(ctx context.Context) error {
	tenants, err := r.store.ListRollupTenants(ctx)
	if err != nil {
		return fmt.Errorf("discover aggregate tenants: %w", err)
	}
	deadline := personalRoundDeadline(r.cfg.PersonalRollupInterval)
	for _, one := range tenants {
		if err := ctx.Err(); err != nil {
			return err
		}
		// One tenant's failure must not stop the rest: the others still have
		// contributions waiting, and this one is retried next tick.
		if err := r.runPersonalRound(ctx, one, deadline); err != nil {
			slog.Default().Warn("personal rollup round failed", "tenant_id", one.ID, "error", err)
		}
	}
	return nil
}

// runPersonalRound runs one round for one tenant.
func (r *personalRunner) runPersonalRound(ctx context.Context, one tenant.Tenant, deadline time.Duration) error {
	roundCtx, cancel := context.WithTimeout(ctx, deadline)
	defer cancel()

	model := r.model(one.ID)
	if !usableModel(model) {
		return nil
	}

	var input storage.PersonalRoundInput
	var stamp time.Time
	ran, err := r.store.BeginPersonalRound(roundCtx, one.ID, r.cfg.PersonalRollupBatchSize, deadline,
		func(read storage.PersonalRoundInput, claim time.Time) error {
			input = read
			stamp = claim
			return nil
		})
	if err != nil {
		return err
	}
	if !ran {
		// Another process holds the tenant, or its breaker is open.
		return nil
	}

	plan, err := planPersonalRound(input, r.cfg)
	if err != nil {
		// A refusal is recorded, not counted: the round did not fail, it found the
		// tenant outside what a round may do.
		return r.store.AbandonPersonalRound(ctx, one.ID, storage.PersonalFailure{Error: err.Error()})
	}
	if !plan.merge {
		// Nothing new and nothing stale: record the heartbeat, spend no write.
		return r.store.AbandonPersonalRound(ctx, one.ID, storage.PersonalFailure{})
	}

	req := buildPersonalFinish(input, plan, stamp)
	if err := r.store.FinishPersonalRound(roundCtx, req); err != nil {
		if err == storage.ErrPersonalClaimLost {
			// Another process took the round over; its result is the one that counts.
			return nil
		}
		return err
	}
	if len(req.Insert) > 0 || len(req.Remove) > 0 {
		enqueueAggregateConsolidation(ctx, r.store, input)
	}
	return nil
}

// model returns the chat model for one tenant, or nil when there is none.
func (r *personalRunner) model(tenantID string) provider.Provider {
	if r.models == nil {
		return nil
	}
	return r.models(tenantID)
}

// usableModel reports whether a provider can answer at all. The projection calls
// no model, but consolidation of the projected rows does; without one the
// aggregate would fill with facts that can never become observations.
func usableModel(model provider.Provider) bool {
	if model == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(model.Name())) {
	case "", "none", "noop", "noop-provider":
		return false
	}
	return true
}

// personalPlan is what one round will do.
type personalPlan struct {
	// merge reports whether a write is needed at all.
	merge bool
	// rebuild reports the round recomputed every projected row from scratch.
	rebuild bool
	// diff is the fact-level change the projection produced.
	diff personalrollup.ProjectionDiff
	// cursors maps each consumed bank to the watermark its facts reach.
	cursors map[string]storage.MergeCursor
}

// planPersonalRound decides what this round does. It is a pure function of the
// round's input and the deployment's limits: no model, no prompt, no topic
// vocabulary. A projected row is identified by its source unit, so an edited
// fact replaces its row rather than creating a second one.
func planPersonalRound(input storage.PersonalRoundInput, cfg config.MaintenanceConfig) (personalPlan, error) {
	plan := personalPlan{cursors: map[string]storage.MergeCursor{}}
	previous := make([]personalrollup.ProjectionFact, 0, len(input.Previous))
	for _, fact := range input.Previous {
		previous = append(previous, personalrollup.ProjectionFact{
			ID: fact.ID, Key: fact.MergeKey, Text: fact.Text, FactType: fact.FactType,
			EventDate: fact.EventDate, OccurredStart: fact.OccurredStart,
			OccurredEnd: fact.OccurredEnd, MentionedAt: fact.MentionedAt,
		})
	}
	fresh := make([]personalrollup.ProjectionFact, 0, len(input.Facts))
	for _, fact := range input.Facts {
		fresh = append(fresh, personalrollup.ProjectionFact{
			Key:  personalrollup.ProjectionKey(fact.BankID, fact.UnitID),
			Text: fact.Text, FactType: fact.FactType,
			EventDate: fact.EventDate, OccurredStart: fact.OccurredStart,
			OccurredEnd: fact.OccurredEnd, MentionedAt: fact.MentionedAt,
			BankID: fact.BankID, UnitID: fact.UnitID,
		})
	}
	plan.rebuild = input.RebuildRequested || input.SourceDeleted
	if plan.rebuild {
		// A rebuild reads every source fact, so its input is bounded on its own
		// and failing closed is the right answer when it will not fit: leaving
		// deleted facts projected is the thing the rebuild exists to prevent.
		if len(fresh) > cfg.PersonalRollupRebuildMaxUnits {
			return plan, fmt.Errorf(
				"rebuild input of %d units exceeds the %d-unit limit; raise %s",
				len(fresh), cfg.PersonalRollupRebuildMaxUnits, config.EnvPersonalRollupRebuildMaxUnits)
		}
	}
	plan.diff = personalrollup.DiffProjection(previous, fresh, plan.rebuild)
	plan.merge = len(plan.diff.Insert) > 0 || len(plan.diff.Remove) > 0
	if !plan.merge {
		return plan, nil
	}
	// The watermark advances to the last consumed (updated_at, id) per bank, so a
	// fact not reached this round is read next round rather than skipped.
	for _, fact := range input.Facts {
		cursor := plan.cursors[fact.BankID]
		if !fact.UpdatedAt.After(cursor.UpdatedAt) && cursor.UnitID != "" {
			continue
		}
		plan.cursors[fact.BankID] = storage.MergeCursor{UpdatedAt: fact.UpdatedAt, UnitID: fact.UnitID}
	}
	return plan, nil
}

// buildPersonalFinish turns one round's projection diff into the store's write.
func buildPersonalFinish(
	input storage.PersonalRoundInput,
	plan personalPlan,
	stamp time.Time,
) storage.FinishPersonalRequest {
	remove := make([]storage.PersonalPreviousFact, 0, len(plan.diff.Remove))
	for _, fact := range plan.diff.Remove {
		remove = append(remove, projectionRow(fact))
	}
	insert := make([]storage.PersonalPreviousFact, 0, len(plan.diff.Insert))
	for _, fact := range plan.diff.Insert {
		insert = append(insert, projectionRow(fact))
	}
	sources := make([]storage.PersonalSourceRow, 0, len(plan.diff.Sources))
	factBanks := make(map[string][]string, len(plan.diff.Sources))
	for _, source := range plan.diff.Sources {
		sources = append(sources, storage.PersonalSourceRow{
			UnitID: source.UnitID, MergeKey: source.MergeKey, BankID: source.BankID,
		})
		// The navigation index still links the aggregate to the banks its rows
		// came from; the topic vocabulary, which only the old model call could
		// produce, is left empty until a later round rebuilds it.
		factBanks[source.MergeKey] = appendUniqueBank(factBanks[source.MergeKey], source.BankID)
	}
	return storage.FinishPersonalRequest{
		TenantID:       input.TenantID,
		Schema:         input.Schema,
		BankID:         input.BankID,
		Stamp:          stamp,
		Remove:         remove,
		Insert:         insert,
		Sources:        sources,
		Cursors:        plan.cursors,
		Rebuild:        plan.rebuild,
		FactBanks:      factBanks,
		UnmanagedFacts: input.UnmanagedFacts,
	}
}

// projectionRow converts the engine's fact to the store's row shape.
func projectionRow(fact personalrollup.ProjectionFact) storage.PersonalPreviousFact {
	return storage.PersonalPreviousFact{
		ID: fact.ID, Text: fact.Text, MergeKey: fact.Key, FactType: fact.FactType,
		EventDate: fact.EventDate, OccurredStart: fact.OccurredStart,
		OccurredEnd: fact.OccurredEnd, MentionedAt: fact.MentionedAt,
	}
}

// appendUniqueBank adds a bank to a projection's source list once.
func appendUniqueBank(banks []string, bank string) []string {
	for _, existing := range banks {
		if existing == bank {
			return banks
		}
	}
	return append(banks, bank)
}

// enqueueAggregateConsolidation asks for a consolidation on the aggregate bank,
// so the person's observations follow their projected facts.
//
// It is best-effort: the facts are already committed, and the consolidation
// reconciler sweeps stranded facts on its own.
func enqueueAggregateConsolidation(ctx context.Context, store storage.PersonalStore, input storage.PersonalRoundInput) {
	enqueuer, ok := store.(storage.OperationEnqueuer)
	if !ok {
		return
	}
	schemaCtx, err := storage.WithSchema(ctx, input.Schema)
	if err != nil {
		return
	}
	if _, err := enqueuer.EnqueueOperation(schemaCtx, storage.EnqueueRequest{
		Operation: storage.Operation{
			BankID:           input.BankID,
			OperationType:    storage.OperationTypeConsolidate,
			SerializationKey: storage.ConsolidationSerializationKey(input.BankID),
		},
	}); err != nil {
		slog.Default().Warn("could not schedule consolidation of the aggregate bank",
			"bank_id", input.BankID, "tenant_id", input.TenantID, "error", err)
	}
}

// personalRollupEnabled reports whether this process should start the aggregate
// job at all.
//
// It is not a configuration switch: there is none, and a deployment with a chat
// model projects in either tenant mode. The conditions here are about the
// process — a build or a store without the PostgreSQL driver cannot run the
// round, and a model that cannot answer would leave projected facts that can
// never be consolidated.
func personalRollupEnabled(personals storage.PersonalStore) bool {
	return personals != nil
}
