package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/storage"
)

// With no model configured the service composes and serves, and the active
// provider is the no-model selection.
func TestComposesWithNoModelConfigured(t *testing.T) {
	cfg := testConfig()
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if application.Provider == nil {
		t.Fatal("the composed application must expose a provider")
	}
	if application.Provider.Name() != none.NameNone {
		t.Fatalf("provider = %q, want the no-model selection", application.Provider.Name())
	}
	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("version status = %d", rec.Code)
	}
}

// The no-model provider fails loudly if a path unexpectedly needs a model.
func TestNoModelProviderFailsLoudly(t *testing.T) {
	cfg := testConfig()
	application, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.Provider.Complete(context.Background(), provider.CompletionRequest{}); err == nil {
		t.Fatal("a completion under the no-model selection must fail")
	}
	if err := application.Provider.VerifyConnection(context.Background()); err != nil {
		t.Fatalf("connection verification must be a no-op success, got %v", err)
	}
}

// An invalid provider selection surfaces an error from composition.
func TestInvalidProviderSelectionFailsComposition(t *testing.T) {
	cfg := testConfig()
	cfg.Provider.Provider = "not-a-provider"
	_, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err == nil {
		t.Fatal("an invalid provider selection must surface at composition time")
	}
	if !provider.IsInvalidProvider(err) {
		t.Fatalf("error is %T, want the invalid-provider class", err)
	}
}

// A credential-requiring provider with no credential surfaces a configuration
// error from composition.
func TestMissingCredentialFailsComposition(t *testing.T) {
	cfg := testConfig()
	cfg.Provider.Provider = "openai"
	cfg.Provider.APIKey = ""
	_, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err == nil {
		t.Fatal("a provider requiring a credential must fail without one")
	}
	if !provider.IsMissingCredential(err) {
		t.Fatalf("error is %T, want missing-credential", err)
	}
}

// An injected provider overrides selection so a test needs no live endpoint.
func TestInjectedProviderOverridesSelection(t *testing.T) {
	injected := &stubProvider{name: "injected"}
	cfg := testConfig()
	application, err := New(context.Background(), cfg, Dependencies{
		Store:    storage.NewMemoryStore("public"),
		Provider: injected,
	})
	if err != nil {
		t.Fatal(err)
	}
	if application.Provider.Name() != "injected" {
		t.Fatalf("provider = %q, want the injected one", application.Provider.Name())
	}
}

// The resolved provider settings carry the configuration through to the
// boundary without a parallel config path.
func TestProviderSettingsMapFromConfig(t *testing.T) {
	pc := config.FromEnv(func(key string) (string, bool) {
		values := map[string]string{
			config.EnvLLMProvider:      "openai",
			config.EnvLLMAPIKey:        "k",
			config.EnvLLMModel:         "m",
			config.EnvLLMMaxRetries:    "9",
			config.EnvLLMMaxConcurrent: "4",
		}
		v, ok := values[key]
		return v, ok
	})
	settings := ProviderSettings(pc.Provider)
	if settings.Provider != "openai" || settings.APIKey != "k" || settings.Model != "m" {
		t.Fatalf("settings = %+v", settings)
	}
	if settings.Retry.MaxRetries != 9 {
		t.Fatalf("retry = %+v", settings.Retry)
	}
	if settings.Concurrency.Global != 4 {
		t.Fatalf("concurrency = %+v", settings.Concurrency)
	}
}

// stubProvider is a minimal provider for composition tests.
type stubProvider struct{ name string }

func (s *stubProvider) Name() string                               { return s.name }
func (s *stubProvider) Dimensions() int                            { return provider.DeclaredDimensions() }
func (s *stubProvider) VerifyConnection(ctx context.Context) error { return nil }
func (s *stubProvider) Complete(ctx context.Context, req provider.CompletionRequest) (provider.CompletionResult, error) {
	return provider.CompletionResult{}, nil
}
func (s *stubProvider) Embed(ctx context.Context, req provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}
