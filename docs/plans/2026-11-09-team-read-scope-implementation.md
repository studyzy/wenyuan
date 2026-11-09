# Team Read Scope Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Let a caller of `recall` and `memories/list` choose whether a team
member's read is served from the project's shared bank (`team`, the default and
current behaviour) or from their own bank (`personal`), through a query
parameter that never enters the OpenAPI contract; `both` is refused with 422
until cross-bank fusion has a design of its own.

**Architecture:** The parameter is parsed in the HTTP layer into a
`storage.FallbackPreference` and carried on `FallbackQuery`. `withFallback`
gains one short-circuit: `PreferPersonal` reads the caller's own bank without
probing the team bank. Nothing else in the redirect changes. The parameter is
declared on both routes through `httpx.QueryParams`, which is what keeps it out
of the `X-Ignored-Params` report and out of the declaration-drift test.

**Tech Stack:** Go 1.25, `net/http`, the in-memory store for unit tests, the
existing `withFallback` helper in `internal/storage`.

**Design:** `docs/plans/2026-11-09-team-read-scope-design.md`

---

## Context a fresh engineer needs

- `withFallback` (`internal/storage/team_fallback.go:45`) already implements
  **team first, personal as the floor**: it probes `BankHasUnits` on the *team*
  bank, reads the team bank when it holds anything, and reads the caller's own
  bank otherwise. `scope=team` names that behaviour; it does not change it.
- Two routes call it: `internal/memoryops/recall.go:165` and
  `internal/reporting/memory_routes.go:118`.
- Query parameters are **declared per route** via `httpx.QueryParams(handler,
  names...)` at registration. A handler that reads an undeclared name fails
  `internal/app/query_declaration_test.go`, and an undeclared name that arrives
  on a request is echoed in the `X-Ignored-Params` header. Declaring `scope` is
  therefore not optional.
- `?scope=` stays out of the OpenAPI document: `internal/httpx/openapi.json` is
  captured byte for byte from the reference and must not be edited.
  `scripts/reference_openapi.py --check` and `scripts/derive_client_spec.py
  --check` must both keep passing, and nothing under `clients/` is regenerated.
- Verification gate: `make check` (build + test + vet + pgx build + lint).
- Commit style: imperative subject, body says why.

---

### Task 1: `PreferPersonal` in the storage fallback

**Files:**
- Modify: `internal/storage/team_fallback.go`
- Test: `internal/storage/team_fallback_test.go`

**Step 1: Write the failing test**

Append to `internal/storage/team_fallback_test.go`. `presenceStore` already
exists at line 85 of that file and ignores the schema; this task needs a variant
that counts probes so it can assert the *absence* of one.

```go
// countingPresence answers presence from a script and records every probe it
// was asked, which is what pins that PreferPersonal never asks.
type countingPresence struct {
	Store
	has    bool
	probes int
}

func (p *countingPresence) BankHasUnits(_ context.Context, bankID string) (bool, error) {
	p.probes++
	return p.has, nil
}

// TestPreferPersonalNeverProbesTheTeamBank pins the redirect being skipped
// before the capability check: a caller that stated "personal" gets its own
// bank, the team bank is not consulted, and one query is saved.
func TestPreferPersonalNeverProbesTheTeamBank(t *testing.T) {
	presence := &countingPresence{has: true}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	var tried []string
	value, servedBy, err := withFallback[string](context.Background(), presence,
		FallbackQuery{BankID: "alice::Web", Preference: PreferPersonal}, scope,
		func(_ context.Context, bankID string) (string, error) {
			tried = append(tried, bankID)
			return bankID, nil
		})
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if value != "alice::Web" || servedBy != "alice::Web" {
		t.Fatalf("value = %q servedBy = %q, want the personal bank", value, servedBy)
	}
	if presence.probes != 0 {
		t.Fatalf("the team bank was probed %d times, want 0", presence.probes)
	}
	if len(tried) != 1 || tried[0] != "alice::Web" {
		t.Fatalf("read %v, want exactly the personal bank once", tried)
	}
}

// TestPreferPersonalIgnoresAMissingScope pins that the preference is honoured
// even with no scope resolved: a non-member asking for "personal" is not an
// error, it is the read they already had.
func TestPreferPersonalIgnoresAMissingScope(t *testing.T) {
	value, servedBy, err := withFallback[string](context.Background(), NewMemoryStore("public"),
		FallbackQuery{BankID: "alice::Web", Preference: PreferPersonal}, nil,
		func(_ context.Context, bankID string) (string, error) { return bankID, nil })
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if value != "alice::Web" || servedBy != "alice::Web" {
		t.Fatalf("value = %q servedBy = %q, want the personal bank", value, servedBy)
	}
}

// TestPreferPersonalStillHonoursFiltersThatCannotBeServed pins that the
// preference does not widen the redirect: state and document filters keep their
// own rule, which for these queries is the same bank either way.
func TestPreferPersonalStillHonoursFiltersThatCannotBeServed(t *testing.T) {
	presence := &countingPresence{has: true}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	for _, query := range []FallbackQuery{
		{BankID: "alice::Web", State: "invalidated", Preference: PreferPersonal},
		{BankID: "alice::Web", DocumentID: "doc-1", Preference: PreferPersonal},
	} {
		_, servedBy, err := withFallback[string](context.Background(), presence, query, scope,
			func(_ context.Context, bankID string) (string, error) { return bankID, nil })
		if err != nil {
			t.Fatalf("withFallback(%+v): %v", query, err)
		}
		if servedBy != "alice::Web" {
			t.Fatalf("withFallback(%+v) served by %q, want the personal bank", query, servedBy)
		}
	}
	if presence.probes != 0 {
		t.Fatalf("the team bank was probed %d times, want 0", presence.probes)
	}
}

// TestPreferTeamIsTheZeroValue pins that leaving the preference unset keeps the
// behaviour every existing caller has: the team bank answers once it holds
// content.
func TestPreferTeamIsTheZeroValue(t *testing.T) {
	if PreferTeam != FallbackPreference(0) {
		t.Fatalf("PreferTeam = %v, want the zero value", PreferTeam)
	}
	presence := &countingPresence{has: true}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	_, servedBy, err := withFallback[string](context.Background(), presence,
		FallbackQuery{BankID: "alice::Web"}, scope,
		func(_ context.Context, bankID string) (string, error) { return bankID, nil })
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if servedBy != "acme::Web" {
		t.Fatalf("servedBy = %q, want the team bank", servedBy)
	}
}
```

**Step 2: Run the tests to verify they fail**

Run: `go test ./internal/storage/ -run 'TestPreferPersonal|TestPreferTeamIsTheZeroValue' -v`

Expected: compile failure — `undefined: PreferPersonal`, `undefined:
PreferTeam`, `unknown field Preference in struct literal of type FallbackQuery`.

**Step 3: Write the minimal implementation**

In `internal/storage/team_fallback.go`, after the `FallbackPicker` type
declaration and before `FallbackQuery`, add:

```go
// FallbackPreference says which bank a redirectable read wants.
type FallbackPreference int

const (
	// PreferTeam reads the team bank when it holds anything, and the caller's
	// own bank otherwise. It is the zero value, so a caller that states no
	// preference gets the behaviour this helper has always had.
	PreferTeam FallbackPreference = iota
	// PreferPersonal never redirects and never probes the team bank: the caller
	// has said the answer it wants is its own, so asking the presence question
	// would only cost a query.
	PreferPersonal
)
```

Add the field to `FallbackQuery`, keeping its existing comments:

```go
	// Preference says whether a redirect is wanted at all. The zero value keeps
	// the team-first behaviour.
	Preference FallbackPreference
```

In `withFallback`, after the first `if fallback == nil ... { ... }` block and
before the `presence, ok := store.(BankPresence)` block, insert:

```go
	// A caller that asked for its own bank is answered from it, and the team
	// bank is not probed at all.
	if query.Preference == PreferPersonal {
		value, err := read(ctx, query.BankID)
		return value, query.BankID, err
	}
```

**Step 4: Run the tests to verify they pass**

Run: `go test ./internal/storage/ -run 'TestPreferPersonal|TestPreferTeamIsTheZeroValue|TestWithFallback' -v`

Expected: PASS. `TestWithFallbackChoosesTheTeamBankWhenItHoldsContent` and
`TestWithFallbackStaysPersonalWhenTheTeamIsEmpty` still pass untouched — they
pin `PreferTeam`, which is unchanged.

Then the whole package: `go test ./internal/storage/ -count=1`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/storage/team_fallback.go internal/storage/team_fallback_test.go
git commit -m "let a fallback read ask for the personal bank" \
  -m "withFallback gains a preference: PreferPersonal reads the caller's own
bank and skips the team-bank probe entirely, which saves a query and makes
'the caller already decided' explicit. The zero value keeps team-first, so
every existing caller is unaffected."
```

---

### Task 2: Parse `?scope=`

**Files:**
- Create: `internal/memoryops/read_scope.go`
- Test: `internal/memoryops/read_scope_test.go`

**Step 1: Write the failing test**

```go
package memoryops

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TestMemoryReadScope pins the parameter's answers: absent means team, the two
// supported values map to their preference, and both a refused-but-known value
// and an unknown one are field-level items that say which they are.
func TestMemoryReadScope(t *testing.T) {
	cases := []struct {
		name     string
		query    string
		want     storage.FallbackPreference
		wantKind string // "" means accepted
	}{
		{"absent", "", storage.PreferTeam, ""},
		{"empty", "?scope=", storage.PreferTeam, ""},
		{"team", "?scope=team", storage.PreferTeam, ""},
		{"personal", "?scope=personal", storage.PreferPersonal, ""},
		{"both is known but unsupported", "?scope=both", storage.PreferTeam, "value_error"},
		{"unknown", "?scope=bogus", storage.PreferTeam, "literal_error"},
		{"case matters", "?scope=PERSONAL", storage.PreferTeam, "literal_error"},
		{"last occurrence wins", "?scope=personal&scope=team", storage.PreferTeam, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/v1/default/banks/b/memories/recall"+tc.query, nil)
			got, failure := memoryReadScope(req)
			if tc.wantKind == "" {
				if failure != nil {
					t.Fatalf("scope %q was refused: %v", tc.query, failure)
				}
				if got != tc.want {
					t.Fatalf("scope %q = %v, want %v", tc.query, got, tc.want)
				}
				return
			}
			if failure == nil || len(failure.Errors) != 1 {
				t.Fatalf("scope %q was accepted, want one failure item", tc.query)
			}
			if kind := failure.Errors[0].Type; kind != tc.wantKind {
				t.Fatalf("scope %q error type = %q, want %q", tc.query, kind, tc.wantKind)
			}
			loc := strings.Join(failure.Errors[0].Loc, ".")
			if loc != "query.scope" {
				t.Fatalf("scope %q error location = %q, want query.scope", tc.query, loc)
			}
		})
	}
}

// TestMemoryReadScopeBothNamesItself pins that "both" reads as a known value
// that is not available yet, while an unknown value is told what exists.
func TestMemoryReadScopeBothNamesItself(t *testing.T) {
	_, failure := memoryReadScope(httptest.NewRequest(http.MethodPost, "/x?scope=both", nil))
	if failure == nil {
		t.Fatal("both must be refused while fusion is unimplemented")
	}
	if msg := failure.Errors[0].Msg; !strings.Contains(msg, "not supported yet") {
		t.Fatalf("both message = %q, want it to say the value is not supported yet", msg)
	}
	_, failure = memoryReadScope(httptest.NewRequest(http.MethodPost, "/x?scope=bogus", nil))
	if failure == nil {
		t.Fatal("an unknown value must be refused")
	}
	if msg := failure.Errors[0].Msg; !strings.Contains(msg, "team") || !strings.Contains(msg, "personal") {
		t.Fatalf("unknown-value message = %q, want the accepted values named", msg)
	}
}
```

Open `internal/httpx/validation.go` first and use the real field names of
`httpx.ValidationError` (the expected ones are `Type`, `Loc`, `Msg`); adjust the
test to the struct, never the struct to the test.

**Step 2: Run the test to verify it fails**

Run: `go test ./internal/memoryops/ -run TestMemoryReadScope -v`

Expected: compile failure — `undefined: memoryReadScope`.

**Step 3: Write the minimal implementation**

Create `internal/memoryops/read_scope.go`:

```go
package memoryops

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

const (
	scopeTeam     = "team"
	scopePersonal = "personal"
	scopeBoth     = "both"
)

// memoryReadScopes is the accepted set, as an unknown value is told it. "both"
// is listed because this service recognises it; the item for it says it is not
// available yet.
var memoryReadScopes = []string{scopeTeam, scopePersonal, scopeBoth}

// memoryReadScope reads the `scope` query parameter that chooses which bank a
// bank-scoped read is served from.
//
// The parameter is not part of the reference's contract, so it stays out of the
// request model and out of the OpenAPI document: a route declares it through
// httpx.QueryParams and nothing else knows about it. Absent means "team", which
// is the behaviour the team fallback has always had.
//
// The two refusals are deliberately different items: "both" is a value this
// service intends to support, so saying it is not supported yet is actionable,
// while an unknown spelling is told the accepted set. Folding the first into
// the second would send a caller looking for a typo that is not there.
func memoryReadScope(r *http.Request) (storage.FallbackPreference, *httpx.ValidationFailure) {
	value := httpx.Query(r, "scope")
	switch value {
	case "", scopeTeam:
		return storage.PreferTeam, nil
	case scopePersonal:
		return storage.PreferPersonal, nil
	case scopeBoth:
		return storage.PreferTeam, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.ValueError([]string{"query", "scope"}, value,
				"both is not supported yet; use team or personal"),
		}}
	default:
		return storage.PreferTeam, &httpx.ValidationFailure{Errors: []httpx.ValidationError{
			httpx.Literal([]string{"query", "scope"}, value, memoryReadScopes),
		}}
	}
}

// MemoryReadScope is memoryReadScope for the other route package that falls
// back: the two reads share one parse so their refusals cannot drift apart.
func MemoryReadScope(r *http.Request) (storage.FallbackPreference, *httpx.ValidationFailure) {
	return memoryReadScope(r)
}
```

`httpx.Query` records the read for the declaration audit, which is what lets
`internal/app/query_declaration_test.go` catch a route that reads `scope`
without declaring it.

**Step 4: Run the test to verify it passes**

Run: `go test ./internal/memoryops/ -run TestMemoryReadScope -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/memoryops/read_scope.go internal/memoryops/read_scope_test.go
git commit -m "parse the read-scope query parameter" \
  -m "Team is the default, so a client that says nothing keeps today's
behaviour. 'both' is refused with a message that says it is not available yet
rather than reported as a typo."
```

---

### Task 3: Apply the scope to recall

**Files:**
- Modify: `internal/memoryops/routes.go:14`
- Modify: `internal/memoryops/recall.go` (around lines 160-165)
- Modify: `internal/memoryops/team_scope.go`
- Test: `internal/memoryops/read_scope_recall_test.go`

**Step 1: Write the failing test**

First read how `internal/memoryops` builds a handler over a store double
(`embeddings_test.go`, `disconnect_test.go`) and reuse that pattern; the double
must satisfy `storage.Store` and expose whether `Recall` was called.

```go
// TestRecallRefusesBoth pins that an unsupported scope answers 422 before the
// store is asked, so a refused scope never costs a recall.
func TestRecallRefusesBoth(t *testing.T) {
	// Build the handler and the recording store with this package's own harness.
	req := httptest.NewRequest(http.MethodPost,
		"/v1/default/banks/a::web/memories/recall?scope=both", strings.NewReader(`{"query":"x"}`))
	rec := httptest.NewRecorder()
	handlers.Recall(rec, req)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("recall?scope=both = %d, want 422: %s", rec.Code, rec.Body.String())
	}
	if recorder.called() {
		t.Fatal("a refused scope still reached the store")
	}
}

// TestRecallScopePersonalSkipsTheTeamScope pins that a personal read never
// asks the resolver: a resolver that fails the test when called is wired as
// handlers.TeamScope, and a ?scope=personal recall answers 200 with zero calls.
```

Fill these against the package's harness instead of inventing new plumbing.

**Step 2: Run the tests to verify they fail**

Run: `go test ./internal/memoryops/ -run 'TestRecallRefusesBoth|TestRecallScopePersonal' -v`

Expected: FAIL — `?scope=both` answers 200 and the resolver is still called for
a personal read.

**Step 3: Write the minimal implementation**

In `internal/memoryops/routes.go`:

```go
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixRecall), httpx.QueryParams(h.Recall, "scope"))
```

In `internal/memoryops/recall.go`, immediately before the block that starts with
`query = applyRecallTagScope(r, query)`, insert the parse so a refusal answers
before any store call:

```go
	// Which bank answers is the caller's choice, made before anything is
	// retrieved: a refused scope must not cost a recall.
	preference, scopeFailure := memoryReadScope(r)
	if scopeFailure != nil {
		httpx.WriteValidationFailure(w, scopeFailure)
		return
	}
```

and change the fallback line to carry it:

```go
	query.Fallback = h.teamFallback(r.Context(), bankID, preference)
```

In `internal/memoryops/team_scope.go`, give `teamFallback` the parameter. The
resolver interface stays as it is — the preference is not its business:

```go
func (h *Handlers) teamFallback(ctx context.Context, bankID string, preference storage.FallbackPreference) *storage.FallbackScope {
	if preference == storage.PreferPersonal {
		return nil
	}
	if h.TeamScope == nil {
		return nil
	}
	caller, ok := storage.SchemaFrom(ctx)
	if !ok {
		return nil
	}
	scope, err := h.TeamScope.Resolve(ctx, caller, bankID)
	if err != nil {
		return nil
	}
	return scope
}
```

Returning nil is correct, not merely cheap: with no scope the storage layer
reads the caller's own bank, which is exactly `PreferPersonal`. The
`FallbackQuery.Preference` short-circuit from Task 1 remains the defence for a
caller that hands the storage layer a preference directly, so no
`FallbackPreference` field is added to `RecallQuery` or `MemoryQuery`.

**Step 4: Run the tests to verify they pass**

Run: `go test ./internal/memoryops/ -count=1`

Expected: PASS, including the existing recall tests.

Run: `go test ./internal/app/ -run TestRouteHandlersReadOnlyDeclaredQueryParams -v`

Expected: PASS — recall declares `scope`, so the read is not drift.

**Step 5: Commit**

```bash
git add internal/memoryops/routes.go internal/memoryops/recall.go internal/memoryops/team_scope.go
git add internal/memoryops/read_scope_recall_test.go
git commit -m "apply the read scope to recall" \
  -m "The scope is parsed before anything is retrieved, so a refused value
costs no recall, and a personal read never resolves a team scope."
```

---

### Task 4: Apply the scope to the memory listing

**Files:**
- Modify: `internal/reporting/handlers.go:97-99`
- Modify: `internal/reporting/memory_routes.go` (around line 100)
- Modify: `internal/reporting/team_scope.go`
- Test: `internal/reporting/read_scope_list_test.go`

**Step 1: Write the failing test**

Follow the package's existing served-handler fixture for `ListMemories`.

```go
// TestListMemoriesScopeRequests pins the listing's three accepted states and
// its two refusals.
func TestListMemoriesScopeRequests(t *testing.T) {
	for _, query := range []string{"", "?scope=team", "?scope=personal"} {
		if rec := serveListMemories(t, query); rec.Code != http.StatusOK {
			t.Fatalf("list%s = %d, want 200: %s", query, rec.Code, rec.Body.String())
		}
	}
	for _, query := range []string{"?scope=both", "?scope=bogus", "?scope=PERSONAL"} {
		rec := serveListMemories(t, query)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Fatalf("list%s = %d, want 422: %s", query, rec.Code, rec.Body.String())
		}
		body := rec.Body.String()
		if !strings.Contains(body, `"query"`) || !strings.Contains(body, `"scope"`) {
			t.Fatalf("list%s error body does not locate query.scope: %s", query, body)
		}
	}
	if rec := serveListMemories(t, "?scope=both"); !strings.Contains(rec.Body.String(), "not supported yet") {
		t.Fatalf("list?scope=both body = %s, want the not-supported message", rec.Body.String())
	}
}
```

**Step 2: Run the test to verify it fails**

Run: `go test ./internal/reporting/ -run TestListMemoriesScopeRequests -v`

Expected: FAIL — every scope answers 200.

**Step 3: Write the minimal implementation**

In `internal/reporting/handlers.go`, add `"scope"` to the list route's declared
set:

```go
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixMemoriesList), httpx.QueryParams(h.ListMemories,
		"type", "q", "consolidation_state", "state", "document_id", "entity_id", "tags", "tags_match",
		"time_field", "start_date", "end_date", "limit", "offset", "scope"))
```

In `internal/reporting/memory_routes.go`, before the `MemoryQuery` literal is
built, add:

```go
	// The scope chooses which bank answers. It is validated with the other query
	// members, so a refused value answers before the store is asked.
	preference, scopeFailure := memoryops.MemoryReadScope(r)
	if scopeFailure != nil {
		httpx.WriteValidationFailure(w, scopeFailure)
		return
	}
```

and change the fallback line to:

```go
		Fallback:           h.teamFallback(r.Context(), bankID, preference),
```

In `internal/reporting/team_scope.go`, mirror Task 3's signature change so a
personal listing never resolves a team scope.

**Step 4: Run the tests to verify they pass**

Run: `go test ./internal/reporting/ -count=1`

Expected: PASS.

Run: `go test ./internal/app/ -run TestRouteHandlersReadOnlyDeclaredQueryParams -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/reporting/handlers.go internal/reporting/memory_routes.go internal/reporting/team_scope.go
git add internal/reporting/read_scope_list_test.go
git commit -m "apply the read scope to the memory listing" \
  -m "The listing and recall share one parse, so the two reads that fall back
cannot disagree about which scopes they accept."
```

---

### Task 5: End-to-end proof through the served handlers

**Files:**
- Modify: `internal/app/team_read_test.go`

**Step 1: Write the failing test**

The fixture already exists (`newTeamReadFixture`, `writeTeamFact`,
`writePersonalFact`). Add helpers next to `listingTotal`: `recallTexts`,
`listingTexts`, `containsText`; give `recall` an extra query-string parameter
instead of duplicating its request plumbing.

```go
// TestRecallScopePersonalReadsOnlyTheirOwnBank pins the whole path in one
// request: the same member and bank id, answered by the personal bank when the
// scope says so, while the default still answers from the team.
func TestRecallScopePersonalReadsOnlyTheirOwnBank(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.writePersonalFact(t, "alice", "Alice's own note about the deploy.")

	got := fixture.recallTexts(t, "alice", "alice::web", "deploy", "")
	if !containsText(got, "The deploy uses port 15432.") {
		t.Fatalf("default recall = %v, want the team's fact", got)
	}
	got = fixture.recallTexts(t, "alice", "alice::web", "deploy", "?scope=personal")
	if containsText(got, "The deploy uses port 15432.") {
		t.Fatalf("scope=personal recall = %v, want none of the team's facts", got)
	}
	if !containsText(got, "Alice's own note about the deploy.") {
		t.Fatalf("scope=personal recall = %v, want Alice's own fact", got)
	}
}

// TestRecallScopeBothIsRefused pins the served refusal, status and message.
func TestRecallScopeBothIsRefused(t *testing.T) {
	fixture := newTeamReadFixture(t)
	request := httptest.NewRequest(http.MethodPost,
		"/v1/default/banks/alice::web/memories/recall?scope=both", strings.NewReader(`{"query":"deploy"}`))
	request.Header.Set("Authorization", "Bearer "+fixture.keys["alice"])
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("recall?scope=both = %d, want 422: %s", recorder.Code, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), "not supported yet") {
		t.Fatalf("body = %s, want the not-supported message", recorder.Body.String())
	}
}

// TestListScopePersonalIsHonoured pins the same choice on the listing.
func TestListScopePersonalIsHonoured(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.writePersonalFact(t, "alice", "Alice's own note about the deploy.")

	if got := listingTotal(t, fixture, "alice", "alice::web"); got != 1 {
		t.Fatalf("default listing total = %d, want the team's one fact", got)
	}
	got := listingTexts(t, fixture, "alice", "alice::web", "?scope=personal")
	if len(got) != 1 || got[0] != "Alice's own note about the deploy." {
		t.Fatalf("personal listing = %v, want only Alice's fact", got)
	}
}
```

**Step 2: Run the tests to verify they fail**

Run: `go test ./internal/app/ -run 'TestRecallScope|TestListScope' -v`

Expected: FAIL on the missing helpers first; with those written, `scope=personal`
still returns team content until Tasks 3 and 4 are applied.

**Step 3: Implement what is missing**

Reproduce the failure before assuming a cause. If `personal` still returns team
content, check the order in `internal/memoryops/recall.go`: the scope is parsed
before `teamFallback`, and `teamFallback` returns nil for `PreferPersonal`.

**Step 4: Run the tests to verify they pass**

Run: `go test ./internal/app/ -run 'TestRecallScope|TestListScope|TestMember|TestNonMember|TestTeamKey|TestInvalidated|TestRemoving' -count=1 -v`

Expected: PASS. The existing member tests are the regression proof that the
default path is unchanged.

**Step 5: Commit**

```bash
git add internal/app/team_read_test.go
git commit -m "prove the read scope end to end" \
  -m "The same member and bank id answer from the team by default and from
their own bank when the scope says personal, and both is refused at the edge."
```

---

### Task 6: Record the parameter as a deliberate addition

**Files:**
- Modify: `docs/PARITY-OPEN-ITEMS.md`
- Modify: `AGENTS.md` (the team plane section)

**Step 1: Add the parity note**

Append to the residual list in `docs/PARITY-OPEN-ITEMS.md`:

```markdown
- **`?scope=` on `memories/recall` and `memories/list` is this service's own.**
  It chooses which bank a team member's read is served from: `team` (default,
  the existing behaviour), `personal`, and `both` (refused 422 until cross-bank
  fusion is designed). It is deliberately absent from
  `internal/httpx/openapi.json` — that document is captured from the reference —
  and is declared per route in the router, so it is neither reported in
  `X-Ignored-Params` nor fails the declaration audit. A parity audit must treat
  its presence on those two routes as expected and its absence from the OpenAPI
  document as correct. Design:
  `docs/plans/2026-11-09-team-read-scope-design.md`.
```

**Step 2: Update the AGENTS.md sentence**

In the team plane section, replace the read-fallback bullet's opening with:

```markdown
- **Reads are team-first, and the caller can say otherwise.** A member's
  `POST /banks/{id}/memories/recall` and `GET /banks/{id}/memories/list` read the
  project's team bank once that bank holds any units; when it holds none, the
  member's own bank answers. `?scope=team` (the default) names that behaviour,
  `?scope=personal` reads the member's own bank and never resolves the team
  scope, and `?scope=both` is refused with 422 until cross-bank fusion is
  designed. The parameter is this service's own: it is not in the captured
  OpenAPI document and is declared per route, so it does not appear in
  `X-Ignored-Params`.
```

**Step 3: Run the OpenAPI and SDK checks**

Run:

```bash
python3 scripts/derive_client_spec.py --check
git diff --stat internal/httpx/openapi.json clients/openapi.json
```

Expected: the first prints success or no diff; the second prints nothing —
neither document changed. If `internal/httpx/openapi.json` changed, something
edited it; revert that file, the parameter must not be there.

`scripts/reference_openapi.py --check` needs a reachable reference service; run
it when one is available and confirm no diff.

**Step 4: Run the full gate**

Run: `make check`

Expected: PASS (build, test, vet, pgx build, lint).

**Step 5: Commit**

```bash
git add AGENTS.md docs/PARITY-OPEN-ITEMS.md
git commit -m "record the read-scope parameter as our own" \
  -m "A parity audit has to know the parameter is deliberate and that its
absence from the captured OpenAPI document is correct."