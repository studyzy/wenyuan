package worker

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// syncBuffer is a log sink safe to read after the consumer's goroutines stop.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runForClaim starts a consumer and lets one empty poll cycle happen, then stops
// it. The claim loop is what the warning belongs to, so this drives the real path.
func runForClaim(t *testing.T, cfg Config) string {
	t.Helper()
	var sink syncBuffer
	cfg.Logger = slog.New(slog.NewTextHandler(&sink, nil))
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.PollInterval <= 0 {
		cfg.PollInterval = time.Millisecond
	}
	if cfg.Lease <= 0 {
		cfg.Lease = time.Hour
	}
	store := newStore(t)
	consumer := New(store, ProcessorFunc(func(context.Context, storage.Operation) error { return nil }), cfg)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = consumer.Run(ctx)
		close(done)
	}()
	time.Sleep(30 * time.Millisecond)
	cancel()
	<-done
	return sink.String()
}

// An unconfigured worker id is a shared identity across every worker in the
// deployment, and the shutdown release selects its rows by that id — so one
// worker's shutdown can hand back another's in-flight operations. The reference
// warns about the same shape (warn_if_container_default_worker_id,
// worker/main.py:246); this service warns when the id was never configured.
//
// Falsification: drop the `warnAboutDefaultWorkerID` call from `Run` and this
// FAILs — nothing is logged.
func TestDefaultWorkerIDWarnsAtStartup(t *testing.T) {
	logged := runForClaim(t, Config{})
	if !strings.Contains(logged, "worker id is not configured") {
		t.Fatalf("an unconfigured worker id must warn at startup, got:\n%s", logged)
	}
	if !strings.Contains(logged, "WENYUAN_API_WORKER_ID") {
		t.Fatalf("the warning must name the knob that fixes it, got:\n%s", logged)
	}
}

// A configured id is the whole fix, so it must be silent — a warning that fires
// on every correctly-configured deployment is noise an operator learns to ignore.
func TestConfiguredWorkerIDDoesNotWarn(t *testing.T) {
	logged := runForClaim(t, Config{WorkerID: "worker-7"})
	if strings.Contains(logged, "worker id is not configured") {
		t.Fatalf("a configured worker id must not warn, got:\n%s", logged)
	}
}

// The fallback identity is still the stable constant, so a single worker
// deployment keeps a fixed id across restarts (which is what makes reclaiming its
// own abandoned rows possible at all).
func TestDefaultWorkerIDIsStable(t *testing.T) {
	consumer := New(newStore(t), ProcessorFunc(func(context.Context, storage.Operation) error { return nil }), Config{
		Logger: quietLogger(),
	})
	if got := consumer.workerID(); got != "worker" {
		t.Fatalf("default worker id = %q, want the stable %q", got, "worker")
	}
}
