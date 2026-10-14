// Command wenyuan-worker is the process entrypoint for the background queue
// consumer. It resolves configuration the same way the server does, opens the
// same storage backend, and drains the asynchronous operation queue until it
// is asked to stop.
//
// It shares the server's configuration resolution and storage selection rather
// than duplicating them, so a deployment configures both entrypoints
// identically and only the process role differs.
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/config"
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

	cfg := config.FromEnvironment()
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

	consumer := app.NewQueueConsumer(store, model, app.NewReflectRunner(model), cfg.Worker)
	return app.RunQueueConsumer(ctx, consumer)
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
