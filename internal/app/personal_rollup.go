package app

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/personalrollup"
	"github.com/studyzy/wenyuan/internal/projectmerge"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The personal rollup job: it folds every bank one person owns into that person's
// aggregate bank, `<tenant>::myself`.
//
// A round runs in two transactions with the model call between them. The first
// takes the tenant's claim and reads the watermark's worth of new facts; the
// model call then happens outside any transaction, because a slow model holding a
// database transaction would block the person writing to their own banks; the
// second writes the result, fenced on the claim stamp so a round that another
// process took over cannot apply a stale answer.
//
// Unlike the project merge, this job has no switch: a deployment with a chat model
// aggregates, single-tenant or not. The aggregate is not a team feature.

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
	// models resolves the chat model for a tenant. It is a function rather than a
	// provider so tenant-level configuration overrides apply, and so a test can
	// hand a different model per tenant.
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
	if plan.skip {
		return r.store.AbandonPersonalRound(ctx, one.ID, storage.PersonalFailure{Error: plan.reason})
	}
	if !plan.merge {
		// Nothing new and nothing stale: record the heartbeat, spend no model call.
		return r.store.AbandonPersonalRound(ctx, one.ID, storage.PersonalFailure{})
	}

	answers, err := rollupWithModel(roundCtx, model, plan)
	if err != nil {
		return r.store.AbandonPersonalRound(ctx, one.ID,
			storage.PersonalFailure{Error: err.Error(), Counting: true})
	}
	req := buildPersonalFinish(input, plan, answers, stamp)
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

// usableModel reports whether a provider can answer a round at all.
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
	// merge reports whether a model call is needed at all.
	merge bool
	// skip reports the tenant must not merge at all this round.
	skip   bool
	reason string
	// rebuild reports the round recomputes the aggregate from every source fact.
	rebuild bool
	// previous is the aggregate content carried into the prompt.
	previous []personalrollup.PreviousFact
	// fresh is the new input, in a stable order.
	fresh []personalrollup.SourceFact
	// topics is the vocabulary injected into the prompt.
	topics []string
	// cursors maps each consumed bank to the watermark its facts reach.
	cursors map[string]storage.MergeCursor
}

// planPersonalRound decides what this round does, without calling the model.
func planPersonalRound(input storage.PersonalRoundInput, cfg config.MaintenanceConfig) (personalPlan, error) {
	plan := personalPlan{cursors: map[string]storage.MergeCursor{}}
	for _, fact := range input.Previous {
		// A fact with no merge key still carries its text; its key is derived from
		// it. The store already fills the key in, so this only guards a caller
		// that did not.
		key := fact.MergeKey
		if key == "" {
			key = personalrollup.MergeKey(fact.Text)
		}
		plan.previous = append(plan.previous, personalrollup.PreviousFact{
			ID: fact.ID, Text: fact.Text, MergeKey: key,
		})
	}
	plan.rebuild = input.RebuildRequested || input.SourceDeleted
	plan.topics = topicNames(input)
	for _, fact := range input.Facts {
		plan.fresh = append(plan.fresh, personalrollup.SourceFact{
			BankID: fact.BankID, UnitID: fact.UnitID, Text: fact.Text,
		})
	}
	if plan.rebuild {
		// A rebuild reads every source fact, so its input is bounded on its own
		// and failing closed is the right answer when it will not fit: leaving
		// deleted facts in the aggregate is the thing the rebuild exists to
		// prevent.
		if len(plan.fresh) > cfg.PersonalRollupRebuildMaxUnits {
			return plan, fmt.Errorf(
				"rebuild input of %d units exceeds the %d-unit limit; raise %s",
				len(plan.fresh), cfg.PersonalRollupRebuildMaxUnits, config.EnvPersonalRollupRebuildMaxUnits)
		}
	} else if len(plan.previous) > cfg.PersonalRollupMaxTeamFacts {
		// An oversized aggregate is a capacity problem, not a privacy one: skip
		// rather than fail closed, because nothing a person deleted is at stake.
		plan.skip = true
		plan.reason = fmt.Sprintf(
			"aggregate content of %d facts exceeds the %d-fact limit; raise %s",
			len(plan.previous), cfg.PersonalRollupMaxTeamFacts, config.EnvPersonalRollupMaxTeamFacts)
		return plan, nil
	}
	plan.merge = len(plan.fresh) > 0 || plan.rebuild
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

// topicNames lists the vocabulary entries a prompt carries, in reading order.
func topicNames(input storage.PersonalRoundInput) []string {
	seen := make(map[string]bool, len(input.Topics))
	out := make([]string, 0, len(input.Topics))
	for _, topic := range input.Topics {
		name := personalrollup.Normalize(topic.Name)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, topic.Name)
	}
	return out
}

// rollupWithModel asks the model for the person's aggregate content.
func rollupWithModel(ctx context.Context, model provider.Provider, plan personalPlan) ([]personalrollup.Answer, error) {
	messages := personalrollup.Prompt(plan.previous, plan.fresh, plan.topics, personalrollup.PromptOptions{})
	result, err := model.Complete(ctx, provider.CompletionRequest{Messages: messages})
	if err != nil {
		return nil, fmt.Errorf("rollup model call: %w", err)
	}
	answers, err := personalrollup.ParseAnswer(result.Content, len(plan.fresh))
	if err != nil {
		return nil, err
	}
	if len(answers) == 0 && len(plan.previous) > 0 {
		return nil, personalrollup.ErrAnswerEmpty
	}
	return answers, nil
}

// buildPersonalFinish turns the round's answer into the store's write.
func buildPersonalFinish(
	input storage.PersonalRoundInput,
	plan personalPlan,
	answers []personalrollup.Answer,
	stamp time.Time,
) storage.FinishPersonalRequest {
	diff := personalrollup.Diff(plan.previous, answers)
	sourceKeys := diff.SourceMergeKeys(len(plan.fresh))

	sources := make([]storage.PersonalSourceRow, 0, len(sourceKeys))
	for _, index := range sortedIntKeys(sourceKeys) {
		fact := plan.fresh[index]
		for _, key := range sourceKeys[index] {
			sources = append(sources, storage.PersonalSourceRow{
				UnitID: fact.UnitID, MergeKey: key, BankID: fact.BankID,
			})
		}
	}
	topics := make([]storage.PersonalTopic, 0, len(diff.Topics))
	for key, names := range diff.Topics {
		banks := personalrollup.BanksByKey(sourceKeys, plan.fresh)[key]
		if len(banks) == 0 {
			// A fact carried over on wording alone keeps no source this round; it
			// must not claim a bank it cannot name.
			continue
		}
		for _, name := range names {
			for _, bank := range banks {
				topics = append(topics, storage.PersonalTopic{Name: name, BankID: bank, MergeKey: key})
			}
		}
	}
	return storage.FinishPersonalRequest{
		TenantID:       input.TenantID,
		Schema:         input.Schema,
		BankID:         input.BankID,
		Stamp:          stamp,
		Remove:         toPersonalFacts(diff.Remove),
		Insert:         toPersonalFacts(diff.Insert),
		Sources:        sources,
		Cursors:        plan.cursors,
		Rebuild:        plan.rebuild,
		Topics:         topics,
		FactBanks:      personalrollup.BanksByKey(sourceKeys, plan.fresh),
		UnmanagedFacts: input.UnmanagedFacts,
	}
}

// toPersonalFacts converts the engine's facts to the store's row shape.
func toPersonalFacts(facts []projectmerge.TeamFact) []storage.PersonalPreviousFact {
	if len(facts) == 0 {
		return nil
	}
	out := make([]storage.PersonalPreviousFact, 0, len(facts))
	for _, fact := range facts {
		out = append(out, storage.PersonalPreviousFact{ID: fact.ID, Text: fact.Text, MergeKey: fact.MergeKey})
	}
	return out
}

// sortedIntKeys returns a map's integer keys in ascending order.
//
// The batch's source indexes are walked in order so the ledger rows, and the
// prompt they came from, are produced the same way twice.
func sortedIntKeys[V any](values map[int]V) []int {
	keys := make([]int, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortInts(keys)
	return keys
}

// sortInts orders integers ascending, in one place so the call sites stay short.
func sortInts(values []int) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

// enqueueAggregateConsolidation asks for a consolidation on the aggregate bank,
// so the person's observations follow their aggregate facts.
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
// model aggregates in either tenant mode. The two conditions here are about the
// process, not the deployment — a build or a store without the PostgreSQL driver
// cannot run the round, and a model that cannot answer would only record an error
// every round. A round that does meet the wiring still checks its model itself, so
// a deployment that resolves the chat model per tenant (the model factory below)
// is not turned off by the process-wide default.
func personalRollupEnabled(personals storage.PersonalStore) bool {
	return personals != nil
}
