package provider_test

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
	"github.com/studyzy/wenyuan/internal/provider/none"
	"github.com/studyzy/wenyuan/internal/provider/openai"
)

// localDouble is the injected local transport used by the boundary
// integration test, so the test needs no live endpoint or real credential.
type localDouble struct {
	status int
	body   string
}

func (d *localDouble) Do(req *http.Request) (*http.Response, error) {
	return &http.Response{
		StatusCode: d.status,
		Body:       io.NopCloser(strings.NewReader(d.body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}, nil
}

// The provider boundary is reachable through selection with no live endpoint
// and no real credential.
func TestBoundarySelectsNoModelOffline(t *testing.T) {
	reg := provider.NewRegistry()
	reg.Register(provider.Registration{
		Name: none.NameNone,
		Build: func(provider.Settings) (provider.Provider, error) {
			return none.New(), nil
		},
	})
	openai.Register(reg)

	selected, err := provider.Select(reg, provider.Settings{Provider: none.NameNone})
	if err != nil {
		t.Fatal(err)
	}
	if selected.Name() != none.NameNone {
		t.Fatalf("selected %q", selected.Name())
	}
	// The no-model selection reports the boundary's declared dimension.
	if selected.Dimensions() != provider.EmbeddingDimensions {
		t.Fatalf("dimensions = %d, want %d", selected.Dimensions(), provider.EmbeddingDimensions)
	}
}

// An unknown selection is rejected before any call.
func TestBoundaryRejectsUnknownSelectionOffline(t *testing.T) {
	reg := provider.NewRegistry()
	openai.Register(reg)
	_, err := provider.Select(reg, provider.Settings{Provider: "no-such-provider"})
	if !provider.IsInvalidProvider(err) {
		t.Fatalf("error is %T, want invalid-provider", err)
	}
}

// A completion call routes through the configured provider against a local
// transport double and returns usage metadata.
func TestCompletionRoutesThroughAdapterOffline(t *testing.T) {
	doer := &localDouble{
		status: 200,
		body:   `{"model":"m","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`,
	}
	client := openai.NewClient(provider.Settings{
		Provider: "openai",
		APIKey:   "test-key",
		BaseURL:  "https://example.test/v1",
		Model:    "m",
	}, doer)

	result, err := client.Complete(context.Background(), provider.CompletionRequest{
		Messages: []provider.Message{{Role: provider.RoleUser, Content: "hello"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Content != "hi" {
		t.Fatalf("content = %q", result.Content)
	}
	if result.Usage.TotalTokens != 5 {
		t.Fatalf("usage = %+v", result.Usage)
	}
}
