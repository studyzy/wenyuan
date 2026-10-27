package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/mock"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/storage"
)

// probeStub is a provider double whose startup probe behaviour is scripted, so
// the composition-level tests need no endpoint.
type probeStub struct {
	name   string
	err    error
	block  bool
	called *bool
}

func (s probeStub) Name() string    { return s.name }
func (s probeStub) Dimensions() int { return provider.DeclaredDimensions() }
func (s probeStub) VerifyConnection(ctx context.Context) error {
	if s.called != nil {
		*s.called = true
	}
	if s.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return s.err
}
func (s probeStub) Complete(context.Context, provider.CompletionRequest) (provider.CompletionResult, error) {
	return provider.CompletionResult{}, nil
}
func (s probeStub) Embed(context.Context, provider.EmbeddingRequest) (provider.EmbeddingResult, error) {
	return provider.EmbeddingResult{}, nil
}

func TestStartupProbeAcceptsReachableProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`)
	}))
	defer server.Close()

	cfg := testConfig()
	// The shipped default bounds the probe at the reference's 300s; a healthy
	// probe must compose within it.
	if cfg.ModelInitTimeout != 300*time.Second {
		t.Fatalf("default model-init timeout = %s, want 300s", cfg.ModelInitTimeout)
	}
	cfg.Provider.Provider = "openai"
	cfg.Provider.APIKey = "k"
	cfg.Provider.Model = "m"
	cfg.Provider.BaseURL = server.URL

	if _, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")}); err != nil {
		t.Fatalf("a provider that answers the probe must compose, got %v", err)
	}
}

// An unreachable provider fails startup, and the error names the provider so
// the operator knows which endpoint was checked.
func TestStartupProbeFailsOnUnreachableProvider(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(w, `{"error":{"message":"no such endpoint"}}`)
	}))
	defer server.Close()

	cfg := testConfig()
	cfg.Provider.Provider = "openai"
	cfg.Provider.APIKey = "k"
	cfg.Provider.Model = "m"
	cfg.Provider.BaseURL = server.URL

	_, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err == nil {
		t.Fatal("an unreachable provider must fail startup")
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Fatalf("startup error must name the provider, got %v", err)
	}
}

// The probe is bounded: a provider that never answers fails startup within the
// configured model-init timeout rather than hanging it.
func TestStartupProbeBoundedByTimeout(t *testing.T) {
	cfg := testConfig()
	cfg.ModelInitTimeout = 50 * time.Millisecond

	start := time.Now()
	_, err := New(context.Background(), cfg, Dependencies{
		Store:    storage.NewMemoryStore("public"),
		Provider: probeStub{name: "slow", block: true},
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("a provider that never answers must fail startup")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("probe waited %s, want the %s timeout to bound it", elapsed, cfg.ModelInitTimeout)
	}
	if !strings.Contains(err.Error(), "slow") {
		t.Fatalf("timeout error must name the provider, got %v", err)
	}
}

// An explicitly skipped probe is not run at all, so a provider that would fail
// the probe composes and no failure is produced.
func TestSkipLLMVerificationSkipsProbe(t *testing.T) {
	called := false
	cfg := testConfig()
	cfg.SkipLLMVerification = true

	if _, err := New(context.Background(), cfg, Dependencies{
		Store:    storage.NewMemoryStore("public"),
		Provider: probeStub{name: "down", err: errors.New("boom"), called: &called},
	}); err != nil {
		t.Fatalf("a skipped probe must not fail startup, got %v", err)
	}
	if called {
		t.Fatal("a skipped probe must not call VerifyConnection")
	}
}

// The no-model and mock selections are valid composition states with nothing to
// reach, so a probe that would fail never runs for them.
func TestStartupProbeSkipsNoModelAndMock(t *testing.T) {
	for _, name := range []string{none.NameNone, mock.NameMock} {
		cfg := testConfig()
		called := false
		if _, err := New(context.Background(), cfg, Dependencies{
			Store:    storage.NewMemoryStore("public"),
			Provider: probeStub{name: name, err: errors.New("boom"), called: &called},
		}); err != nil {
			t.Fatalf("%s must compose, got %v", name, err)
		}
		if called {
			t.Fatalf("%s must not be probed", name)
		}
	}
}

// A reference .env that turns the startup probe off is accepted rather than
// refused: WENYUAN_API_SKIP_LLM_VERIFICATION is a setting this build honours.
func TestSkipLLMVerificationIsAccepted(t *testing.T) {
	cfg := config.FromEnv(func(key string) (string, bool) {
		switch key {
		case "WENYUAN_API_DATABASE_URL":
			return "memory", true
		}
		if key == config.EnvSkipLLMVerification {
			return "true", true
		}
		return "", false
	})
	if err := cfg.ValidateSupported(); err != nil {
		t.Fatalf("the reference's skip-verification switch was refused: %v", err)
	}
	if !cfg.SkipLLMVerification {
		t.Fatal("the switch did not resolve")
	}
}
