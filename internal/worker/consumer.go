// Package worker consumes queued asynchronous operations. It claims, processes,
// and completes or retries them, reusing the storage area's operation
// interface rather than reaching the queue table directly.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/storage"
)

// Default tuning. The attempt budget matches the reference implementation's
// consumer, which retries a failed operation three times beyond its first try
// and clamps the value at zero so it can never go negative.
const (
	DefaultMaxRetries     = 3
	DefaultInitialBackoff = time.Second
	DefaultMaxBackoff     = time.Minute
	DefaultLease          = 5 * time.Minute
	DefaultConcurrency    = 4
	DefaultPollInterval   = time.Second
	// DefaultBackpressureDefer is how long a task the store shed under
	// backpressure is held before its next attempt, matching the reference's
	// DEFAULT_BACKPRESSURE_DEFER_SECONDS.
	DefaultBackpressureDefer = 120 * time.Second
)

// Config tunes the consumer.
type Config struct {
	// WorkerID identifies this consumer in the queue. It must be unique among
	// consumers sharing a queue so a claim can be traced back to its holder.
	WorkerID string
	// MaxRetries is the attempt budget beyond the first try. It is clamped at
	// zero, so a negative value means no retry.
	MaxRetries int
	// InitialBackoff is the first retry delay.
	InitialBackoff time.Duration
	// MaxBackoff is the backoff ceiling.
	MaxBackoff time.Duration
	// Lease bounds how long a claim stays valid before another consumer may
	// reclaim the work, which is how a consumer that died is recovered.
	Lease time.Duration
	// Concurrency is how many operations this consumer processes at once.
	Concurrency int
	// PollInterval is how long to wait before retrying an empty claim.
	PollInterval time.Duration
	// Logger receives the operator-visible progress lines.
	Logger *slog.Logger
	// Now is injectable so backoff is testable without real waiting.
	Now func() time.Time
	// Sleep is injectable so backoff is testable without real waiting.
	Sleep func(time.Duration)
	// WallTimeouts bounds one operation type's wall-clock budget. A type absent
	// from the map is bounded only by the claim's lease.
	WallTimeouts map[string]time.Duration
	// ReservedSlots reserves a floor of the consumer's concurrency for a given
	// operation type, so a bank mid-bulk-ingest cannot starve it.
	ReservedSlots map[string]int
	// BackpressureDefer is how long a task the store shed under backpressure is
	// held before its next attempt. It applies only where a deferral already
	// happens: an operation whose processor named an explicit delay keeps that
	// delay.
	BackpressureDefer time.Duration
	// ConsolidationByBank is the reference's bank-priority list, spelled
	// "bank-pattern:priority,...". When set, a consolidation claim prefers the
	// banks matching a higher-priority pattern.
	ConsolidationByBank string
}

// Processor performs the work of one operation. It is supplied by the caller so
// the consumer owns the queue lifecycle while the work itself stays with the
// area that understands it.
type Processor interface {
	// Process runs the operation's work. A permanent failure must be reported
	// as a PermanentError so it does not consume the retry budget.
	Process(ctx context.Context, op storage.Operation) error
}

// ProcessorFunc adapts a function to the Processor interface.
type ProcessorFunc func(ctx context.Context, op storage.Operation) error

// Process implements Processor.
func (f ProcessorFunc) Process(ctx context.Context, op storage.Operation) error { return f(ctx, op) }

// PermanentError marks a failure that must not be retried: the same input would
// earn the same rejection, so retrying only wastes the budget.
type PermanentError struct{ Cause error }

// Error implements error.
func (e *PermanentError) Error() string {
	if e.Cause == nil {
		return "permanent failure"
	}
	return e.Cause.Error()
}

// Unwrap exposes the cause so callers can inspect it.
func (e *PermanentError) Unwrap() error { return e.Cause }

// DeferralError marks a failure that should be deferred rather than counted
// against the retry budget: the work itself is fine, but a transient external
// condition (a provider rate limit, a saturated store) says "try again later".
type DeferralError struct{ Cause error }

func (e *DeferralError) Error() string { return e.Cause.Error() }
func (e *DeferralError) Unwrap() error { return e.Cause }

// Defer marks an error as a deferral.
func Defer(err error) error {
	if err == nil {
		return nil
	}
	return &DeferralError{Cause: err}
}

// IsDeferral reports whether an error is marked deferrable.
func IsDeferral(err error) bool {
	var target *DeferralError
	return errors.As(err, &target)
}

// deferredAtError is a deferral that names its own delay.
type deferredAtError struct {
	delay time.Duration
	cause error
}

func (e *deferredAtError) Error() string { return e.cause.Error() }
func (e *deferredAtError) Unwrap() error { return e.cause }

// DeferAt marks an error as deferrable with an explicit retry delay, for a
// schedule the caller controls (a webhook's backoff ladder, say).
func DeferAt(delay time.Duration, err error) error {
	if err == nil {
		return nil
	}
	return &deferredAtError{delay: delay, cause: err}
}

// deferralDelay returns the deferral's explicit delay, if it named one.
func deferralDelay(err error) (time.Duration, bool) {
	var target *deferredAtError
	if errors.As(err, &target) {
		return target.delay, true
	}
	return 0, false
}

// isDeferral reports whether an error asks for a deferral, explicit or not.
func isDeferral(err error) bool {
	return IsDeferral(err) || isExplicitDeferral(err)
}

// isExplicitDeferral reports whether the error carries an explicit delay.
func isExplicitDeferral(err error) bool {
	_, ok := deferralDelay(err)
	return ok
}

// Permanent marks an error as non-retryable.
func Permanent(err error) error {
	if err == nil {
		return nil
	}
	return &PermanentError{Cause: err}
}

// IsPermanent reports whether an error is marked non-retryable.
func IsPermanent(err error) bool {
	var target *PermanentError
	return errors.As(err, &target)
}

// Consumer drains the operation queue.
type Consumer struct {
	store     storage.Store
	processor Processor
	cfg       Config

	// auditor, when non-nil, records each processed operation on the trail.
	auditor Auditor

	wg     sync.WaitGroup
	stopCh chan struct{}
	once   sync.Once

	inflightMu sync.Mutex
	inflight   map[string]int
	// pollMu guards lastPoll, the time of the last completed claim cycle that
	// the worker liveness probe reports the age of.
	pollMu   sync.Mutex
	lastPoll time.Time
	// consolidationPriority is the parsed ConsolidationByBank list, applied to
	// consolidation claims. It is empty when the deployment configured none.
	consolidationPriority map[string]int
}

// Auditor decides and records system-transport audit entries. It is satisfied
// by *audit.Logger.
type Auditor interface {
	ShouldAudit(ctx context.Context, action, bankID string) bool
	Record(ctx context.Context, entry audit.Entry)
}

// WithAuditor wires the audit logger the consumer records processed operations
// through. It returns the consumer so composition can chain the call.
func (c *Consumer) WithAuditor(auditor Auditor) *Consumer {
	c.auditor = auditor
	return c
}

// New builds a consumer over the store and processor, applying defaults to any
// unset tuning value.
func New(store storage.Store, processor Processor, cfg Config) *Consumer {
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	if cfg.InitialBackoff <= 0 {
		cfg.InitialBackoff = DefaultInitialBackoff
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = DefaultMaxBackoff
	}
	if cfg.MaxBackoff < cfg.InitialBackoff {
		cfg.MaxBackoff = cfg.InitialBackoff
	}
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = DefaultConcurrency
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = DefaultPollInterval
	}
	if cfg.BackpressureDefer <= 0 {
		cfg.BackpressureDefer = DefaultBackpressureDefer
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	return &Consumer{
		store: store, processor: processor, cfg: cfg,
		consolidationPriority: ParseBankPriority(cfg.ConsolidationByBank),
		stopCh:                make(chan struct{}),
	}
}

// ParseBankPriority reads the reference's "bank-pattern:priority,..." list into
// a pattern-to-priority map. "*" in a pattern is kept as-is; the claim layers
// translate it to their own wildcard. A blank entry, an entry with no colon, an
// empty pattern or an unparseable or non-positive priority is dropped rather
// than failing the consumer, matching the defensive reading the rest of the
// configuration surface applies to a value carried as a plain string.
func ParseBankPriority(raw string) map[string]int {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}
	priority := map[string]int{}
	for _, entry := range strings.Split(trimmed, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		colon := strings.LastIndex(entry, ":")
		if colon < 0 {
			continue
		}
		pattern := strings.TrimSpace(entry[:colon])
		value, err := strconv.Atoi(strings.TrimSpace(entry[colon+1:]))
		if pattern == "" || err != nil || value < 1 {
			continue
		}
		priority[pattern] = value
	}
	if len(priority) == 0 {
		return nil
	}
	return priority
}

// BackoffFor returns the delay before the given retry attempt, counted from
// one. The delay doubles from the initial value and is clamped at the ceiling,
// so it never grows without bound.
func (c *Consumer) BackoffFor(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := c.cfg.InitialBackoff
	for i := 1; i < attempt; i++ {
		delay *= 2
		if delay >= c.cfg.MaxBackoff {
			return c.cfg.MaxBackoff
		}
	}
	if delay > c.cfg.MaxBackoff {
		return c.cfg.MaxBackoff
	}
	return delay
}

// Run drains the queue until the context is cancelled or Stop is called. It
// returns when every in-flight operation has been settled or handed back.
func (c *Consumer) Run(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Stop must also release a Run blocked in its poll wait.
	go func() {
		select {
		case <-c.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	// A shutdown must hand back the work this consumer still holds, so another
	// consumer picks it up immediately instead of waiting out the lease.
	defer c.releaseHeldWork()

	for i := 0; i < c.cfg.Concurrency; i++ {
		c.wg.Add(1)
		go func(slot int) {
			defer c.wg.Done()
			c.loop(ctx, slot)
		}(i)
	}
	c.wg.Wait()
	return ctx.Err()
}

// LastPoll reports the time of the last completed claim cycle, or nil before
// the first one. The worker liveness probe reports its age for alerting, so a
// stalled consumer is visible without a restart.
func (c *Consumer) LastPoll() *time.Time {
	c.pollMu.Lock()
	defer c.pollMu.Unlock()
	if c.lastPoll.IsZero() {
		return nil
	}
	last := c.lastPoll
	return &last
}

// stampPoll records that a claim cycle completed just now.
func (c *Consumer) stampPoll() {
	c.pollMu.Lock()
	c.lastPoll = c.cfg.Now()
	c.pollMu.Unlock()
}

// Stop asks the consumer to finish and hand back its work.
func (c *Consumer) Stop() {
	c.once.Do(func() { close(c.stopCh) })
}

// loop claims and processes operations until the context ends.
func (c *Consumer) loop(ctx context.Context, slot int) {
	for {
		if ctx.Err() != nil {
			return
		}
		op, err := c.claim(ctx)
		// A claim cycle that completed — an operation taken, or an empty batch —
		// is what the liveness probe ages; a failed claim is not a completed
		// cycle, so it leaves the stamp alone.
		if err == nil || errors.Is(err, storage.ErrOperationNotFound) {
			c.stampPoll()
		}
		if err != nil {
			if errors.Is(err, storage.ErrOperationNotFound) {
				c.wait(ctx)
				continue
			}
			if ctx.Err() != nil {
				return
			}
			c.cfg.Logger.Warn("claim failed", "slot", slot, "error", err)
			c.wait(ctx)
			continue
		}
		c.handle(ctx, op)
		c.trackInflight(op.OperationType, -1)
	}
}

// claim takes the next operation, honouring per-type reserved slots: a type
// with a reserved floor is claimed first while the consumer's in-flight count
// for it is below the reservation, and any work is claimed otherwise.
func (c *Consumer) claim(ctx context.Context) (storage.Operation, error) {
	if preferred := c.preferredTypes(); len(preferred) > 0 {
		op, err := c.claimPreferred(ctx, preferred)
		if err == nil {
			c.trackInflight(op.OperationType, 1)
			return op, nil
		}
		if !errors.Is(err, storage.ErrOperationNotFound) {
			return storage.Operation{}, err
		}
	}
	op, err := c.store.ClaimNextOperation(ctx, c.workerID(), c.cfg.Lease)
	if err == nil {
		c.trackInflight(op.OperationType, 1)
	}
	return op, err
}

// claimPreferred claims from the reserved types, and — when a bank priority is
// configured — takes the consolidation pool first, ordered by that priority. It
// is the reference's tiered consolidation claim: consolidation is claimed from
// its own pool, ahead of the shared types, so a busy bank's priority governs
// which consolidation runs next.
func (c *Consumer) claimPreferred(ctx context.Context, preferred []string) (storage.Operation, error) {
	if len(c.consolidationPriority) > 0 && containsType(preferred, storage.OperationTypeConsolidate) {
		if claimer, ok := c.store.(storage.ConsolidationPriorityClaimer); ok {
			op, err := claimer.ClaimConsolidationByPriority(ctx, c.workerID(), c.cfg.Lease, c.consolidationPriority)
			if err == nil {
				return op, nil
			}
			if !errors.Is(err, storage.ErrOperationNotFound) {
				return storage.Operation{}, err
			}
		}
	}
	return c.store.ClaimNextOperationForTypes(ctx, c.workerID(), c.cfg.Lease, preferred)
}

// containsType reports whether a slice names an operation type.
func containsType(types []string, want string) bool {
	for _, opType := range types {
		if opType == want {
			return true
		}
	}
	return false
}

// preferredTypes lists the reserved types whose floor is not yet met by the
// consumer's in-flight work.
func (c *Consumer) preferredTypes() []string {
	if len(c.cfg.ReservedSlots) == 0 {
		return nil
	}
	c.inflightMu.Lock()
	defer c.inflightMu.Unlock()
	preferred := make([]string, 0, len(c.cfg.ReservedSlots))
	for opType, reserved := range c.cfg.ReservedSlots {
		if reserved > 0 && c.inflight[opType] < reserved {
			preferred = append(preferred, opType)
		}
	}
	sort.Strings(preferred)
	return preferred
}

// trackInflight adjusts the in-flight count for one operation type.
func (c *Consumer) trackInflight(opType string, delta int) {
	c.inflightMu.Lock()
	defer c.inflightMu.Unlock()
	if c.inflight == nil {
		c.inflight = map[string]int{}
	}
	c.inflight[opType] += delta
}

// handle runs one claimed operation and settles its outcome.
func (c *Consumer) handle(ctx context.Context, op storage.Operation) {
	// Execution-start guard: re-read the operation and abort when it is gone or
	// cancelled before any work runs. A bank deleted after the claim takes its
	// queued operations with it, and running one of them would resurrect the
	// bank through the write path's lazy create. This is the reference's
	// _check_op_alive at task dispatch; a store that cannot answer is treated as
	// alive, so the guard never blocks a live operation.
	if !c.operationAlive(ctx, op) {
		c.cfg.Logger.Info("operation is gone or cancelled, skipping",
			"operation_id", op.OperationID, "bank_id", op.BankID, "type", op.OperationType)
		return
	}
	// The operation continues under a background context so a shutdown drains
	// it rather than leaving it half-applied. It is bounded by the lease, and
	// more tightly by a per-type wall ceiling when one is configured: a wedged
	// task then frees its slot at the ceiling rather than at the lease.
	budget := c.cfg.Lease
	if ceiling := c.cfg.WallTimeouts[op.OperationType]; ceiling > 0 && ceiling < budget {
		budget = ceiling
	}
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), budget)
	defer cancel()

	// The sink collects whatever the processor reports about this run. It is
	// read only after Process returns, so no lock is needed, and a processor
	// that reports nothing leaves it empty and takes the plain path below.
	sink := &outcomeSink{}
	workCtx = context.WithValue(workCtx, outcomeKey{}, sink)

	// The audit decision and the entry's start are taken before the work runs,
	// so the recorded duration brackets the operation itself.
	audited := c.auditor != nil && c.auditor.ShouldAudit(ctx, op.OperationType, op.BankID)
	started := time.Now().UTC()

	err := c.processor.Process(workCtx, op)

	if audited {
		entry := audit.Entry{
			Action:    op.OperationType,
			Transport: audit.TransportSystem,
			BankID:    op.BankID,
			StartedAt: started,
			EndedAt:   time.Now().UTC(),
			Request:   op.Payload,
		}
		if err != nil {
			entry.Error = err.Error()
		} else if len(sink.metadata) > 0 {
			entry.Response = sink.metadata
		}
		// Recording must outlive the shutdown context that may have cancelled
		// the work, so it drops that cancellation.
		c.auditor.Record(context.WithoutCancel(ctx), entry)
	}

	if err == nil {
		c.complete(ctx, op, sink.metadata)
		c.recordOperation(op, true, time.Since(started))
		return
	}
	if errors.Is(err, storage.ErrOperationAborted) {
		// The operation was cancelled or removed mid-run: there is no row to
		// settle, so this is a clean stop rather than a failure to record.
		c.cfg.Logger.Info("operation stopped: its record is gone or cancelled",
			"operation_id", op.OperationID, "bank_id", op.BankID)
		return
	}
	if IsPermanent(err) {
		// A permanent failure is terminal on the first try; it must not consume
		// the retry budget.
		c.fail(ctx, op, err.Error(), sink.metadata)
		c.recordOperation(op, false, time.Since(started))
		return
	}
	if isBackpressure(err) || isDeferral(err) {
		// A store that is shedding load, or a handler that asked for a deferral
		// (a provider rate limit, say), is a transient condition, not a failure
		// of this operation: defer it without consuming the retry budget, so a
		// sustained condition does not permanently fail work that would clear.
		// The attempt's report is kept: a deferral did work enough to name a
		// reason (a webhook's receiver status, an explicit retry delay), and
		// that is exactly what an operator reads across a retry.
		c.deferOperation(ctx, op, err, sink.metadata)
		return
	}
	c.retry(ctx, op, err, sink.metadata, started)
}

// recordOperation reports one terminally-settled operation to the metric
// registry: its duration and count, under the worker source. Only a terminal
// outcome is counted — a deferral, or a retry that is still scheduled, is not a
// completion and must not appear as one.
func (c *Consumer) recordOperation(op storage.Operation, success bool, duration time.Duration) {
	monitoring.Workload.ObserveOperation(metricOperationLabel(op.OperationType), "worker", "", "", success, duration.Seconds())
}

// metricOperationLabel names an operation for the operation-duration and
// operation-total families. The reference collapses every retain-shaped type
// into one "retain" series and reports any other type by its own name, so a
// batch retain and a file-convert retain read as the same workload.
func metricOperationLabel(operationType string) string {
	switch operationType {
	case storage.OperationTypeRetain, storage.AsyncRetainType, storage.OperationTypeFileConvertRetain:
		return "retain"
	case "":
		return "unknown"
	default:
		return operationType
	}
}

// isBackpressure reports whether a failure is the store shedding load rather
// than the operation itself failing.
func isBackpressure(err error) bool {
	return errors.Is(err, storage.ErrWritesUnavailable) || errors.Is(err, storage.ErrReadShed)
}

// operationAlive reports whether a claimed operation is still worth running. A
// store that cannot answer the question — one without the liveness capability —
// is treated as alive, so the guard is never the reason live work stops. A read
// error is likewise treated as alive, matching the reference's "assume alive to
// avoid false-positive aborts".
func (c *Consumer) operationAlive(ctx context.Context, op storage.Operation) bool {
	checker, ok := c.store.(storage.OperationLiveness)
	if !ok {
		return true
	}
	alive, err := checker.OperationAlive(ctx, op.BankID, op.OperationID)
	if err != nil {
		return true
	}
	return alive
}

// deferOperation reschedules an operation without consuming its retry budget,
// so backpressure does not walk work toward the permanent-failure cliff.
//
// It keeps whatever the processor reported about the attempt, when the store can
// carry it, so a deferral does not throw away the one thing an operator needs to
// tell "the endpoint is refusing us" from "we never reached it". A processor
// that reported nothing takes the plain scheduling path unchanged.
func (c *Consumer) deferOperation(ctx context.Context, op storage.Operation, cause error, metadata map[string]any) {
	// A deferral under backpressure is held for the configured interval: the
	// delay is how long the store needs to catch up, not how many attempts the
	// work has already spent, so it does not walk a retry ladder tuned for a
	// failure. A processor that named its own delay — a webhook's backoff ladder,
	// a provider's rate-limit reset — keeps it, and any other deferral keeps the
	// ordinary backoff.
	delay := c.BackoffFor(op.RetryCount + 1)
	if isBackpressure(cause) {
		delay = c.cfg.BackpressureDefer
	}
	if explicit, ok := deferralDelay(cause); ok && explicit > 0 {
		delay = explicit
	}
	next := c.cfg.Now().Add(delay)
	// A store that can reschedule without recording a failure takes that path;
	// one that cannot falls back to the retry write, which keeps the deferral
	// scheduled at the cost of a message on the record.
	if deferrer, ok := c.store.(storage.OperationDeferrer); ok {
		if err := deferrer.DeferOperation(ctx, op.OperationID, next, metadata); err != nil {
			c.reportSettleLapse(op, "defer", err)
			return
		}
	} else if err := c.scheduleRetry(ctx, op, op.RetryCount, next, cause.Error(), metadata); err != nil {
		c.reportSettleLapse(op, "defer", err)
		return
	}
	// The refusal reason belongs in the log, not in the error column: a
	// deferral is backpressure rather than a failure, and a caller that waits
	// for a bank to settle reads a pending operation carrying an error as
	// already lost. It is logged only once the reschedule has applied, so a
	// deferral whose row is gone does not claim to have been scheduled.
	c.cfg.Logger.Info("operation deferred under backpressure",
		"operation_id", op.OperationID, "next_retry_at", next, "reason", cause.Error())
}

// retry schedules another attempt, or fails the operation once the attempt
// budget is spent.
func (c *Consumer) retry(ctx context.Context, op storage.Operation, cause error, metadata map[string]any, started time.Time) {
	attempt := op.RetryCount + 1
	if attempt > c.cfg.MaxRetries {
		c.fail(ctx, op, fmt.Sprintf("retry budget exhausted after %d attempt(s): %v", op.RetryCount, cause), metadata)
		c.recordOperation(op, false, time.Since(started))
		return
	}

	// Record the failed attempt and the next attempt time, then return the
	// operation to the claimable pool. The schedule write itself releases the
	// claim: ScheduleOperationRetry clears its holder and flips it back to
	// pending, so a second release would find no claim to release and fail with
	// a conflict — noise on the log, and it would mask the recorded cause.
	next := c.cfg.Now().Add(c.BackoffFor(attempt))
	if err := c.store.ScheduleOperationRetry(ctx, op.OperationID, op.RetryCount+1, next, cause.Error()); err != nil {
		c.reportSettleLapse(op, "schedule-retry", err)
		return
	}
	c.cfg.Logger.Info("operation scheduled for retry",
		"operation_id", op.OperationID, "attempt", attempt, "next_retry_at", next)
}

// wait pauses before the next empty poll, waking early on shutdown.
func (c *Consumer) wait(ctx context.Context) {
	timer := time.NewTimer(c.cfg.PollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-c.stopCh:
	case <-timer.C:
	}
}

// releaseHeldWork hands back every operation this consumer still holds, so a
// clean shutdown does not strand work for the length of the lease.
func (c *Consumer) releaseHeldWork() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	page, err := c.store.ListOperations(ctx, storage.OperationQuery{
		Status:   string(storage.StatusProcessing),
		WorkerID: c.workerID(),
	})
	if err != nil {
		c.cfg.Logger.Warn("could not list held work on shutdown", "error", err)
		return
	}
	for _, op := range page.Operations {
		if op.WorkerID != c.workerID() {
			continue
		}
		if err := c.store.ReleaseOperationClaim(ctx, op.OperationID, c.workerID()); err != nil {
			c.cfg.Logger.Warn("could not release held work", "operation_id", op.OperationID, "error", err)
			continue
		}
		c.cfg.Logger.Info("released claimed work on shutdown", "operation_id", op.OperationID)
	}
}

// workerID returns the configured identity, falling back to a stable default.
func (c *Consumer) workerID() string {
	if c.cfg.WorkerID != "" {
		return c.cfg.WorkerID
	}
	return "worker"
}
