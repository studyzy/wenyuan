// Command wenyuan-worker is the process entrypoint for the background work:
// the queue consumer and the maintenance loop. It resolves configuration the
// same way the server does, opens the same storage backend, and runs both until
// it is asked to stop.
//
// It shares the server's configuration resolution and storage selection rather
// than duplicating them, so a deployment configures both entrypoints
// identically and only the process role differs.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/maintenance"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
	"github.com/studyzy/wenyuan/internal/worker"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("worker failed", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	flags := flag.NewFlagSet("wenyuan-worker", flag.ContinueOnError)
	var (
		logLevel    = flags.String("log-level", "", "log level")
		workerID    = flags.String("worker-id", "", "consumer identity (overrides the environment)")
		concurrency = flags.Int("concurrency", 0, "simultaneous operations (overrides the environment)")
		maxRetries  = flags.Int("max-retries", -1, "attempt budget beyond the first try (overrides the environment)")
	)
	if err := flags.Parse(args); err != nil {
		return err
	}

	if err := config.LoadDotenvForEntrypoint(); err != nil {
		return err
	}
	cfg := config.FromEnvironment()
	slog.SetDefault(slog.New(monitoring.NewLogHandler(cfg)))
	cfg = config.ApplyFlags(cfg, config.FlagValues{LogLevel: optionalString(flags, "log-level", logLevel)})
	applyWorkerFlags(&cfg.Worker, flags, workerID, concurrency, maxRetries)

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevelFor(cfg.LogLevel)})))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// The worker shares the server's storage resolution, so it sees exactly the
	// same backend, schema, and migrations.
	store, err := app.OpenStore(ctx, cfg, app.Dependencies{})
	if err != nil {
		return err
	}
	if closer, ok := store.(interface{ Close() error }); ok {
		defer func() { _ = closer.Close() }()
	}

	slog.Info("worker starting",
		"worker_id", cfg.Worker.ID,
		"concurrency", cfg.Worker.Concurrency,
		"max_retries", cfg.Worker.MaxRetries,
		"lease_seconds", int(cfg.Worker.Lease.Seconds()))

	// The worker shares the server's provider resolution, so a queued
	// consolidation pass and a queued mental-model refresh call the same model
	// the server would.
	model, err := app.ResolveProvider(cfg)
	if err != nil {
		return err
	}
	// The worker records its model calls in the same trace the server writes,
	// so a queued consolidation or refresh is visible to /llm-requests too.
	model = app.TraceProvider(model, store, cfg.Provider)
	// Record the worker's completions in the same metric registry the server
	// fills, so a queued consolidation or refresh reports its LLM calls too.
	model = provider.MetricsProvider(model, cfg.Provider.Model)
	app.InstallRecallPhaseObserver(store)
	app.InstallRetainPhaseObserver()
	reflection := app.NewReflectRunner(model)

	// The worker resolves the embedding backend the same way the server does,
	// so queued writes embed with the deployment's configured provider instead
	// of falling back to the non-vector recall arms.
	embedder, err := app.ResolveEmbedder(cfg)
	if err != nil {
		return err
	}

	// The maintenance loop runs beside the consumer rather than in the server,
	// so the housekeeping work and the queued work share one process's
	// lifetime: a deployment that runs a worker gets the maintenance without
	// having to run the API too.
	//
	// It is started in every worker process with no leader election, which is
	// safe because each job's work is collision-proof rather than because one
	// process is chosen.
	tenants, err := app.OpenTenantExtension(ctx, cfg, store)
	if err != nil {
		return err
	}
	loop := app.NewMaintenanceLoop(store, model, reflection, tenants, cfg.Maintenance, cfg.VectorIndex)
	if loop.Enabled() {
		slog.Info("maintenance enabled",
			"jobs", len(loop.Jobs),
			"jitter_seconds", int(cfg.Maintenance.StartJitter.Seconds()))
	}

	consumer := app.NewQueueConsumer(store, model, reflection, app.EmbeddingGeneratorFrom(embedder), cfg.Worker, cfg.WorkerTuning, app.NewWebhookGuard(cfg), app.ExtractionSettingsFrom(cfg), !cfg.StoreDocumentText, cfg.DatabaseSchema, tenants).
		WithAuditor(app.NewAuditLogger(cfg, store))
	startWorkerHTTP(ctx, cfg, store, consumer, tenants)
	return runBoth(ctx, consumer, loop)
}

// workerPinger adapts the store's readiness probe to the monitoring boundary.
type workerPinger struct{ store storage.Store }

func (p workerPinger) Ping(r *http.Request) error { return p.store.Ready(r.Context()) }

// PoolWaiting forwards the store's pooled-acquire waiter count, so the
// worker's readiness payload carries db_pool_waiting exactly as the API's does
// — both read the same pool instrumentation in the reference.
func (p workerPinger) PoolWaiting() (int, bool) {
	reporter, ok := p.store.(interface {
		PoolWaiting() (int, bool)
	})
	if !ok {
		return 0, false
	}
	return reporter.PoolWaiting()
}

// startWorkerHTTP serves the worker's HTTP surface on its own port: the root
// info endpoint, the health probes, and the metrics exposition. An orchestrator
// probes the worker exactly as it probes the API. A non-positive port disables
// the surface.
func startWorkerHTTP(ctx context.Context, cfg config.Config, store storage.Store, consumer *worker.Consumer, tenants tenant.Extension) {
	if cfg.Worker.HTTPPort <= 0 {
		return
	}
	handler := &monitoring.WorkerHandler{
		HealthHandler: monitoring.HealthHandler{DB: workerPinger{store: store}, Version: httpx.APIVersion, Started: time.Now()},
		WorkerID:      cfg.Worker.ID,
		IsShutdown:    func() bool { return ctx.Err() != nil },
		LastPoll:      consumer.LastPoll,
	}
	mux := http.NewServeMux()
	bind := func(method, pattern string, h http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, h)
	}
	monitoring.BindWorkerRoutes(handler, bind)
	monitoring.BindMetricsRoutes(app.WorkerMetricsHandler(store, cfg, tenants), bind)
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", cfg.Worker.HTTPPort),
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("worker health surface failed", "error", err)
		}
	}()
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdownCtx)
	}()
}

// runBoth drives the consumer and the maintenance loop together, returning
// when the context ends.
//
// They share one context so a shutdown stops both: a loop still sweeping while
// the queue drains would keep the process alive past its stop, and the reverse
// would abandon queued work.
//
// The consumer's error is the one reported, because a queue failure is what a
// deployment needs to hear about; a maintenance failure is logged per job and
// retried on the next tick rather than being fatal.
func runBoth(ctx context.Context, consumer *worker.Consumer, loop *maintenance.Loop) error {
	loopDone := make(chan error, 1)
	go func() { loopDone <- app.RunMaintenanceLoop(ctx, loop) }()

	consumerErr := app.RunQueueConsumer(ctx, consumer)
	if err := <-loopDone; err != nil && consumerErr == nil {
		return err
	}
	return consumerErr
}

// applyWorkerFlags layers the supplied flags over the resolved worker
// configuration, matching the server's flag-over-environment precedence.
func applyWorkerFlags(cfg *config.WorkerConfig, fs *flag.FlagSet, workerID *string, concurrency *int, maxRetries *int) {
	if flagWasSet(fs, "worker-id") && workerID != nil {
		cfg.ID = *workerID
	}
	if flagWasSet(fs, "concurrency") && concurrency != nil && *concurrency > 0 {
		cfg.Concurrency = *concurrency
	}
	if flagWasSet(fs, "max-retries") && maxRetries != nil && *maxRetries >= 0 {
		cfg.MaxRetries = *maxRetries
	}
}

// optionalString returns the flag value only when the caller supplied it, so an
// unset flag does not override the environment.
func optionalString(fs *flag.FlagSet, name string, value *string) *string {
	if !flagWasSet(fs, name) {
		return nil
	}
	return value
}

// flagWasSet reports whether a flag was supplied on the command line.
func flagWasSet(fs *flag.FlagSet, name string) bool {
	found := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}

// logLevelFor maps a configured level name to a slog level.
func logLevelFor(name string) slog.Level {
	switch name {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}
