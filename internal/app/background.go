package app

import (
	"context"
	"errors"
	"log/slog"

	"github.com/studyzy/wenyuan/internal/maintenance"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/worker"
)

// BackgroundRoles bundles the queue consumer and the maintenance loop that a
// process runs beside the HTTP server. The reference's standalone mode starts
// both inside the API process, so a deployment that runs only the server still
// drains its queue and runs its housekeeping.
type BackgroundRoles struct {
	Consumer *worker.Consumer
	Loop     *maintenance.Loop
}

// NewBackgroundRoles builds the in-process worker roles from a composed
// application: the same store, provider and reflection boundary the HTTP
// surface uses, so queued work and served requests see one another.
func NewBackgroundRoles(application *App) *BackgroundRoles {
	model := application.Provider
	reflection := NewReflectRunner(model)
	tenants := application.TenantExtension
	if tenants == nil {
		tenants = NewTenantExtension(application.Config)
	}
	return &BackgroundRoles{
		Consumer: NewQueueConsumer(application.Store, model, reflection, embeddingGeneratorFrom(application.Embedder), application.Config.Worker, application.Config.WorkerTuning, application.webhookGuard(), ExtractionSettingsFrom(application.Config), !application.Config.StoreDocumentText, application.Config.DatabaseSchema, tenants).WithAuditor(application.Audit),
		Loop: NewMaintenanceLoop(
			application.Store,
			model,
			reflection,
			tenants,
			application.Config.Maintenance,
			application.Config.VectorIndex,
		),
	}
}

// Run drives both roles until the context ends. A cancelled context is a clean
// stop rather than a failure. The consumer's error is the one returned; a
// maintenance failure is logged per job and retried on the next tick, so it is
// not fatal to the process.
func (b *BackgroundRoles) Run(ctx context.Context) error {
	if b == nil {
		return nil
	}
	loopDone := make(chan error, 1)
	go func() { loopDone <- RunMaintenanceLoop(ctx, b.Loop) }()

	consumerErr := RunQueueConsumer(ctx, b.Consumer)
	if err := <-loopDone; err != nil && consumerErr == nil {
		return err
	}
	return consumerErr
}

// startBackgroundRoles launches the in-process worker roles when the worker is
// enabled and returns a stop function that waits for them to drain. When the
// worker is disabled it returns a no-op stop function.
func startBackgroundRoles(ctx context.Context, application *App) func() {
	if application == nil {
		return func() {}
	}
	stopProfile := startProfileEmitter(ctx, application)
	if !application.Config.WorkerEnabled {
		return stopProfile
	}
	roles := NewBackgroundRoles(application)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := roles.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("background worker stopped", "error", err)
		}
	}()
	return func() {
		<-done
		stopProfile()
	}
}

// startProfileEmitter runs the periodic profile report when the profiling
// request is set. It is independent of the worker: the reference arms it in the
// API process too, because the process that dies is the one under investigation.
func startProfileEmitter(ctx context.Context, application *App) func() {
	spec, err := application.Config.ResolveProfile()
	if err != nil {
		slog.Error("profiling is not armed", "error", err)
		return func() {}
	}
	if spec == nil {
		return func() {}
	}
	emitter := &monitoring.ProfileEmitter{Every: spec.Every, Top: spec.Top}
	done := make(chan struct{})
	go func() {
		defer close(done)
		emitter.Run(ctx)
	}()
	return func() { <-done }
}
