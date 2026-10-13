package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/studyzy/wenyuan/internal/bankapi"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/storage"
)

// App is the composed, served application. It is the object the process
// entrypoint and the application import target both resolve to.
type App struct {
	Config   config.Config
	Store    storage.Store
	Router   *httpx.Router
	Handler  http.Handler
	Metrics  *httpx.MetricsRecorder
	MetricsH *monitoring.MetricsHandler
	// Provider is the active model-provider boundary, built once at
	// composition so a selection error surfaces at startup. When no model is
	// configured it is the no-model selection, which preserves the existing
	// deterministic memory-write and reflection behavior.
	Provider provider.Provider
}

// Dependencies is the injectable surface used to build an App. Tests supply
// in-process doubles; production supplies the PostgreSQL backend.
type Dependencies struct {
	// Store overrides backend selection when non-nil.
	Store storage.Store
	// Backend overrides backend selection when non-nil.
	Backend storage.Backend
	// Provider overrides provider selection when non-nil, so tests can inject
	// a provider without a live endpoint or credential.
	Provider provider.Provider
	// Listen probes the resolved listen address before initialization.
	Listen func(network, address string) (net.Listener, error)
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

// New composes the application: it selects storage, applies migrations, mounts
// every route namespace, and wires the middleware stack.
func New(ctx context.Context, cfg config.Config, deps Dependencies) (*App, error) {
	listen := deps.Listen
	if listen == nil {
		listen = net.Listen
	}

	// Probe the listen address before expensive initialization.
	probe, err := probeAddress(cfg, listen)
	if err != nil {
		return nil, err
	}
	if probe != nil {
		_ = probe.Close()
	}

	store, err := resolveStore(ctx, cfg, deps)
	if err != nil {
		return nil, err
	}
	if cfg.RunMigrationsOnStartup {
		if err := store.Migrate(ctx); err != nil {
			return nil, fmt.Errorf("migrations failed: %w", err)
		}
	}

	activeProvider := deps.Provider
	if activeProvider == nil {
		selected, err := ResolveProvider(cfg)
		if err != nil {
			return nil, err
		}
		activeProvider = selected
	}

	metrics := &httpx.MetricsRecorder{}
	router := httpx.NewRouter(httpx.RouterOptions{
		NullDropFields:       cfg.NullDropFields,
		GzipMinSize:          cfg.GzipMinSize,
		Metrics:              metrics,
		EnabledBankConfigAPI: cfg.EnableBankConfigAPI,
		EnabledMCP:           cfg.MCPEnabled,
		Extensions:           httpx.NewExtensionHost(),
		Authenticator:        authenticatorFrom(cfg),
	})

	app := &App{Config: cfg, Store: store, Router: router, Metrics: metrics, Provider: activeProvider}
	app.mount(router)
	app.Handler = router.Service()
	return app, nil
}

func resolveStore(ctx context.Context, cfg config.Config, deps Dependencies) (storage.Store, error) {
	if deps.Store != nil {
		return deps.Store, nil
	}
	backend := deps.Backend
	if backend == nil {
		selected, err := storage.SelectBackend(backendNameFor(cfg))
		if err != nil {
			return nil, err
		}
		backend = selected
	}
	return backend.Open(ctx, storage.BackendConfig{
		DatabaseURL:         cfg.DatabaseURL,
		Schema:              cfg.DatabaseSchema,
		VectorExtension:     cfg.VectorExtension,
		TextSearchExtension: cfg.TextSearchExtension,
		RunMigrations:       cfg.RunMigrationsOnStartup,
	})
}

// backendNameFor picks the in-process backend when no database URL is
// configured, so the service can serve without a live database.
func backendNameFor(cfg config.Config) string {
	if cfg.DatabaseURL == "" {
		return storage.BackendMemory
	}
	return storage.BackendPostgreSQL
}

// probeAddress binds the resolved listen address once to fail fast on a bind
// error, retrying a transient address-in-use condition for a short grace
// window. A nil listener means the address was not probed.
func probeAddress(cfg config.Config, listen func(string, string) (net.Listener, error)) (net.Listener, error) {
	deadline := time.Now().Add(cfg.BindGrace)
	for {
		listener, err := listen("tcp", cfg.Address())
		if err == nil {
			return listener, nil
		}
		if !isAddressInUse(err) || time.Now().After(deadline) {
			return nil, fmt.Errorf("cannot bind %s: %w", cfg.Address(), err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func isAddressInUse(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return errors.Is(opErr.Err, syscall.EADDRINUSE)
	}
	return false
}

func (a *App) mount(router *httpx.Router) {
	handle := router.Handle

	health := &monitoring.HealthHandler{DB: storePinger{a.Store}}
	monitoring.BindHealthRoutes(health, handle)

	version := &monitoring.VersionHandler{
		APIVersion: httpx.APIVersion,
		Features:   a.Config.FeatureMap(),
	}
	monitoring.BindVersionRoutes(version, handle)

	a.MetricsH = &monitoring.MetricsHandler{Requests: &a.Metrics.Requests, InFlight: &a.Metrics.InFlight}
	monitoring.BindMetricsRoutes(a.MetricsH, handle)

	banks := bankapi.NewHandlers(a.Store)
	banks.WithConfigAPI(a.Config.EnableBankConfigAPI)
	banks.BindRoutes(handle)

	memops := memoryops.NewHandlers(a.Store, int(a.Config.RetryAfter.Seconds()), int(a.Config.ReadTimeout.Milliseconds()))
	memops.RetainBatchEnabled = a.Config.RetainBatchEnabled
	memops.RecallStrategyBoosts = a.Config.RecallStrategyBoosts
	memops.BankReflectDefaults = &memoryops.BankReflectDefaults{
		SearchObservationsMaxTokens:       a.Config.ReflectSearchObservationsMaxTokens,
		SearchObservationsIncludeEntities: a.Config.ReflectSearchObservationsIncludeEntities,
	}
	memops.ReflectTimeout = a.Config.ReadTimeout
	memops.BindRoutes(handle)

	retired := &bankapi.RetiredHandler{Auth: authenticatorFrom(a.Config)}
	retired.BindRetiredRoutes(handle)

	a.mountTransfer(handle)
	a.mountMentalModels(handle)
	a.mountDirectives(handle)
}

// storePinger adapts the store readiness probe to the monitoring dependency.
type storePinger struct{ store storage.Store }

func (p storePinger) Ping(r *http.Request) error {
	if p.store == nil {
		return errors.New("no store")
	}
	return p.store.Ready(r.Context())
}
