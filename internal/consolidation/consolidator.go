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
	"log/slog"
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
	// UpdatedAt is the fact's last-edit stamp as read. A store that can
	// re-check it under a write lock carries it so the pass can tell whether
	// the fact was edited while the model was answering; an empty value leaves
	// the fact out of that check.
	UpdatedAt string
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
// CurrentText is the observation's text as the model copied it before editing —
// the prompt asks for it so a small model reads the old text before writing the
// new one; the write itself uses Text alone (the reference never reads the
// field either, consolidator.py:788-797).
type Update struct {
	ObservationID string   `json:"observation_id"`
	CurrentText   string   `json:"current_text,omitempty"`
	Text          string   `json:"text"`
	SourceIDs     []string `json:"source_fact_ids"`
	Reason        string   `json:"reason"`
}

// Delete removes one observation the new facts superseded outright. MergedInto
// names the observation that now carries this one's content when the delete is
// a merge: its evidence moves there instead of vanishing with the row.
type Delete struct {
	ObservationID string `json:"observation_id"`
	MergedInto    string `json:"merged_into,omitempty"`
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
	// ObservationIDs are the observations this pass wrote — created ones, and
	// ones an update rewrote — in the order they were applied, without repeats.
	// A caller that has to say which observations one consolidation produced
	// reads this instead of guessing from a time window, which is what the
	// reference records on the operation's trace as `metadata.memory_ids`
	// (consolidator.py, `record_created_memory_ids`).
	ObservationIDs []string
	Usage          provider.Usage
}

// noteWritten records one observation this pass wrote.
func (r *Result) noteWritten(observationID string) {
	if observationID == "" {
		return
	}
	for _, existing := range r.ObservationIDs {
		if existing == observationID {
			return
		}
	}
	r.ObservationIDs = append(r.ObservationIDs, observationID)
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
	// callerRequested marks a chain an explicit /consolidate call started. A
	// round-capped chain it started keeps draining the backlog even when
	// automatic consolidation is off; a chain a retain started stops
	// (consolidator.py:2211-2225).
	callerRequested bool
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

// WithCallerRequested marks the pass as part of a chain an explicit /consolidate
// call started, whose round-capped requeues keep draining the backlog even when
// automatic consolidation is off. A pass a retain triggered leaves it unset.
func (c *Consolidator) WithCallerRequested(requested bool) *Consolidator {
	c.callerRequested = requested
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
	// bound. The load batch size then bounds how many memories one fetch
	// carries, so a pass reads the bank in the reference's batches rather than
	// pulling the whole round into memory at once — and when the fetch exhausts
	// the round budget, a successor operation carries the rest
	// (consolidator.py:1741-1747, 2186-2229).
	fetchBatch := c.maxFacts
	if c.tuning.MemoryBatchSize > 0 && c.tuning.MemoryBatchSize < fetchBatch {
		fetchBatch = c.tuning.MemoryBatchSize
	}
	roundCap := settings.MaxMemoriesPerRound
	var total Result
	totalUnits := 0
	hitRoundLimit := false
	for {
		// Cap the fetch by the remaining round budget, the way the reference's
		// fetch_limit does.
		fetch := fetchBatch
		if roundCap > 0 {
			if remaining := roundCap - totalUnits; remaining < fetch {
				fetch = remaining
			}
			if fetch <= 0 {
				hitRoundLimit = true
				break
			}
		}
		facts, err := c.store.ConsolidationFacts(ctx, bankID, fetch)
		if err != nil {
			return total, fmt.Errorf("read facts: %w", err)
		}
		if len(facts) == 0 {
			break
		}
		// An initial durable snapshot, so an operator polling the status route
		// sees the pass has started and how much work it found before the first
		// batch of model calls completes — which can take minutes on a dense
		// bank. The reference writes it at the same point with the same
		// "consolidating" stage (`consolidator.py:1585-1586`), so the pass reads
		// as one phase advancing 0/N to N/N rather than an opaque start.
		totalUnits += len(facts)
		c.reportProgress("consolidating", total.Processed(), totalUnits, nil)
		for _, group := range groupFactsByScope(facts) {
			result, err := c.runScope(ctx, bankID, group.scope, group.facts, settings.forScope(group.scope))
			total.add(result)
			// The heartbeat an operator polls: one snapshot per scope, carrying
			// the running counters, so `processed` advances as each scope
			// commits rather than sitting at the pre-pass count through the
			// whole model phase.
			c.reportProgress("consolidating", total.Processed(), totalUnits, total.progressDetail())
			if err != nil {
				return total, err
			}
		}
		if roundCap > 0 && totalUnits >= roundCap {
			hitRoundLimit = true
			break
		}
	}
	// The final phase, written once the chain has drained: the observations are
	// in and what remains is the mental-model refreshes the pass triggers.
	c.reportProgress("refreshing_mental_models", total.Processed(), totalUnits, nil)
	if hitRoundLimit {
		// An automatic chain stops re-queuing once auto-consolidation is off;
		// only an explicit /consolidate run keeps draining the backlog
		// (consolidator.py:2211-2225).
		if !c.callerRequested && !settings.EnableAutoConsolidation {
			return total, nil
		}
		enqueuer, ok := c.store.(SuccessorEnqueuer)
		if !ok {
			// A store that cannot requeue leaves the round as the last word;
			// the facts past the cap wait for the next trigger.
			return total, nil
		}
		if err := enqueuer.EnqueueConsolidationSuccessor(ctx, bankID, c.callerRequested); err != nil {
			// A failure here must propagate: swallowing it leaves the bank with
			// backlog and no queued work — silently stuck — because the outer
			// operation gets marked completed in the success path.
			return total, fmt.Errorf("requeue consolidation: %w", err)
		}
	}
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
	// Every observation the folded-in pass wrote belongs to the whole run too,
	// so a caller reading the total sees the writes of all scopes.
	for _, observationID := range other.ObservationIDs {
		r.noteWritten(observationID)
	}
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
		// The cap is measured against the scope's TRUE observation count, not
		// the bounded candidate pool: a scope holding more observations than the
		// pool reads would otherwise grow past its cap (the reference counts the
		// scope to the end, engine/memories/base.py:3838-3857, and computes the
		// remaining slots from it, consolidator.py:2555-2560). A store without
		// the counting capability keeps the pool-derived reading.
		total := len(observations)
		if counter, ok := c.store.(ScopeObservationCounter); ok {
			if counted, err := counter.CountObservationsInScope(ctx, bankID, scope); err == nil {
				total = counted
			}
		}
		remaining = settings.MaxObservationsPerScope - total
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
//
// A fact the response never acted on is stamped consolidated all the same, and
// would never be shown to the model again. In a multi-fact batch that is as
// likely an oversight as a judgement, so the facts it left are asked again
// together, in a smaller batch; one that batch forgets too gets a last call of
// its own — at most two re-asks per fact, and a fact skipped while alone is the
// model's answer (chatter) and is never re-asked (consolidator.py:1948-1969).
func (c *Consolidator) runBatch(ctx context.Context, bankID string, scope []string, facts []Fact, observations []Observation, settings Settings, remaining int) (Result, error) {
	var total Result
	reasked := map[string]int{}
	pending := [][]Fact{facts}
	for len(pending) > 0 {
		batch := pending[0]
		pending = pending[1:]
		result, referenced, err := c.askAndApply(ctx, bankID, scope, batch, observations, settings, remaining)
		if err != nil {
			return total, err
		}
		total.add(result)
		if len(batch) <= 1 {
			continue
		}
		// The response left facts unaddressed: re-ask the ones that have calls
		// left, the never-re-asked ones as one group and the rest singly. The
		// re-asked batches stamp their own facts when answered.
		var first []Fact
		var again []Fact
		for _, fact := range batch {
			if referenced[fact.ID] || reasked[fact.ID] >= maxReasksPerFact {
				continue
			}
			if reasked[fact.ID] == 0 {
				first = append(first, fact)
			} else {
				again = append(again, fact)
			}
			reasked[fact.ID]++
		}
		if len(first) == 0 && len(again) == 0 {
			continue
		}
		prepended := make([][]Fact, 0, 1+len(again))
		if len(first) > 0 {
			prepended = append(prepended, first)
		}
		for _, fact := range again {
			prepended = append(prepended, []Fact{fact})
		}
		pending = append(prepended, pending...)
	}
	return total, nil
}

// maxReasksPerFact bounds how many times one pass re-asks a fact the response
// left unaddressed before stamping it anyway. It is the reference's
// `reasked.get(...) < 2`.
const maxReasksPerFact = 2

// askAndApply makes one model call for one batch and applies its decision.
func (c *Consolidator) askAndApply(ctx context.Context, bankID string, scope []string, facts []Fact, observations []Observation, settings Settings, remaining int) (Result, map[string]bool, error) {
	input := buildInput(facts, observations, settings, remaining)
	request := provider.CompletionRequest{
		Messages: []provider.Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: input},
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
		return Result{}, nil, &llmFailure{factIDs: factIDsOf(facts), err: fmt.Errorf("consolidation call failed: %w", err)}
	}
	decision, err := parseDecision(response.Structured, response.Content)
	if err != nil {
		return Result{}, nil, &llmFailure{factIDs: factIDsOf(facts), err: err}
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

// addsNewSources reports whether sources names any id the observation does not
// already rest on.
func addsNewSources(sources, existing []string) bool {
	if len(sources) == 0 {
		return false
	}
	known := make(map[string]bool, len(existing))
	for _, id := range existing {
		known[id] = true
	}
	for _, id := range sources {
		if !known[id] {
			return true
		}
	}
	return false
}

// factIDsOf collects the identifiers of one batch's facts.
func factIDsOf(facts []Fact) []string {
	ids := make([]string, 0, len(facts))
	for _, fact := range facts {
		ids = append(ids, fact.ID)
	}
	return ids
}

// applyDecision writes one decision to storage and stamps the whole batch
// consolidated once the pass has answered it. remaining bounds the creates this
// call may apply: -1 is unlimited, 0 or more is the observation slots left in
// the scope. It returns the facts an applied create or update cited, so the
// caller can re-ask the ones the response left unaddressed.
func (c *Consolidator) applyDecision(ctx context.Context, bankID string, scope []string, facts []Fact, observations []Observation, decision Decision, usage provider.Usage, remaining int) (Result, map[string]bool, error) {
	result := Result{Usage: usage}
	factIDs := make([]string, 0, len(facts))
	for _, fact := range facts {
		factIDs = append(factIDs, fact.ID)
	}
	// A model recording a new fact sometimes names that fact as the UPDATE
	// target. There is no observation to update, and dropping it loses the
	// fact: it is a create (consolidator.py:3753-3760).
	factIDSet := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		factIDSet[id] = true
	}
	creates, updates := reaimUpdatesAtFacts(decision, factIDSet)
	// Exact-duplicate guard: an observation whose normalized text already
	// exists (or was created this pass) is not created again, so a near-miss
	// the model restated does not accumulate as a twin.
	existingTexts := map[string]bool{}
	for _, observation := range observations {
		existingTexts[normalizeObservationText(observation.Text)] = true
	}
	observationTextByID := map[string]string{}
	// The facts each pooled observation stands on, read here so a delete can name
	// them after the row is gone: the change history files the removal under the
	// documents those facts came from.
	observationSourcesByID := map[string][]string{}
	// The ids the model was shown: every target it names — an update, a delete
	// or a merge destination — must be one of them, and a miscopied id is
	// repaired against this set first (consolidator.py:2659, 3285-3306).
	poolIDs := make(map[string]bool, len(observations))
	for _, observation := range observations {
		observationTextByID[observation.ID] = normalizeObservationText(observation.Text)
		observationSourcesByID[observation.ID] = observation.SourceIDs
		poolIDs[observation.ID] = true
	}
	for i := range updates {
		updates[i].ObservationID = resolvePoolID(updates[i].ObservationID, poolIDs)
	}
	// Several updates naming one observation collapse into a single write: keep
	// the last text and union their source ids in order, so the observation is
	// rewritten once and gets one history snapshot.
	updates = dedupeUpdates(updates)
	for i := range decision.Deletes {
		decision.Deletes[i].ObservationID = resolvePoolID(decision.Deletes[i].ObservationID, poolIDs)
		if decision.Deletes[i].MergedInto != "" {
			decision.Deletes[i].MergedInto = resolvePoolID(decision.Deletes[i].MergedInto, poolIDs)
		}
	}
	updatedIDs := make(map[string]bool, len(updates))
	for _, update := range updates {
		updatedIDs[update.ObservationID] = true
	}
	preparedDeletes := prepareDeletes(decision.Deletes, updatedIDs, poolIDs)
	// A delete the pass rejected — updated in the same response, or naming an
	// observation the model was never shown — is a dropped action like any
	// other, and advances the pass's counters rather than vanishing.
	result.Skipped += len(decision.Deletes) - len(preparedDeletes)

	// Prepare phase: resolve every duplicate verdict before any write, and
	// before the source-fact revalidation, so the focused model calls never run
	// while a write lock is held.
	plan := c.prepareAdjudications(ctx, bankID, scope, creates, updates)

	// Guard: inside the response's write transaction, confirm the batch's source
	// facts still carry the updated_at values they had when read. An edit in
	// between already requeued those facts and dropped their observations;
	// applying this stale response would rebuild them under the old tags and
	// the consolidated stamp would undo the requeue. Drop the whole response,
	// stamps included, and let the next pass re-read them.
	changed, err := c.sourcesChangedSinceRead(ctx, bankID, facts)
	if err != nil {
		return result, nil, fmt.Errorf("revalidate source facts: %w", err)
	}
	if len(changed) > 0 {
		slog.Info("Batch consolidation: discarding response — source facts edited since read", "bank_id", bankID, "changed", len(changed))
		// Mark every fact referenced so the re-ask loop does not spend this
		// pass's remaining calls on a deliberately dropped response; leaving
		// them unstamped keeps them pending for the next pass.
		discardTouched := make(map[string]bool, len(facts))
		for _, fact := range facts {
			discardTouched[fact.ID] = true
		}
		return Result{Usage: result.Usage}, discardTouched, nil
	}

	touched := map[string]bool{}
	createsApplied := 0
	for i, create := range creates {
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
		// path does: equality needs no model.
		if existingTexts[key] {
			result.Skipped++
			continue
		}
		sources := validSourceIDs(create.SourceIDs, factIDs)
		// A cosine twin at or above the threshold is only a candidate: the
		// focused verdict decides. A "keep" verdict (or an unusable one) keeps
		// both rows, so the create proceeds; only "merge" folds it into the
		// twin, and a twin that vanished or was rewritten meanwhile falls back
		// to the create so nothing is lost.
		verdict := plan.creates[i]
		if verdict.twin.ID != "" && verdict.shouldMerge {
			folded, err := c.mergeCreateIntoTwin(ctx, bankID, verdict, sources)
			if err != nil {
				return result, touched, fmt.Errorf("merge duplicate observation: %w", err)
			}
			if folded {
				c.noteObservationWritten(ctx, verdict.twin.ID, sources, false)
				markTouched(touched, sources)
				result.Skipped++
				continue
			}
		}
		createdID, err := c.store.CreateObservation(ctx, bankID, create.Text, sources, scope)
		if err != nil {
			return result, touched, fmt.Errorf("create observation: %w", err)
		}
		c.noteObservationWritten(ctx, createdID, sources, true)
		result.noteWritten(createdID)
		existingTexts[key] = true
		markTouched(touched, sources)
		createsApplied++
		result.Created++
	}
	for i, update := range updates {
		if update.ObservationID == "" || strings.TrimSpace(update.Text) == "" {
			continue
		}
		// Security: the observation must be one the model was shown. An id
		// outside the pool is a warning and a skipped action, not a failed
		// pass — the batch continues (consolidator.py:2696-2712).
		if !poolIDs[update.ObservationID] {
			slog.Warn("Batch consolidation: rejected update — observation " + update.ObservationID + " not in the recall pool")
			result.Skipped++
			continue
		}
		sources := validSourceIDs(update.SourceIDs, factIDs)
		// A rewritten observation can drift within the dedup threshold of a
		// different existing observation. On a merge verdict, fold the updated
		// row into that twin and retire it rather than leaving a near-duplicate
		// pair behind.
		verdict := plan.updates[i]
		if verdict.twin.ID != "" && verdict.shouldMerge {
			folded, err := c.mergeUpdateIntoTwin(ctx, bankID, update.ObservationID, update.Text, verdict)
			if err != nil {
				return result, touched, fmt.Errorf("merge updated observation: %w", err)
			}
			if folded {
				c.noteObservationWritten(ctx, verdict.twin.ID, sources, false)
				markTouched(touched, sources)
				result.Updated++
				continue
			}
		}
		// An update that restates the observation's text *and* adds no evidence
		// changes nothing and is dropped rather than written back. A restatement
		// that does name a new source is applied: its text is unchanged, but the
		// evidence it attaches is exactly what the merge was for.
		if observationTextByID[update.ObservationID] == normalizeObservationText(update.Text) &&
			!addsNewSources(sources, observationSourcesByID[update.ObservationID]) {
			result.Skipped++
			continue
		}
		if err := c.store.UpdateObservation(ctx, bankID, update.ObservationID, update.Text, sources); err != nil {
			return result, touched, fmt.Errorf("update observation: %w", err)
		}
		c.noteObservationWritten(ctx, update.ObservationID, sources, false)
		result.noteWritten(update.ObservationID)
		markTouched(touched, sources)
		result.Updated++
	}
	for _, del := range preparedDeletes {
		// A merge moves the deleted observation's evidence onto its survivor, or
		// — when the model named no usable target — requeues that evidence for
		// the next pass (consolidator.py:2881-2897, 3335-3380). A store without
		// the merge capability lets the evidence go with the row, as any plain
		// delete always has.
		if del.isMerge {
			if merger, ok := c.store.(MergeStore); ok {
				if err := merger.MergeObservationEvidence(ctx, bankID, del.mergedInto, del.observationID); err != nil {
					return result, touched, fmt.Errorf("fold merged evidence: %w", err)
				}
			}
		}
		// Noted before the row goes: its source facts are what the document
		// change history attributes the removal through.
		c.noteObservationRemoved(ctx, del.observationID, observationSourcesByID[del.observationID])
		if err := c.store.DeleteObservation(ctx, bankID, del.observationID); err != nil {
			return result, touched, fmt.Errorf("delete observation: %w", err)
		}
		result.Deleted++
	}
	// The whole batch is stamped consolidated once the pass has answered it,
	// including the facts the response skipped
	// (consolidator.py:1948-1969, 2962-2973): a fact left unstamped is
	// re-offered to every later pass forever, which is what kept the backlog
	// answering and reconcile re-enqueueing. The facts the model could not be
	// asked about at all take the failed path instead (see llmFailure).
	if len(factIDs) > 0 {
		if err := c.store.MarkConsolidated(ctx, bankID, factIDs); err != nil {
			return result, touched, fmt.Errorf("mark consolidated: %w", err)
		}
	}
	return result, touched, nil
}

// sourcesChangedSinceRead reports the batch's source facts whose last-edit
// stamp moved since the pass read them. Only a store that can take the lock and
// compare inside the write transaction provides it; a store with no updated_at
// on its reads leaves readAt empty and is not checked. A non-empty answer makes
// applyDecision discard the whole response, stamps included.
func (c *Consolidator) sourcesChangedSinceRead(ctx context.Context, bankID string, facts []Fact) ([]string, error) {
	revalidator, ok := c.store.(SourceRevalidationStore)
	if !ok {
		return nil, nil
	}
	readAt := make(map[string]string, len(facts))
	for _, fact := range facts {
		if fact.ID != "" && fact.UpdatedAt != "" {
			readAt[fact.ID] = fact.UpdatedAt
		}
	}
	if len(readAt) == 0 {
		return nil, nil
	}
	return revalidator.SourcesChangedSinceRead(ctx, bankID, readAt)
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

// normalizeObservationText collapses whitespace but preserves case. The
// reference matches exact duplicates case-sensitively: "TLS" and "tls" can
// carry different information, so folding them into one would lose a create.
func normalizeObservationText(text string) string {
	return strings.Join(strings.Fields(text), " ")
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
	// A model that answered in neither form produced nothing the pass can act
	// on. That is a failed batch rather than an empty answer: an empty answer
	// stamps the batch consolidated once applied, and stamping facts the pass
	// never actually read would drop them for good.
	return Decision{}, errors.New("consolidation response carried no usable JSON")
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
