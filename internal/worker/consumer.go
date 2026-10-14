// Package worker consumes queued asynchronous operations. It claims, processes,
// and completes or retries them, reusing the storage area's operation
// interface rather than reaching the queue table directly.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

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

	wg     sync.WaitGroup
	stopCh chan struct{}
	once   sync.Once
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
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = func() time.Time { return time.Now().UTC() }
	}
	if cfg.Sleep == nil {
		cfg.Sleep = time.Sleep
	}
	return &Consumer{store: store, processor: processor, cfg: cfg, stopCh: make(chan struct{})}
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
		op, err := c.store.ClaimNextOperation(ctx, c.workerID(), c.cfg.Lease)
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
	}
}

// handle runs one claimed operation and settles its outcome.
func (c *Consumer) handle(ctx context.Context, op storage.Operation) {
	// The operation continues under a background context so a shutdown drains
	// it rather than leaving it half-applied. It is bounded by the lease.
	workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.cfg.Lease)
	defer cancel()

	err := c.processor.Process(workCtx, op)
	if err == nil {
		c.complete(ctx, op)
		return
	}
	if IsPermanent(err) {
		// A permanent failure is terminal on the first try; it must not consume
		// the retry budget.
		c.fail(ctx, op, err.Error())
		return
	}
	c.retry(ctx, op, err)
}

// complete marks an operation succeeded.
func (c *Consumer) complete(ctx context.Context, op storage.Operation) {
	if _, err := c.store.AdvanceOperation(ctx, op.OperationID, storage.StatusCompleted, ""); err != nil {
		c.cfg.Logger.Error("complete failed", "operation_id", op.OperationID, "error", err)
		return
	}
	c.cfg.Logger.Info("operation completed", "operation_id", op.OperationID, "bank_id", op.BankID)
}

// fail marks an operation terminally failed.
func (c *Consumer) fail(ctx context.Context, op storage.Operation, detail string) {
	if _, err := c.store.AdvanceOperation(ctx, op.OperationID, storage.StatusFailed, detail); err != nil {
		c.cfg.Logger.Error("fail failed", "operation_id", op.OperationID, "error", err)
		return
	}
	c.cfg.Logger.Warn("operation failed permanently", "operation_id", op.OperationID, "detail", detail)
}

// retry schedules another attempt, or fails the operation once the attempt
// budget is spent.
func (c *Consumer) retry(ctx context.Context, op storage.Operation, cause error) {
	attempt := op.RetryCount + 1
	if attempt > c.cfg.MaxRetries {
		c.fail(ctx, op, fmt.Sprintf("retry budget exhausted after %d attempt(s): %v", op.RetryCount, cause))
		return
	}

	// Record the failed attempt and the next attempt time, then return the
	// operation to the claimable pool. The claim is released so another
	// consumer may take it when the gate passes.
	next := c.cfg.Now().Add(c.BackoffFor(attempt))
	if err := c.store.ScheduleOperationRetry(ctx, op.OperationID, op.RetryCount+1, next, cause.Error()); err != nil {
		c.cfg.Logger.Error("retry scheduling failed", "operation_id", op.OperationID, "error", err)
		return
	}
	if err := c.store.ReleaseOperationClaim(ctx, op.OperationID, c.workerID()); err != nil {
		c.cfg.Logger.Error("claim release failed", "operation_id", op.OperationID, "error", err)
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
