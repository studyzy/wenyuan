package app

import (
	"context"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/monitoring"
)

// startWorkerMetrics wires the worker-labelled /metrics when the deployment
// turns the switch on. With --workers N every worker is its own process with
// its own registry, so a scrape reaches one random worker; this publishes each
// worker's series with an api_worker label so one scrape covers them all. It is
// a no-op when the switch is off or no slot is free, and it stops publishing
// when the context is cancelled.
func startWorkerMetrics(ctx context.Context, cfg config.Config, app *App) {
	if !cfg.MetricsTuning.WorkerLabel || app == nil || app.MetricsH == nil {
		return
	}
	worker := monitoring.StartWorkerMetrics(cfg.Workers, app.MetricsH.Exposition, "")
	if worker == nil {
		return
	}
	app.MetricsH.Workers = worker
	go func() {
		<-ctx.Done()
		worker.Stop()
	}()
}
