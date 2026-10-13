package none

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/provider"
)

// test-none-provider-completion-fails: a completion under the no-model
// selection raises a typed not-available error naming the provider
// configuration key.
func TestCompletionFailsWithNotAvailable(t *testing.T) {
	p := New()
	_, err := p.Complete(context.Background(), provider.CompletionRequest{})
	if err == nil {
		t.Fatal("no-model completion must fail")
	}
	if !provider.IsNotAvailable(err) {
		t.Fatalf("error is %T, want the not-available class", err)
	}
	var typed *provider.NotAvailableError
	if !errors.As(err, &typed) {
		t.Fatal("error is not a typed not-available error")
	}
	if typed.ConfigKey != provider.ConfigKeyProvider {
		t.Fatalf("error names key %q, want %q", typed.ConfigKey, provider.ConfigKeyProvider)
	}
	if !strings.Contains(err.Error(), provider.ConfigKeyProvider) {
		t.Fatalf("message %q does not name the configuration key", err.Error())
	}
}

// The scope carried by the request is preserved so an operator can tell which
// scope is unconfigured.
func TestCompletionPreservesScope(t *testing.T) {
	p := New()
	_, err := p.Complete(context.Background(), provider.CompletionRequest{Scope: "reflect"})
	var typed *provider.NotAvailableError
	if !errors.As(err, &typed) {
		t.Fatal("error is not a typed not-available error")
	}
	if typed.Scope != "reflect" {
		t.Fatalf("scope = %q, want reflect", typed.Scope)
	}
}

func TestEmbedFailsWithNotAvailable(t *testing.T) {
	p := New()
	_, err := p.Embed(context.Background(), provider.EmbeddingRequest{Texts: []string{"a"}})
	if !provider.IsNotAvailable(err) {
		t.Fatalf("embed error is %T, want not-available", err)
	}
}

// test-none-provider-verify-ok: connection verification under the no-model
// selection succeeds as a no-op.
func TestVerifyConnectionIsSuccessfulNoOp(t *testing.T) {
	p := New()
	if err := p.VerifyConnection(context.Background()); err != nil {
		t.Fatalf("verify = %v, want a no-op success", err)
	}
}

func TestNameAndDimensions(t *testing.T) {
	p := New()
	if p.Name() != NameNone {
		t.Fatalf("name = %q, want %q", p.Name(), NameNone)
	}
	if got := p.Dimensions(); got != provider.EmbeddingDimensions {
		t.Fatalf("dimensions = %d, want %d", got, provider.EmbeddingDimensions)
	}
}
