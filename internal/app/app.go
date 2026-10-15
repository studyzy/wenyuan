package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"syscall"
	"time"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/bankapi"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/fileparse"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/mcp"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/monitoring"
	"github.com/studyzy/wenyuan/internal/oteltrace"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/embed"
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
	// Embedder is the configured embedding backend, independent of the chat
	// provider; nil when no embedding provider is configured.
	Embedder embed.Embedder
	// Reranker is the configured cross-encoder chain, independent of the chat
	// provider; nil when no reranker is configured.
	Reranker provider.Reranker
	// Tracer exports spans when tracing is enabled; nil when it is off.
	Tracer *oteltrace.Tracer
	// Audit decides and records the audit trail.
	Audit *audit.Logger
	// Extensions is the deployment extension host. It is created during
	// composition so a caller can register route extensions and an operation
	// validator before serving.
	Extensions *httpx.ExtensionHost
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
	// AddressAlreadyBound tells composition that the caller holds the listen
	// address, so the fail-fast probe must be skipped. Serving binds the
	// address before composing, and a probe of an address the same process
	// already holds fails with address-in-use — which is not a startup error,
	// it is the probe colliding with its own listener.
	AddressAlreadyBound bool
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

	// Probe the listen address before expensive initialization. A caller that
	// already holds the address skips this: the probe would collide with that
	// listener and report a false failure.
	if !deps.AddressAlreadyBound {
		probe, err := probeAddress(cfg, listen)
		if err != nil {
			return nil, err
		}
		if probe != nil {
			_ = probe.Close()
		}
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
	// The embedding backend and the reranker are configured independently of
	// the chat model, so they are built here, once, and a configuration this
	// build cannot honour surfaces at startup rather than at first use.
	embedder, err := resolveEmbedder(cfg)
	if err != nil {
		return nil, err
	}
	reranker, err := resolveReranker(cfg)
	if err != nil {
		return nil, err
	}
	// With tracing enabled, wrap the provider so every completion lands in the
	// model-request trace the observability endpoints serve.
	activeProvider = TraceProvider(activeProvider, store, cfg.Provider)
	// The span tracer is best-effort and off the latency path; a nil tracer
	// (tracing disabled) leaves the provider unwrapped.
	tracer := NewTracer(cfg.Tracing)
	activeProvider = TraceSpans(activeProvider, tracer)
	// The configured output language applies to every generated artifact, so it
	// is applied at the provider boundary rather than in each pipeline.
	activeProvider = WithOutputLanguage(activeProvider, cfg.Provider.OutputLanguage)

	tenantAuth, err := cfg.ResolveTenantAuth()
	if err != nil {
		return nil, err
	}
	// An extraction mode the extractor cannot implement refuses startup, the way
	// the reference's validator does: whoever set it asked for that shape.
	if !config.ValidRetainExtractionMode(cfg.RetainExtractionMode) {
		return nil, fmt.Errorf("%s must be one of (%s), got %q",
			config.EnvRetainExtractionMode, config.RetainExtractionModes, cfg.RetainExtractionMode)
	}

	// Every reported token count is measured with the configured vocabulary, so
	// the encoding is installed once, before any work is measured.
	storage.SetTokenEncoding(cfg.TokenizerEncoding)

	// A malformed profiling request refuses startup rather than silently
	// profiling nothing, which is what the reference does.
	if _, err := cfg.ResolveProfile(); err != nil {
		return nil, err
	}

	metrics := &httpx.MetricsRecorder{}
	extensions := httpx.NewExtensionHost()
	auditLogger := NewAuditLogger(cfg, store)
	router := httpx.NewRouter(httpx.RouterOptions{
		NullDropFields:        cfg.NullDropFields,
		BasePath:              cfg.BasePath,
		TenantAPIKey:          tenantAuth.APIKey,
		TenantMCPAuthDisabled: tenantAuth.MCPAuthDisabled,
		AccessLog:             accessLogWriter(cfg),
		GzipMinSize:           cfg.GzipMinSize,
		Metrics:               metrics,
		EnabledBankConfigAPI:  cfg.EnableBankConfigAPI,
		EnabledMCP:            cfg.MCPEnabled,
		MCPHandler:            mcp.New(store, auditLogger),
		Extensions:            extensions,
		Authenticator:         authenticatorFrom(cfg),
		ResolveBankAlias:      store.ResolveBankID,
		Audit: &httpx.AuditOptions{
			Action: httpAuditAction,
			Hook:   auditLogger,
		},
		Admission: httpx.AdmissionConfig{
			MaxWait:           cfg.AdmissionMaxWait,
			RetryAfterSeconds: int(cfg.RetryAfter.Seconds()),
			// The lanes name the writes the reference bounds. Each matches one
			// method: DELETE .../memories clears a bank and must not consume the
			// retain lane, and the reference admits only the POST routes.
			Rules: []httpx.AdmissionRule{
				{Method: http.MethodPost, Suffix: httpx.SuffixRecall, Lane: "recall", Limit: cfg.AdmissionRecallLimit},
				{Method: http.MethodPost, Suffix: httpx.SuffixMemories, Lane: "retain", Limit: cfg.AdmissionRetainLimit},
				{Method: http.MethodPost, Suffix: httpx.SuffixReflect, Lane: "reflect", Limit: cfg.AdmissionReflectLimit},
			},
		},
	})

	app := &App{Config: cfg, Store: store, Router: router, Metrics: metrics, Provider: activeProvider, Tracer: tracer, Extensions: extensions, Audit: auditLogger, Embedder: embedder, Reranker: reranker}
	// A deployment that names an operation validator gets the one this build
	// implements natively. The name is the reference's class path, so a
	// deployment configured for the reference behaves the same way here.
	if err := registerConfiguredValidator(extensions, cfg); err != nil {
		return nil, err
	}
	app.mount(router)
	app.Handler = router.Service()
	return app, nil
}

// Close flushes and stops the span tracer, so spans queued by the last calls
// reach the collector instead of dying with the process.
func (a *App) Close() {
	if a == nil {
		return
	}
	a.Tracer.Close()
}

func resolveStore(ctx context.Context, cfg config.Config, deps Dependencies) (storage.Store, error) {
	if deps.Store != nil {
		return deps.Store, nil
	}
	// An unset database URL refuses rather than silently serving from memory:
	// the reference provisions a persistent embedded database there, so a
	// deployment that configured nothing would lose its data on restart.
	url, inMemory, err := cfg.ResolveDatabaseTarget()
	if err != nil {
		return nil, err
	}
	backend := deps.Backend
	if backend == nil {
		selected, err := storage.SelectBackend(backendNameFor(inMemory))
		if err != nil {
			return nil, err
		}
		backend = selected
	}
	store, err := backend.Open(ctx, storage.BackendConfig{
		DatabaseURL:         url,
		Schema:              cfg.DatabaseSchema,
		VectorExtension:     cfg.VectorExtension,
		MaxOpenConns:        cfg.DBPoolMaxSize,
		MaxIdleConns:        cfg.DBPoolMinSize,
		TextSearchExtension: cfg.TextSearchExtension,
		RunMigrations:       cfg.RunMigrationsOnStartup,
		BankInfoCacheTTL:    cfg.BankInfoCacheTTL,
		BankStatsCacheTTL:   cfg.BankStatsCacheTTL,
	})
	if err != nil {
		return nil, err
	}
	return attachObjectStore(store, cfg)
}

// attachObjectStore points the store's payload persistence at an object store
// when one is configured. The in-process backend keeps its payloads in memory,
// so only a database-backed store takes the attachment.
func attachObjectStore(store storage.Store, cfg config.Config) (storage.Store, error) {
	pg, ok := store.(*storage.PostgresStore)
	if !ok || !cfg.FileStorage.Enabled() {
		return store, nil
	}
	payloads, err := newObjectStore(cfg.FileStorage)
	if err != nil {
		return nil, err
	}
	return pg.WithObjectStore(payloads), nil
}

// backendNameFor picks the in-process backend when the configuration asked for
// it explicitly, so an operator can run without a live database on purpose.
func backendNameFor(inMemory bool) string {
	if inMemory {
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

	health := &monitoring.HealthHandler{DB: storePinger{a.Store}, Version: httpx.APIVersion, Started: time.Now()}
	monitoring.BindHealthRoutes(health, handle)

	version := &monitoring.VersionHandler{
		APIVersion: httpx.APIVersion,
		Features:   a.Config.FeatureMap(),
	}
	monitoring.BindVersionRoutes(version, handle)

	a.MetricsH = &monitoring.MetricsHandler{Recorder: a.Metrics, Extra: operationMetrics(a.Store)}
	monitoring.BindMetricsRoutes(a.MetricsH, handle)
	if a.Config.PprofEnabled {
		monitoring.BindProfileRoutes(handle)
	}

	banks := bankapi.NewHandlers(a.Store)
	banks.WithConfigAPI(a.Config.EnableBankConfigAPI)
	banks.WithConfigDefaults(bankapi.PublicConfigDefaults(a.Config))
	banks.BindRoutes(handle)

	memops := memoryops.NewHandlers(a.Store, int(a.Config.RetryAfter.Seconds()), int(a.Config.ReadTimeout.Milliseconds()))
	memops.RetainBatchEnabled = a.Config.RetainBatchEnabled
	memops.RecallStrategyBoosts = a.Config.RecallStrategyBoosts
	memops.MaxAttachmentBytes = int64(a.Config.RetainAttachmentMaxSizeMB) << 20
	memops.MaxAttachments = a.Config.RetainAttachmentMaxCount
	memops.VisionEnabled = SupportsVision(a.Config)
	// The file-parsing subsystem takes the parser fallback chain, the
	// requestable-parser allowlist, the batch limits and the keep-bytes switch
	// from configuration, which is where the reference keys are recorded.
	fileParsers := fileparse.Settings{
		Chain:             a.Config.FileParser,
		Allowlist:         a.Config.FileParserAllowlist,
		IrisToken:         a.Config.FileParserIrisToken,
		IrisOrgID:         a.Config.FileParserIrisOrgID,
		LlamaParseAPIKey:  a.Config.FileParserLlamaParseAPIKey,
		MaxBatchCount:     a.Config.FileConversionMaxBatchSize,
		MaxBatchBytes:     int64(a.Config.FileConversionMaxBatchSizeMB) << 20,
		DeleteAfterRetain: a.Config.FileDeleteAfterRetain,
	}
	memops.Parsers = fileParsers.Registry()
	memops.ParserChain = fileParsers.Chain
	memops.ParserAllowlist = fileParsers.Allowlist
	memops.MaxFileBatchCount = fileParsers.MaxBatchCount
	memops.MaxFileBatchBytes = fileParsers.MaxBatchBytes
	memops.DeleteAfterRetain = fileParsers.DeleteAfterRetain
	memops.RecallRecency = storage.RecencyDecayConfig{
		Function:         a.Config.Recency.Function,
		LinearWindowDays: a.Config.Recency.LinearWindowDays,
		HalflifeDays:     a.Config.Recency.HalflifeDays,
	}
	// The extractor is wired from the provider boundary: with a model
	// configured a write stores extracted facts, without one it stores the
	// text verbatim.
	memops.Extraction = newFactExtractor(a.Provider, a.Config.Provider.RetainMaxRetries)
	// With an embedding model configured the write path embeds each unit and
	// the read path embeds the query, so the vector recall arm has vectors to
	// rank; without one both stay on their non-vector paths.
	memops.Embeddings = embeddingGeneratorFrom(a.Embedder)
	setObservationEmbedder(a.Store, embeddingGeneratorFrom(a.Embedder))
	memops.FileUploadDisabled = !a.Config.EnableFileUploadAPI
	memops.ObservationsEnabled = a.Config.EnableObservations
	memops.AutoConsolidationEnabled = a.Config.EnableAutoConsolidation
	memops.DiscardDocumentText = !a.Config.StoreDocumentText
	memops.DefaultExtractionMode = a.Config.RetainExtractionMode
	memops.DefaultOptionalFactDimensions = a.Config.RetainOptionalFactDimensions
	// The model-driven reflection loop replaces the deterministic one when a
	// model is configured.
	memops.Reflection = newReflectRunner(a.Provider)
	// The cross-encoder replaces the best-per-arm relevance fallback when a
	// reranker is configured. A dedicated RERANKER-family backend wins over the
	// chat provider's own capability, and the flags derived from the selected
	// reranker tell the pipeline how to read its scores.
	memops.Reranker, memops.RerankDepth, memops.RerankerScoresByRankPosition,
		memops.RerankerPrunesCandidates, memops.RerankerSeedFromFused =
		recallRerankerWiring(a.Config, a.Provider, a.Reranker)
	memops.BankReflectDefaults = &memoryops.BankReflectDefaults{
		SearchObservationsMaxTokens:       a.Config.ReflectSearchObservationsMaxTokens,
		SearchObservationsIncludeEntities: a.Config.ReflectSearchObservationsIncludeEntities,
	}
	memops.ReflectTimeout = a.Config.ReadTimeout
	// Memory Defense decisions enqueue their webhook through the worker's one
	// enqueue path; a deployment with no webhooks still gets the notifier, but
	// it finds no subscriber and queues nothing.
	memops.Webhooks = webhookNotifier{store: a.Store}
	memops.BindRoutes(handle)

	retired := &bankapi.RetiredHandler{}
	retired.BindRetiredRoutes(handle)

	a.mountTransfer(handle)
	a.mountMentalModels(handle)
	a.mountDirectives(handle)
	a.mountReadSurface(handle, memops)
	a.mountReporting(handle)
	a.mountWebhooks(handle)
}

// accessLogWriter is where the router writes its access lines: the process log
// sink when the deployment asked for them, and nil when it did not.
func accessLogWriter(cfg config.Config) io.Writer {
	if !cfg.AccessLog {
		return nil
	}
	return monitoring.LogSink
}

// storePinger adapts the store readiness probe to the monitoring dependency.
type storePinger struct{ store storage.Store }

func (p storePinger) Ping(r *http.Request) error {
	if p.store == nil {
		return errors.New("no store")
	}
	return p.store.Ready(r.Context())
}

// PoolStats reports the store's connection-pool utilization for the readiness
// payload, when the store can. A store without a pool (the in-memory backend)
// reports no stats, exactly as the reference omits them for a backend that has
// none.
func (p storePinger) PoolStats() (monitoring.PoolStats, bool) {
	reporter, ok := p.store.(interface {
		PoolStats() (storage.PoolStats, bool)
	})
	if !ok {
		return monitoring.PoolStats{}, false
	}
	stats, ok := reporter.PoolStats()
	if !ok {
		return monitoring.PoolStats{}, false
	}
	return monitoring.PoolStats{InUse: stats.InUse, Max: stats.Max, Idle: stats.Idle}, true
}
