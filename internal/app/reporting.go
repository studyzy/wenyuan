package app

import (
	"context"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/reporting"
)

// llmProber adapts the provider boundary onto the reporting area's probe.
type llmProber struct {
	provider provider.Provider
}

// Probe verifies the provider is reachable.
func (p llmProber) Probe(ctx context.Context) error {
	return p.provider.VerifyConnection(ctx)
}

// mountReporting wires the observability, memory sub-route, knowledge-base,
// observation and consolidation families into the existing composition root.
func (a *App) mountReporting(handle func(method, pattern string, handler http.HandlerFunc)) {
	handlers := reporting.NewHandlers(a.Store)
	handlers.APIVersion = httpx.APIVersion
	handlers.Features = a.Config.FeatureMap()
	handlers.BankLLMHealthEnabled = a.Config.EnableBankLLMHealth
	handlers.Extraction = newFactExtractor(a.Provider, a.Config.Provider.RetainMaxRetries)
	handlers.Embeddings = embeddingGeneratorFrom(a.Embedder)
	if a.Provider != nil && a.Provider.Name() != none.NameNone {
		handlers.Prober = llmProber{provider: a.Provider}
	}
	handlers.BindRoutes(handle)
}
