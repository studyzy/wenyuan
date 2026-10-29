// Package consolidation turns raw facts into consolidated observations.
//
// A consolidation pass reads the facts of a bank that have not yet been folded
// into an observation, pools the observations that already exist, and asks the
// model to decide which facts support an existing observation, which form a new
// one, and which supersede one outright. The model answers with three arrays —
// creates, updates, deletes — and the pass applies them to storage.
//
// Observations are memory units of type "observation", so a recall can rank
// them beside the raw facts they summarize, and each is linked to the facts it
// rests on through the observation-source table. This is distinct from a mental
// model, which is a user-defined query refreshed on demand rather than a
// conclusion the engine derives bottom-up.
package consolidation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/history"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ErrNoModel reports a consolidation pass attempted with no model configured.
// The pass is model-driven; without one there is nothing to decide what follows
// from the facts.
var ErrNoModel = errors.New("consolidation requires a configured model")

// DefaultMaxFacts bounds how many facts one pass considers. A pass that read
// every fact of a large bank would exceed the context window and spend the
// caller's budget for no better decision, so the newest facts are folded first
// and the rest wait for the next pass.
const DefaultMaxFacts = 200

// DefaultMaxObservations bounds the pool of existing observations offered as
// merge candidates. Newer observations are the likelier matches.
const DefaultMaxObservations = 100

// Fact is one raw memory unit offered as new evidence.
type Fact struct {
	ID            string
	Text          string
	OccurredStart string
	OccurredEnd   string
	MentionedAt   string
	// Tags are the fact's own tags, the scope it folds under by default.
	Tags []string
	// ObservationScopes is the fact's raw observation_scopes request, resolved
	// into concrete scopes by resolveScopes.
	ObservationScopes json.RawMessage
}

// Observation is one existing consolidated conclusion offered as a merge
// candidate, or produced by the pass.
type Observation struct {
	ID         string
	Text       string
	ProofCount int
	SourceIDs  []string
	// SourceTexts are the texts of the facts this observation rests on, aligned
	// with SourceIDs where the store supplies them. They are shown to the model
	// as the observation's evidence, bounded by the source-fact token budgets.
	SourceTexts []string
	OccurredAt  string
	MentionedAt string
	// Scope is the tag scope the observation was written at. An empty slice is
	// the untagged (default) scope. Only observations of the pass's scope are
	// offered as merge candidates.
	Scope []string
	// Embedding is the observation's stored vector, when the store supplies one,
	// so the pass can decide near-duplicates by cosine similarity against the
	// configured dedup threshold. Empty when the deployment has no embedding
	// model, which leaves the pass on its token-similarity floor.
	Embedding []float32
}

// Create is a new observation the model decided to record.
type Create struct {
	Text      string   `json:"text"`
	SourceIDs []string `json:"source_fact_ids"`
	Reason    string   `json:"reason"`
}

// Update rewrites one existing observation and attaches new evidence to it.
type Update struct {
	ObservationID string   `json:"observation_id"`
	Text          string   `json:"text"`
	SourceIDs     []string `json:"source_fact_ids"`
	Reason        string   `json:"reason"`
}

// Delete removes one observation the new facts superseded outright.
type Delete struct {
	ObservationID string `json:"observation_id"`
	Reason        string `json:"reason"`
}

// Decision is one pass's answer.
type Decision struct {
	Creates []Create `json:"creates"`
	Updates []Update `json:"updates"`
	Deletes []Delete `json:"deletes"`
}

// Result reports what a pass applied.
type Result struct {
	Created int
	Updated int
	Deleted int
	// Failed counts facts the model could not be asked about, or whose answer
	// could not be used. They are marked failed for the backlog gauge rather
	// than failing the whole pass.
	Failed int
	// Skipped counts creates and updates dropped as exact duplicates of an
	// existing or already-created observation.
	Skipped int
	Usage   provider.Usage
}

// Store is what the pass needs from storage. It is an interface so the pass can
// be exercised without a database, and so the storage area keeps owning the
// queries.
type Store interface {
	// ConsolidationFacts returns the newest raw facts of a bank that have not
	// yet been folded into an observation.
	ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]Fact, error)
	// ConsolidationObservations returns the bank's existing observations of one
	// scope, newest first, as merge candidates. Facts fold only with facts of
	// the same scope, so only same-scope observations may be merged into.
	ConsolidationObservations(ctx context.Context, bankID string, scope []string, limit int) ([]Observation, error)
	// CreateObservation records a new observation at the given scope, resting
	// on the given facts.
	CreateObservation(ctx context.Context, bankID, text string, sourceIDs, scope []string) (string, error)
	// UpdateObservation rewrites an observation and attaches more evidence.
	UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error
	// DeleteObservation removes one observation.
	DeleteObservation(ctx context.Context, bankID, observationID string) error
	// MarkConsolidated records that the given facts have been folded, so the
	// next pass does not reconsider them.
	MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error
	// MarkConsolidationFailed records that a pass gave up on the given facts,
	// excluding them from later passes until recovery requeues them. It is how
	// a failed batch becomes visible in the bank's failed-consolidation gauge
	// instead of being retried forever.
	MarkConsolidationFailed(ctx context.Context, bankID string, factIDs []string) error
	// RelatedObservations returns observations of the given scope resembling
	// the given fact texts.
	//
	// It exists because "the newest observations" is not "the observations that
	// resemble these facts": dedup needs the near-identical twin, which shares
	// neither a graph link nor much vocabulary and therefore sits far down a
	// recency-ordered pool. A store without a similarity lookup may return nil.
	RelatedObservations(ctx context.Context, bankID string, scope []string, texts []string, limit int) ([]Observation, error)
	// BankConfig returns a bank's raw configuration overrides, so the pass can
	// read the consolidation settings and the strategies that apply to a scope.
	// A store that has no configuration returns an empty map.
	BankConfig(ctx context.Context, bankID string) (map[string]any, error)
}

// TuningStore is the optional capability a store implements when it carries the
// deployment's consolidation tuning. A store without it leaves the pass on the
// reference's shipped defaults, so the capability only ever moves a deployment's
// configured values into the pass.
type TuningStore interface {
	ConsolidationTuning() storage.ConsolidationTuning
}

// DefaultRelatedObservations bounds the near-duplicate lookup added to the
// merge-candidate pool.
const DefaultRelatedObservations = 10

// widenWithRelated adds the observations that resemble the facts being folded
// to the merge-candidate pool, without duplicating one already offered.
//
// A lookup that fails is not fatal: the pass still runs with the pool it has,
// because losing a merge candidate costs a duplicate conclusion while failing
// the pass costs every conclusion in the batch.
func (c *Consolidator) widenWithRelated(ctx context.Context, bankID string, scope []string, facts []Fact, observations []Observation) []Observation {
	if c.store == nil {
		return observations
	}
	texts := make([]string, 0, len(facts))
	for _, fact := range facts {
		if text := strings.TrimSpace(fact.Text); text != "" {
			texts = append(texts, text)
		}
	}
	if len(texts) == 0 {
		return observations
	}
	related, err := c.store.RelatedObservations(ctx, bankID, scope, texts, DefaultRelatedObservations)
	if err != nil || len(related) == 0 {
		return observations
	}
	seen := make(map[string]bool, len(observations))
	for _, observation := range observations {
		seen[observation.ID] = true
	}
	for _, observation := range related {
		// Defence in depth: the lookup is scoped by the store, but a store that
		// ignores the scope must not leak a differently-scoped observation into
		// the merge pool, where the model would merge across the boundary.
		if observation.ID == "" || seen[observation.ID] || !sameScope(observation.Scope, scope) {
			continue
		}
		seen[observation.ID] = true
		observations = append(observations, observation)
	}
	return observations
}

// Consolidator runs one pass.
type Consolidator struct {
	provider        provider.Provider
	store           Store
	maxFacts        int
	maxObservations int
	scope           string
	// tuning carries the deployment's consolidation knobs — the load batch
	// size, the model-call budget and the per-operation attempt budget. It
	// defaults to the reference's shipped values, so a deployment that set
	// nothing runs as before.
	tuning storage.ConsolidationTuning
	// settings are the bank-wide defaults a pass starts from; Run overrides
	// them with the bank's own configuration.
	settings Settings
	// progress, when set, receives the pass's phase and batch snapshots for the
	// operation running it. A pass started outside a worker (a test, a
	// maintenance job) has none, and every write is skipped.
	progress ProgressReporter
	// operationID names the operation a worker is running, so a snapshot can be
	// attributed to it. It is empty for the same reason progress is nil.
	operationID string
}

// ProgressReporter records a progress snapshot for a running operation. The
// worker passes the store's own writer; nothing a reporter does may fail the
// pass, which is the caller's contract and the reason this is a narrow
// interface rather than an error-returning hook.
type ProgressReporter func(operationID string, snapshot map[string]any)

// New builds a consolidator over the provider and store. When the store carries
// the deployment's consolidation tuning, the pass adopts it; otherwise it runs
// with the reference's defaults.
func New(p provider.Provider, store Store) *Consolidator {
	c := &Consolidator{
		provider:        p,
		store:           store,
		maxFacts:        DefaultMaxFacts,
		maxObservations: DefaultMaxObservations,
		scope:           provider.ScopeConsolidation,
		tuning:          storage.DefaultConsolidationTuning(),
		settings:        DefaultSettings(),
	}
	if tuned, ok := store.(TuningStore); ok {
		c.tuning = tuned.ConsolidationTuning()
	}
	return c
}

// WithOperation attributes the pass to a worker operation, so its phase and
// batch snapshots land on that operation's record and an operator polling the
// status route can see the pass advancing. A nil reporter leaves the pass
// unreported, which is what a pass outside a worker does.
func (c *Consolidator) WithOperation(operationID string, report ProgressReporter) *Consolidator {
	c.operationID = operationID
	c.progress = report
	return c
}

// reportProgress writes one snapshot, dropping it when the pass is unreported.
// The reference's heartbeat swallows its own failures so a reporting problem
// never fails the job; the same holds here, where the reporter is expected to
// have logged whatever went wrong.
func (c *Consolidator) reportProgress(stage string, processed, total int, detail map[string]int) {
	if c.progress == nil || c.operationID == "" {
		return
	}
	// The timestamp is the reference's `datetime.now(UTC).isoformat()`: a
	// dict-valued member, so it carries the `+00:00` offset rather than the `Z`
	// a model field renders. `detail` is always present, null when the stage
	// reports no counters — the reference's snapshot only omits it for a stage
	// that passed none, and every stage here passes the model field through.
	snapshot := map[string]any{
		"stage":     stage,
		"at":        time.Now().UTC().Format("2006-01-02T15:04:05.999999+00:00"),
		"processed": processed,
		"total":     total,
	}
	if len(detail) > 0 {
		snapshot["detail"] = detail
	} else {
		snapshot["detail"] = nil
	}
	c.progress(c.operationID, snapshot)
}

// Available reports whether a model is configured.
func (c *Consolidator) Available() bool {
	if c == nil || c.provider == nil {
		return false
	}
	// The no-model selection is a real provider that reports its unavailability
	// through VerifyConnection rather than by being absent.
	return c.provider.Name() != "none"
}

// Run performs one consolidation pass over a bank. It returns the counts it
// applied. With no model configured it reports ErrNoModel rather than inventing
// observations.
//
// The pass is run once per observation scope the bank's unconsolidated facts
// resolve to, so facts of different scopes are never folded into one
// observation and each produced observation carries its scope.
func (c *Consolidator) Run(ctx context.Context, bankID string) (Result, error) {
	// The pass's observation writes are tracked for the document change history,
	// and filed on the way out — including on failure, because every batch that
	// got this far committed its writes (consolidator.py:1596-1622).
	ctx, changes := history.TrackChanges(ctx)
	defer c.recordConsolidationEvents(ctx, bankID, changes)
	if !c.Available() {
		return Result{}, ErrNoModel
	}
	settings := c.settings
	cfg, err := c.store.BankConfig(ctx, bankID)
	if err != nil {
		return Result{}, fmt.Errorf("read bank config: %w", err)
	}
	if cfg != nil {
		settings = settingsFromConfig(cfg)
	}
	// A round cap bounds how many facts one pass folds, so one bank cannot hold
	// a worker slot indefinitely; 0 means unlimited and falls back to the read
	// bound. The load batch size then bounds how many memories one load carries,
	// so a pass reads the bank in the reference's batches rather than pulling
	// the whole backlog into memory at once.
	limit := c.maxFacts
	if c.tuning.MemoryBatchSize > 0 && c.tuning.MemoryBatchSize < limit {
		limit = c.tuning.MemoryBatchSize
	}
	if settings.MaxMemoriesPerRound > 0 && settings.MaxMemoriesPerRound < limit {
		limit = settings.MaxMemoriesPerRound
	}
	facts, err := c.store.ConsolidationFacts(ctx, bankID, limit)
	if err != nil {
		return Result{}, fmt.Errorf("read facts: %w", err)
	}
	if len(facts) == 0 {
		return Result{}, nil
	}
	// An initial durable snapshot, so an operator polling the status route sees
	// the pass has started and how much work it found before the first batch of
	// model calls completes — which can take minutes on a dense bank. The
	// reference writes it at the same point with the same "consolidating" stage
	// (`consolidator.py:1585-1586`), so the pass reads as one phase advancing
	// 0/N to N/N rather than an opaque start.
	totalUnits := len(facts)
	c.reportProgress("consolidating", 0, totalUnits, nil)
	var total Result
	for _, group := range groupFactsByScope(facts) {
		result, err := c.runScope(ctx, bankID, group.scope, group.facts, settings.forScope(group.scope))
		total.add(result)
		// The heartbeat an operator polls: one snapshot per scope, carrying the
		// running counters, so `processed` advances as each scope commits rather
		// than sitting at the pre-pass count through the whole model phase.
		c.reportProgress("consolidating", total.Processed(), totalUnits,
			total.progressDetail())
		if err != nil {
			return total, err
		}
	}
	// The final phase, written once the chain has drained: the observations are
	// in and what remains is the mental-model refreshes the pass triggers.
	c.reportProgress("refreshing_mental_models", total.Processed(), totalUnits, nil)
	return total, nil
}

// Processed is how many memories the pass has folded so far: everything it
// created, updated or deleted an observation for, plus the ones it skipped and
// the ones it failed on. Counting the failures keeps the bar advancing on a
// bank whose facts the model cannot handle, which is what the reference's own
// `memories_processed` does.
func (r Result) Processed() int {
	return r.Created + r.Updated + r.Deleted + r.Skipped + r.Failed
}

// progressDetail renders the pass's counters as the reference's per-batch
// detail object, so a poller sees how many observations each outcome produced.
func (r Result) progressDetail() map[string]int {
	return map[string]int{
		"observations_created": r.Created,
		"observations_updated": r.Updated,
		"observations_deleted": r.Deleted,
		"memories_failed":      r.Failed,
	}
}

// add folds one scope's counts into the running total.
func (r *Result) add(other Result) {
	r.Created += other.Created
	r.Updated += other.Updated
	r.Deleted += other.Deleted
	r.Skipped += other.Skipped
	r.Failed += other.Failed
	r.Usage.InputTokens += other.Usage.InputTokens
	r.Usage.OutputTokens += other.Usage.OutputTokens
	r.Usage.TotalTokens += other.Usage.TotalTokens
	r.Usage.CachedInputTokens += other.Usage.CachedInputTokens
	r.Usage.ReasoningTokens += other.Usage.ReasoningTokens
}

// runScope folds one scope's facts, offering only that scope's observations as
// merge candidates and stamping created observations with the scope. The facts
// are split into model-call batches of the configured size and run with the
// configured parallelism, so one pass respects both the batch and concurrency
// budgets.
func (c *Consolidator) runScope(ctx context.Context, bankID string, scope []string, facts []Fact, settings Settings) (Result, error) {
	// A scope that already holds its observation cap offers no create slots, so
	// the pool is read with that cap: the count the pass sees is the count the
	// cap is measured against.
	poolLimit := c.maxObservations
	if settings.MaxObservationsPerScope >= 0 && settings.MaxObservationsPerScope < poolLimit {
		poolLimit = settings.MaxObservationsPerScope
	}
	observations, err := c.store.ConsolidationObservations(ctx, bankID, scope, poolLimit)
	if err != nil {
		return Result{}, fmt.Errorf("read observations: %w", err)
	}
	// The pool above is "the newest observations", which is not the same as
	// "the observations that resemble these facts". A near-identical twin the
	// model should merge into can be far down that list, so it is looked up
	// separately and added before the prompt is built.
	observations = c.widenWithRelated(ctx, bankID, scope, facts, observations)

	remaining := -1
	if settings.MaxObservationsPerScope >= 0 {
		remaining = settings.MaxObservationsPerScope - len(observations)
		if remaining < 0 {
			remaining = 0
		}
	}

	batches := chunkFacts(facts, settings.LLMBatchSize)
	results := make([]Result, len(batches))
	errs := make([]error, len(batches))
	parallelism := settings.LLMParallelism
	if parallelism < 1 {
		parallelism = 1
	}
	sem := make(chan struct{}, parallelism)
	var wg sync.WaitGroup
	for index, batch := range batches {
		wg.Add(1)
		go func(index int, batch []Fact) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[index], errs[index] = c.runBatch(ctx, bankID, scope, batch, observations, settings, remaining)
		}(index, batch)
	}
	wg.Wait()

	var total Result
	var failedFacts []string
	for index := range batches {
		var lf *llmFailure
		if errors.As(errs[index], &lf) {
			// A model that failed or answered unusably is recorded against the
			// facts of that batch rather than failing the pass: the writes of
			// the batches that did succeed are already committed, and the
			// failed facts belong in the backlog gauge until recovery requeues
			// them.
			failedFacts = append(failedFacts, lf.factIDs...)
			total.add(results[index])
			total.Failed += len(lf.factIDs)
			continue
		}
		total.add(results[index])
		if errs[index] != nil {
			return total, errs[index]
		}
	}
	if len(failedFacts) > 0 {
		if err := c.store.MarkConsolidationFailed(ctx, bankID, failedFacts); err != nil {
			return total, fmt.Errorf("mark failed consolidation: %w", err)
		}
	}
	return total, nil
}

// runBatch folds one model call's worth of facts against the shared observation
// pool and applies the decision it returns.
func (c *Consolidator) runBatch(ctx context.Context, bankID string, scope []string, facts []Fact, observations []Observation, settings Settings, remaining int) (Result, error) {
	request := provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: buildInput(facts, observations, settings, remaining)},
		},
		ResponseSchema: decisionSchema(),
		Scope:          c.scope,
	}
	// The completion cap only ever narrows the model's own default: unset (zero)
	// leaves the parameter off, which is the reference's behaviour too.
	if c.tuning.MaxCompletionTokens > 0 {
		request.MaxTokens = c.tuning.MaxCompletionTokens
	}
	attempts := c.tuning.MaxAttempts
	if attempts < 1 {
		attempts = 1
	}
	var response provider.CompletionResult
	var err error
	for range attempts {
		response, err = c.provider.Complete(ctx, request)
		if err == nil {
			break
		}
		// Count the failed call. A raised call is transport-shaped: an unchanged
		// re-send may succeed, so it is the "retry" class.
		recordBatchFailure(failureClassRetry, err)
		// A cancelled or expired context will not clear on a retry, so it stops
		// the attempt loop rather than burning the budget on a dead call.
		if ctx.Err() != nil {
			break
		}
	}
	if err != nil {
		return Result{}, &llmFailure{factIDs: factIDsOf(facts), err: fmt.Errorf("consolidation call failed: %w", err)}
	}
	decision, err := parseDecision(response.Structured, response.Content)
	if err != nil {
		return Result{}, &llmFailure{factIDs: factIDsOf(facts), err: err}
	}
	return c.applyDecision(ctx, bankID, scope, facts, observations, decision, response.Usage, remaining)
}

// failureClassRetry is the consolidation batch failure class this service can
// produce: a model call that raised. The reference also has a "fail_fast" class
// for a response its schema rejects and a "propagate" class for a control
// signal; this service's response parser never rejects a response and never
// raises a control signal, so those two classes are never recorded rather than
// fabricated.
const failureClassRetry = "retry"

// recordBatchFailure reports one failed consolidation batch call, labelled by
// failure class so the transport-shaped failures stay separable from the
// schema-shaped ones, and by the error's concrete type name, as the reference
// reports the exception class.
func recordBatchFailure(failureClass string, err error) {
	monitoring.Workload.CountConsolidationBatchFailure(failureClass, errorTypeName(err))
}

// errorTypeName is an error's concrete type name without its package qualifier
// or pointer marker.
func errorTypeName(err error) string {
	if err == nil {
		return ""
	}
	t := reflect.TypeOf(err)
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t.Name()
}

// llmFailure marks a batch whose model call failed or answered unusably, so the
// pass can record those facts as failed without failing the whole run. Storage
// errors are not wrapped in it and still fail the pass.
type llmFailure struct {
	factIDs []string
	err     error
}

func (e *llmFailure) Error() string { return e.err.Error() }
func (e *llmFailure) Unwrap() error { return e.err }

// factIDsOf collects the identifiers of one batch's facts.
func factIDsOf(facts []Fact) []string {
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.ID)
	}
	return ids
}

// applyDecision writes one decision to storage and marks the facts it consumed.
// remaining bounds the creates this call may apply: -1 is unlimited, 0 or more
// is the observation slots left in the scope.
func (c *Consolidator) applyDecision(ctx context.Context, bankID string, scope []string, facts []Fact, observations []Observation, decision Decision, usage provider.Usage, remaining int) (Result, error) {
	result := Result{Usage: usage}
	factIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		factIDs = append(factIDs, fact.ID)
	}
	touched := map[string]bool{}
	// Exact-duplicate guard: an observation whose normalized text already
	// exists (or was created this pass) is not created again, so a near-miss
	// the model restated does not accumulate as a twin.
	existingTexts := map[string]bool{}
	createdKeys := make([]string, 0, len(observations)+len(decision.Creates))
	for _, observation := range observations {
		normalized := normalizeObservationText(observation.Text)
		existingTexts[normalized] = true
		createdKeys = append(createdKeys, normalized)
	}
	observationTextByID := map[string]string{}
	// The facts each pooled observation stands on, read here so a delete can name
	// them after the row is gone: the change history files the removal under the
	// documents those facts came from.
	observationSourcesByID := map[string][]string{}
	for _, observation := range observations {
		observationTextByID[observation.ID] = normalizeObservationText(observation.Text)
		observationSourcesByID[observation.ID] = observation.SourceIDs
	}
	createsApplied := 0
	for _, create := range decision.Creates {
		// The scope's observation cap bounds creates: once the cap is reached
		// the model is told to update or delete, and any create it still emits
		// is dropped rather than written past the cap.
		if remaining >= 0 && createsApplied >= remaining {
			result.Skipped++
			continue
		}
		if strings.TrimSpace(create.Text) == "" {
			continue
		}
		key := normalizeObservationText(create.Text)
		// An exact restatement is never written twice, whatever the embedding
		// path does: the reference folds it too, and equality needs no model.
		if existingTexts[key] {
			result.Skipped++
			continue
		}
		// A near-duplicate is decided by the reference's mechanism when the
		// deployment can embed — the cosine of the two observation embeddings
		// against the configured threshold — and by the token-similarity floor
		// when it cannot. A twin found by the embedding probe is folded: the new
		// evidence is attached to it rather than written as a second row.
		if twin, duplicate := c.duplicateObservation(ctx, bankID, scope, create.Text, key, createdKeys); duplicate {
			if twin.ID != "" && twin.Text != "" {
				sources := validSourceIDs(create.SourceIDs, factIDs)
				if err := c.store.UpdateObservation(ctx, bankID, twin.ID, twin.Text, sources); err != nil {
					return result, fmt.Errorf("fold duplicate observation: %w", err)
				}
				c.noteObservationWritten(ctx, twin.ID, sources, false)
				markTouched(touched, sources)
			}
			result.Skipped++
			continue
		}
		sources := validSourceIDs(create.SourceIDs, factIDs)
		createdID, err := c.store.CreateObservation(ctx, bankID, create.Text, sources, scope)
		if err != nil {
			return result, fmt.Errorf("create observation: %w", err)
		}
		c.noteObservationWritten(ctx, createdID, sources, true)
		existingTexts[key] = true
		createdKeys = append(createdKeys, key)
		markTouched(touched, sources)
		createsApplied++
		result.Created++
	}
	for _, update := range decision.Updates {
		if update.ObservationID == "" || strings.TrimSpace(update.Text) == "" {
			continue
		}
		// An update that only restates the observation's own text changes
		// nothing and is dropped rather than written back.
		if observationTextByID[update.ObservationID] == normalizeObservationText(update.Text) {
			result.Skipped++
			continue
		}
		sources := validSourceIDs(update.SourceIDs, factIDs)
		if err := c.store.UpdateObservation(ctx, bankID, update.ObservationID, update.Text, sources); err != nil {
			return result, fmt.Errorf("update observation: %w", err)
		}
		c.noteObservationWritten(ctx, update.ObservationID, sources, false)
		markTouched(touched, sources)
		result.Updated++
	}
	for _, del := range decision.Deletes {
		// A delete entry without a target names nothing and would remove an
		// arbitrary observation, so it is skipped rather than guessed at.
		if del.ObservationID == "" {
			continue
		}
		// Noted before the row goes: its source facts are what the document
		// change history attributes the removal through.
		c.noteObservationRemoved(ctx, del.ObservationID, observationSourcesByID[del.ObservationID])
		if err := c.store.DeleteObservation(ctx, bankID, del.ObservationID); err != nil {
			return result, fmt.Errorf("delete observation: %w", err)
		}
		result.Deleted++
	}
	// Facts no decision referenced are not marked, so the next pass offers them
	// again rather than losing them.
	consumed := make([]string, 0, len(touched))
	for _, id := range factIDs {
		if touched[id] {
			consumed = append(consumed, id)
		}
	}
	if len(consumed) > 0 {
		if err := c.store.MarkConsolidated(ctx, bankID, consumed); err != nil {
			return result, fmt.Errorf("mark consolidated: %w", err)
		}
	}
	return result, nil
}

// chunkFacts splits facts into batches of at most size, preserving order. A
// size below 1 means one batch (no batching).
func chunkFacts(facts []Fact, size int) [][]Fact {
	if size < 1 || len(facts) <= size {
		return [][]Fact{facts}
	}
	batches := make([][]Fact, 0, (len(facts)+size-1)/size)
	for start := 0; start < len(facts); start += size {
		end := start + size
		if end > len(facts) {
			end = len(facts)
		}
		batches = append(batches, facts[start:end])
	}
	return batches
}

// normalizeObservationText folds case and collapses whitespace, so two texts
// that differ only in spacing or case count as the same observation.
func normalizeObservationText(text string) string {
	return strings.Join(strings.Fields(strings.ToLower(text)), " ")
}

// observationSimilarityFloor is the token-Jaccard floor above which two
// observation texts are treated as the same conclusion. It is the fallback the
// pass uses only when the deployment cannot embed: the reference's mechanism is
// the cosine similarity of the two observation embeddings against the
// consolidation dedup threshold (see duplicateObservation), and Jaccard lives on
// a different scale, so it decides nothing when embeddings are available.
const observationSimilarityFloor = 0.7

// nearDuplicate reports whether a normalized text is nearly identical to any
// candidate, so a restated conclusion does not accumulate as a separate twin.
// It is the no-embedding fallback: when the deployment can embed, the cosine
// threshold decides instead and this floor is not consulted.
func nearDuplicate(text string, candidates []string) bool {
	if strings.TrimSpace(text) == "" {
		return false
	}
	for _, candidate := range candidates {
		if candidate == text {
			continue
		}
		if tokenJaccard(text, candidate) >= observationSimilarityFloor {
			return true
		}
	}
	return false
}

// tokenJaccard returns the Jaccard similarity of two texts' word sets.
func tokenJaccard(a, b string) float64 {
	left := tokenSet(a)
	right := tokenSet(b)
	if len(left) == 0 || len(right) == 0 {
		return 0
	}
	intersection := 0
	for token := range left {
		if right[token] {
			intersection++
		}
	}
	union := len(left) + len(right) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// tokenSet lowercases and splits a text into its word set.
func tokenSet(text string) map[string]bool {
	out := map[string]bool{}
	for _, field := range strings.FieldsFunc(text, func(r rune) bool {
		return !isLetterOrDigit(r)
	}) {
		out[strings.ToLower(field)] = true
	}
	return out
}

// isLetterOrDigit reports whether a rune is an ASCII letter or digit.
func isLetterOrDigit(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')
}

// markTouched records which facts a decision actually consumed.
func markTouched(touched map[string]bool, ids []string) {
	for _, id := range ids {
		touched[id] = true
	}
}

// validSourceIDs keeps only the source ids naming a fact in this batch, so a
// model that invents an identifier cannot link an observation to nothing.
func validSourceIDs(ids []string, known []string) []string {
	allowed := make(map[string]bool, len(known))
	for _, id := range known {
		allowed[id] = true
	}
	kept := make([]string, 0, len(ids))
	for _, id := range ids {
		if allowed[id] {
			kept = append(kept, id)
		}
	}
	return kept
}

// parseDecision reads the model's answer. The structured form is preferred; the
// prose form is accepted so a model that ignored the schema still contributes.
func parseDecision(structured any, content string) (Decision, error) {
	if structured != nil {
		encoded, err := json.Marshal(structured)
		if err == nil {
			var decision Decision
			if err := json.Unmarshal(encoded, &decision); err == nil {
				decision.normalize()
				return decision, nil
			}
		}
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return Decision{}, nil
	}
	if start := strings.Index(trimmed, "{"); start >= 0 {
		if end := strings.LastIndex(trimmed, "}"); end > start {
			var decision Decision
			if err := json.Unmarshal([]byte(trimmed[start:end+1]), &decision); err == nil {
				decision.normalize()
				return decision, nil
			}
		}
	}
	// A model that answered in neither form contributed nothing; that is not an
	// error, because a pass with no decision simply leaves the facts for later.
	return Decision{}, nil
}

// normalize replaces nil slices with empty ones so a reader never has to tell
// an absent array from an empty one.
func (d *Decision) normalize() {
	if d.Creates == nil {
		d.Creates = []Create{}
	}
	if d.Updates == nil {
		d.Updates = []Update{}
	}
	if d.Deletes == nil {
		d.Deletes = []Delete{}
	}
}
