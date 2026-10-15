package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// appValidator is a permit-all OperationValidator with an optional deny for one
// operation.
type appValidator struct{ deny string }

func (v appValidator) ValidateOperation(_ context.Context, op httpx.OperationRequest) httpx.OperationDecision {
	if op.Operation == v.deny {
		return httpx.OperationDecision{Allowed: false, Reason: "denied by deployment", Status: http.StatusForbidden}
	}
	return httpx.OperationDecision{Allowed: true}
}

func (v appValidator) ResolveTagScope(_ context.Context, _ string) []httpx.TagGroup { return nil }

// An operation validator registered on the composed extension host gates the
// served retain route: the denial is answered with its status and reason.
func TestAppExtensionHostRegistersValidator(t *testing.T) {
	application, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	application.Extensions.RegisterOperationValidator(appValidator{deny: httpx.OperationRetain})

	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories",
		strings.NewReader(`{"items":[{"content":"hello"}]}`)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "denied by deployment") {
		t.Fatalf("denial reason missing: %s", rec.Body.String())
	}
}

// With no validator registered the served responses are byte-identical to the
// unextended composition.
func TestAppNoValidatorResponseUnchanged(t *testing.T) {
	plain, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	// Registering nil leaves the host unextended, so this app behaves as if no
	// registration call was made.
	wired, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	wired.Extensions.RegisterOperationValidator(nil)

	first := httptest.NewRecorder()
	plain.Handler.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/version", nil))
	second := httptest.NewRecorder()
	wired.Handler.ServeHTTP(second, httptest.NewRequest(http.MethodGet, "/version", nil))

	if first.Code != second.Code || first.Body.String() != second.Body.String() {
		t.Fatalf("response changed with no validator: %d/%s vs %d/%s",
			first.Code, first.Body.String(), second.Code, second.Body.String())
	}
}

// A permit-all validator leaves an allowed operation unchanged.
func TestAppAllowedOperationUnchanged(t *testing.T) {
	application, err := New(context.Background(), testConfig(), Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	application.Extensions.RegisterOperationValidator(appValidator{})

	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/version", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// scopeValidator permits everything and confines the caller to fixed tags.
type scopeValidator struct{ scope []httpx.TagGroup }

func (v scopeValidator) ValidateOperation(_ context.Context, _ httpx.OperationRequest) httpx.OperationDecision {
	return httpx.OperationDecision{Allowed: true}
}

func (v scopeValidator) ResolveTagScope(_ context.Context, _ string) []httpx.TagGroup { return v.scope }

// A scope resolved by the registered validator reaches the recall handler and
// narrows an unscoped request: the end-to-end wiring works.
func TestAppTagScopeNarrowsRecall(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "alpha", Type: "world", Tags: []string{"a"}})
	store.SeedMemory("b1", storage.RecallMatch{ID: "m2", Text: "alpha", Type: "world", Tags: []string{"b"}})

	application, err := New(context.Background(), testConfig(), Dependencies{Store: store})
	if err != nil {
		t.Fatal(err)
	}
	application.Extensions.RegisterOperationValidator(scopeValidator{scope: []httpx.TagGroup{{Tags: []string{"a"}}}})

	rec := httptest.NewRecorder()
	application.Handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall",
		strings.NewReader(`{"query":"alpha"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"m1"`) || strings.Contains(rec.Body.String(), `"m2"`) {
		t.Fatalf("scope did not narrow the recall: %s", rec.Body.String())
	}
}
