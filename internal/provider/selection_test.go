package provider

import (
	"context"
	"errors"
	"testing"
)

func stubRegistration(name string, requiresCredential bool) Registration {
	return Registration{
		Name:               name,
		RequiresCredential: requiresCredential,
		Build: func(s Settings) (Provider, error) {
			return &stubProvider{name: s.Provider}, nil
		},
	}
}

type stubProvider struct{ name string }

func (s *stubProvider) Name() string                               { return s.name }
func (s *stubProvider) Dimensions() int                            { return DeclaredDimensions() }
func (s *stubProvider) VerifyConnection(ctx context.Context) error { return nil }
func (s *stubProvider) Complete(ctx context.Context, req CompletionRequest) (CompletionResult, error) {
	return CompletionResult{}, nil
}
func (s *stubProvider) Embed(ctx context.Context, req EmbeddingRequest) (EmbeddingResult, error) {
	return EmbeddingResult{}, nil
}

func testRegistry() *Registry {
	r := NewRegistry()
	r.Register(stubRegistration("none", false))
	r.Register(Registration{
		Name:               "openai",
		RequiresCredential: true,
		DefaultBaseURL:     "https://api.openai.com/v1",
		Build: func(s Settings) (Provider, error) {
			return &stubProvider{name: s.Provider}, nil
		},
	})
	return r
}

// test-unknown-provider-rejected: an unknown provider name is rejected with a
// typed invalid-provider error listing the accepted names.
func TestUnknownProviderRejected(t *testing.T) {
	registry := testRegistry()
	_, err := Select(registry, Settings{Provider: "bogus"})
	if !IsInvalidProvider(err) {
		t.Fatalf("error is %T, want the invalid-provider class", err)
	}
	var typed *InvalidProviderError
	if !errors.As(err, &typed) {
		t.Fatal("error is not a typed invalid-provider error")
	}
	if want := []string{"none", "openai"}; !sameStrings(typed.Accepted, want) {
		t.Fatalf("accepted = %v, want %v", typed.Accepted, want)
	}
}

// A blank name is rejected, never treated as a default selection.
func TestBlankProviderRejected(t *testing.T) {
	registry := testRegistry()
	_, err := Select(registry, Settings{Provider: "   "})
	if !IsInvalidProvider(err) {
		t.Fatalf("blank selection must be invalid, got %v", err)
	}
}

// test-missing-credential-rejected: a provider requiring a credential raises
// a configuration error at initialization when it is absent.
func TestMissingCredentialRejected(t *testing.T) {
	registry := testRegistry()
	_, err := Select(registry, Settings{Provider: "openai"})
	if !IsMissingCredential(err) {
		t.Fatalf("error is %T, want missing-credential", err)
	}
}

// A provider that needs no credential selects successfully.
func TestSelectionWithoutCredential(t *testing.T) {
	registry := testRegistry()
	p, err := Select(registry, Settings{Provider: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "none" {
		t.Fatalf("name = %q", p.Name())
	}
}

// A configured credential lets a credential-requiring provider select.
func TestSelectionWithCredential(t *testing.T) {
	registry := testRegistry()
	p, err := Select(registry, Settings{Provider: "openai", APIKey: "k"})
	if err != nil {
		t.Fatal(err)
	}
	if p.Name() != "openai" {
		t.Fatalf("name = %q", p.Name())
	}
}

// The default base URL is applied only when the operator configured none.
func TestDefaultBaseURLApplied(t *testing.T) {
	registry := testRegistry()
	var seen Settings
	registry.Register(Registration{
		Name:           "capture",
		DefaultBaseURL: "https://default.example/v1",
		Build: func(s Settings) (Provider, error) {
			seen = s
			return &stubProvider{name: s.Provider}, nil
		},
	})
	if _, err := Select(registry, Settings{Provider: "capture"}); err != nil {
		t.Fatal(err)
	}
	if seen.BaseURL != "https://default.example/v1" {
		t.Fatalf("base url = %q, want the default", seen.BaseURL)
	}
	if _, err := Select(registry, Settings{Provider: "capture", BaseURL: "https://explicit.example/v1"}); err != nil {
		t.Fatal(err)
	}
	if seen.BaseURL != "https://explicit.example/v1" {
		t.Fatalf("base url = %q, want the explicit value used verbatim", seen.BaseURL)
	}
}

func TestValidateSelection(t *testing.T) {
	registry := testRegistry()
	if err := ValidateSelection(registry, "openai"); err != nil {
		t.Fatalf("known name rejected: %v", err)
	}
	if err := ValidateSelection(registry, "nope"); !IsInvalidProvider(err) {
		t.Fatalf("unknown name = %v, want invalid-provider", err)
	}
}

func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
