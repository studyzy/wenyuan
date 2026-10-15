package httpx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeValidator is a scripted OperationValidator for the middleware tests.
type fakeValidator struct {
	decision    OperationDecision
	validate    func(OperationRequest) OperationDecision
	scope       []TagGroup
	scopeCalls  int
	lastRequest OperationRequest
}

func (v *fakeValidator) ValidateOperation(_ context.Context, op OperationRequest) OperationDecision {
	v.lastRequest = op
	if v.validate != nil {
		return v.validate(op)
	}
	return v.decision
}

func (v *fakeValidator) ResolveTagScope(_ context.Context, _ string) []TagGroup {
	v.scopeCalls++
	return v.scope
}

// operationServer mounts a bank-scoped POST handler that records invocation.
func operationServer() (*http.ServeMux, *bool) {
	called := false
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/default/banks/{bank_id}/memories", func(w http.ResponseWriter, r *http.Request) {
		called = true
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/default/banks/{bank_id}/memories/recall", func(w http.ResponseWriter, r *http.Request) {
		called = true
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	})
	mux.HandleFunc("POST /v1/default/banks/{bank_id}/files/retain", func(w http.ResponseWriter, r *http.Request) {
		called = true
		WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
	})
	return mux, &called
}

func operationHandler(host *ExtensionHost, mux *http.ServeMux) http.Handler {
	return Chain(mux, NullDropMiddleware(false), OperationValidationMiddleware(host))
}

// A denied retain is answered with the validator's status and reason, and the
// retain handler never runs (so nothing is stored).
func TestDeniedRetainAnsweredWithValidatorStatus(t *testing.T) {
	mux, called := operationServer()
	host := NewExtensionHost()
	v := &fakeValidator{decision: OperationDecision{Allowed: false, Reason: "writes are frozen", Status: KindConflict}}
	host.RegisterOperationValidator(v)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories", strings.NewReader(`{}`))
	operationHandler(host, mux).ServeHTTP(rec, req)

	if rec.Code != KindConflict {
		t.Fatalf("status = %d, want %d", rec.Code, KindConflict)
	}
	if !strings.Contains(rec.Body.String(), "writes are frozen") {
		t.Fatalf("denial reason missing: %s", rec.Body.String())
	}
	if *called {
		t.Fatal("a denied retain must not reach its handler")
	}
	if v.lastRequest.Operation != OperationRetain || v.lastRequest.BankID != "b1" {
		t.Fatalf("validator saw %+v", v.lastRequest)
	}
}

// The file-retain route is the same write operation and is denied the same way.
func TestDeniedFileRetainAnsweredWithValidatorStatus(t *testing.T) {
	mux, called := operationServer()
	host := NewExtensionHost()
	host.RegisterOperationValidator(&fakeValidator{decision: OperationDecision{Allowed: false, Reason: "nope", Status: KindForbidden}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/files/retain", strings.NewReader(`{}`))
	operationHandler(host, mux).ServeHTTP(rec, req)

	if rec.Code != KindForbidden {
		t.Fatalf("status = %d, want %d", rec.Code, KindForbidden)
	}
	if *called {
		t.Fatal("a denied file retain must not reach its handler")
	}
}

// A denied recall is answered with the validator's status and reason, and the
// recall handler never runs (so nothing is read).
func TestDeniedRecallAnsweredWithValidatorStatus(t *testing.T) {
	mux, called := operationServer()
	host := NewExtensionHost()
	v := &fakeValidator{decision: OperationDecision{Allowed: false, Reason: "recall off", Status: KindUnavailable}}
	host.RegisterOperationValidator(v)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", strings.NewReader(`{"query":"q"}`))
	operationHandler(host, mux).ServeHTTP(rec, req)

	if rec.Code != KindUnavailable {
		t.Fatalf("status = %d, want %d", rec.Code, KindUnavailable)
	}
	if *called {
		t.Fatal("a denied recall must not reach its handler")
	}
	if v.lastRequest.Operation != OperationRecall {
		t.Fatalf("validator saw %+v", v.lastRequest)
	}
}

// An allowed operation proceeds unchanged.
func TestAllowedOperationProceeds(t *testing.T) {
	mux, called := operationServer()
	host := NewExtensionHost()
	host.RegisterOperationValidator(&fakeValidator{decision: OperationDecision{Allowed: true}})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories", strings.NewReader(`{}`))
	operationHandler(host, mux).ServeHTTP(rec, req)

	if rec.Code != http.StatusOK || !*called {
		t.Fatalf("allowed operation did not run: status = %d called = %v", rec.Code, *called)
	}
}

// A resolved scope is carried on the request context for every bank-scoped
// request, so a scoped reader is confined on the by-id and listing reads as well
// as on recall and reflect; a handler that does not read it is unaffected.
func TestTagScopeCarriedForReads(t *testing.T) {
	scope := []TagGroup{{Tags: []string{"team-a"}}}
	host := NewExtensionHost()
	v := &fakeValidator{decision: OperationDecision{Allowed: true}, scope: scope}
	host.RegisterOperationValidator(v)

	seenScope := func(mux *http.ServeMux, method, path string) ([]TagGroup, int) {
		var seen []TagGroup
		mux.HandleFunc(method+" /v1/default/banks/{bank_id}/", func(w http.ResponseWriter, r *http.Request) {
			seen = TagScopeFrom(r)
			WriteJSONFor(w, r, http.StatusOK, map[string]any{"ok": true})
		})
		rec := httptest.NewRecorder()
		operationHandler(host, mux).ServeHTTP(rec, httptest.NewRequest(method, path, strings.NewReader(`{}`)))
		return seen, rec.Code
	}

	mux := http.NewServeMux()
	got, code := seenScope(mux, http.MethodPost, "/v1/default/banks/b1/memories/recall")
	if code != http.StatusOK || len(got) != 1 || len(got[0].Tags) != 1 || got[0].Tags[0] != "team-a" {
		t.Fatalf("recall scope = %+v (status %d)", got, code)
	}
	before := v.scopeCalls

	// A reflect and a by-id read each resolve the scope too, so the gate is not
	// silently dropped on the reads that now honour it.
	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/v1/default/banks/b1/reflect"},
		{http.MethodGet, "/v1/default/banks/b1/memories/m1"},
		{http.MethodGet, "/v1/default/banks/b1/documents"},
	} {
		muxN := http.NewServeMux()
		gotN, codeN := seenScope(muxN, tc.method, tc.path)
		if codeN != http.StatusOK || len(gotN) != 1 || gotN[0].Tags[0] != "team-a" {
			t.Fatalf("%s %s scope = %+v (status %d)", tc.method, tc.path, gotN, codeN)
		}
	}
	if v.scopeCalls <= before {
		t.Fatalf("the reads did not resolve a scope: calls = %d", v.scopeCalls)
	}
}

// With no validator registered the middleware is a pass-through: the response
// is byte-identical to the unwrapped handler.
func TestNoValidatorLeavesResponseUnchanged(t *testing.T) {
	mux, _ := operationServer()
	host := NewExtensionHost() // registers nothing

	plain := httptest.NewRecorder()
	Chain(mux, NullDropMiddleware(false)).ServeHTTP(plain,
		httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", strings.NewReader(`{}`)))

	wrapped := httptest.NewRecorder()
	operationHandler(host, mux).ServeHTTP(wrapped,
		httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories/recall", strings.NewReader(`{}`)))

	if plain.Code != wrapped.Code || plain.Body.String() != wrapped.Body.String() {
		t.Fatalf("unextended response changed: %d/%s vs %d/%s",
			plain.Code, plain.Body.String(), wrapped.Code, wrapped.Body.String())
	}
}

// A nil host is tolerated, so a deployment that wires no extension host does
// not panic or change the response.
func TestNilHostPassesThrough(t *testing.T) {
	mux, called := operationServer()
	rec := httptest.NewRecorder()
	Chain(mux, NullDropMiddleware(false), OperationValidationMiddleware(nil)).ServeHTTP(rec,
		httptest.NewRequest(http.MethodPost, "/v1/default/banks/b1/memories", strings.NewReader(`{}`)))
	if rec.Code != http.StatusOK || !*called {
		t.Fatalf("nil host changed behaviour: status = %d called = %v", rec.Code, *called)
	}
}

// Only the gated operations are classified; other bank-scoped writes pass
// through untouched.
func TestClassifyOperationScope(t *testing.T) {
	cases := []struct {
		method, path string
		operation    string
		bankID       string
		ok           bool
	}{
		{http.MethodPost, "/v1/default/banks/b1/memories", OperationRetain, "b1", true},
		{http.MethodPost, "/v1/default/banks/b1/files/retain", OperationRetain, "b1", true},
		{http.MethodPost, "/v1/default/banks/b1/memories/recall", OperationRecall, "b1", true},
		{http.MethodDelete, "/v1/default/banks/b1/memories", "", "", false},
		{http.MethodPost, "/v1/default/banks/b1/reflect", OperationReflect, "b1", true},
		{http.MethodPost, "/v1/default/banks/b1", "", "", false},
		{http.MethodPost, "/v1/default/banks", "", "", false},
	}
	for _, tc := range cases {
		req := httptest.NewRequest(tc.method, tc.path, nil)
		op, bankID, ok := classifyOperation(req)
		if op != tc.operation || bankID != tc.bankID || ok != tc.ok {
			t.Errorf("%s %s => %q %q %v, want %q %q %v", tc.method, tc.path, op, bankID, ok, tc.operation, tc.bankID, tc.ok)
		}
	}
}
