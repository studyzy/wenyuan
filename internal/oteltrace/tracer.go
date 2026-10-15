package oteltrace

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Tracer emits spans to an OTLP endpoint without blocking the traced work.
//
// A model call is on the latency path, so a span is queued and exported by a
// background worker rather than posted inline. The queue is bounded: a backend
// that stops answering must not grow the process's memory, so an overflow drops
// the span and counts it, which is the behaviour a tracing outage should have.
type Tracer struct {
	exporter *exporter
	queue    chan Span
	logger   *slog.Logger

	mu     sync.Mutex
	closed bool
	wg     sync.WaitGroup

	dropped int64
}

// Option tunes a tracer.
type Option func(*tracerConfig)

type tracerConfig struct {
	queueSize int
	logger    *slog.Logger
}

// WithQueueSize sets how many finished spans may wait to be exported.
func WithQueueSize(size int) Option {
	return func(c *tracerConfig) {
		if size > 0 {
			c.queueSize = size
		}
	}
}

// WithLogger sets where export failures are reported.
func WithLogger(logger *slog.Logger) Option {
	return func(c *tracerConfig) {
		if logger != nil {
			c.logger = logger
		}
	}
}

// defaultQueueSize is how many spans may wait for the exporter.
const defaultQueueSize = 512

// New builds a tracer and starts its export worker.
func New(cfg Config, opts ...Option) *Tracer {
	resolved := tracerConfig{queueSize: defaultQueueSize, logger: slog.Default()}
	for _, opt := range opts {
		opt(&resolved)
	}
	tracer := &Tracer{
		exporter: newExporter(cfg),
		queue:    make(chan Span, resolved.queueSize),
		logger:   resolved.logger,
	}
	tracer.wg.Add(1)
	go tracer.run()
	return tracer
}

// run drains the queue in batches so a burst of spans costs one request.
func (t *Tracer) run() {
	defer t.wg.Done()
	batch := make([]Span, 0, 32)
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for {
		select {
		case span, ok := <-t.queue:
			if !ok {
				t.flushBatch(batch)
				return
			}
			batch = append(batch, span)
			if len(batch) >= cap(batch) {
				t.flushBatch(batch)
				batch = batch[:0]
			}
		case <-timer.C:
			if len(batch) > 0 {
				t.flushBatch(batch)
				batch = batch[:0]
			}
			timer.Reset(time.Second)
		}
	}
}

// flushBatch exports a batch and reports a failure without retrying, so a
// broken backend cannot build an unbounded retry backlog.
func (t *Tracer) flushBatch(batch []Span) {
	if len(batch) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := t.exporter.export(ctx, batch); err != nil {
		t.logger.Warn("otlp export failed", "spans", len(batch), "error", err)
	}
}

// Emit queues a finished span. A full queue drops it.
func (t *Tracer) Emit(span Span) {
	if t == nil {
		return
	}
	t.mu.Lock()
	closed := t.closed
	t.mu.Unlock()
	if closed {
		return
	}
	select {
	case t.queue <- span:
	default:
		t.mu.Lock()
		t.dropped++
		t.mu.Unlock()
	}
}

// Dropped reports how many spans were shed because the queue was full.
func (t *Tracer) Dropped() int64 {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.dropped
}

// Close flushes queued spans and stops the worker.
func (t *Tracer) Close() {
	if t == nil {
		return
	}
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return
	}
	t.closed = true
	t.mu.Unlock()
	close(t.queue)
	t.wg.Wait()
}
