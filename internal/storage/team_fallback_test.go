package storage

import (
	"context"
	"testing"
)

// The design's §13.4 clause 3: a store that does not implement BankPresence never
// falls back.
//
// This is the property that keeps every test double and every backend without a
// team concept compiling and behaving exactly as before. It is asserted on the
// helper rather than through a whole store so it holds for any caller that routes
// reads through it.

// TestWithFallbackIgnoresANilScope pins the cheapest and most common case: a read
// with no fallback is the read it was, byte for byte.
func TestWithFallbackIgnoresANilScope(t *testing.T) {
	ctx := context.Background()
	var called []string
	value, servedBy, err := withFallback[string](ctx, NewMemoryStore("public"), FallbackQuery{BankID: "alice::Web"}, nil,
		func(_ context.Context, bankID string) (string, error) {
			called = append(called, bankID)
			return "personal", nil
		})
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if value != "personal" || servedBy != "alice::Web" {
		t.Fatalf("value = %q servedBy = %q, want the personal bank", value, servedBy)
	}
	if len(called) != 1 || called[0] != "alice::Web" {
		t.Fatalf("read %v, want exactly the personal bank once", called)
	}
}

// storeWithoutPresence satisfies Store but carries no BankPresence, which is what
// a backend with no team concept — and a test double — looks like.
type storeWithoutPresence struct{ Store }

// TestStoreWithoutBankPresenceNeverFallsBack is clause 3: a store that does not
// implement the capability is passed no presence, so its reads stay put even when
// a scope was resolved.
func TestStoreWithoutBankPresenceNeverFallsBack(t *testing.T) {
	var store Store = storeWithoutPresence{Store: NewMemoryStore("public")}
	if _, ok := store.(BankPresence); ok {
		t.Fatal("this clause is about a store that does not implement BankPresence")
	}
	ctx := context.Background()
	value, servedBy, err := withFallback[string](ctx, store,
		FallbackQuery{BankID: "alice::Web"},
		&FallbackScope{Schema: "acme", BankID: "acme::Web"},
		func(_ context.Context, bankID string) (string, error) { return bankID, nil })
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if value != "alice::Web" || servedBy != "alice::Web" {
		t.Fatalf("value = %q servedBy = %q, want the personal bank", value, servedBy)
	}
}

// TestFallbackFiltersThatCannotBeServed pins the two conditions a fallback must
// not be used for: the team's bank holds no documents and no invalidated rows, so
// redirecting would turn "this bank has archived facts" into "there is nothing
// here".
func TestFallbackFiltersThatCannotBeServed(t *testing.T) {
	cases := []struct {
		name  string
		query FallbackQuery
		want  bool
	}{
		{"plain read", FallbackQuery{BankID: "alice::Web"}, true},
		{"valid state", FallbackQuery{BankID: "alice::Web", State: "valid"}, true},
		{"invalidated state", FallbackQuery{BankID: "alice::Web", State: "invalidated"}, false},
		{"document filter", FallbackQuery{BankID: "alice::Web", DocumentID: "doc-1"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.query.CanFallBack(); got != tc.want {
				t.Fatalf("CanFallBack() = %v, want %v", got, tc.want)
			}
		})
	}
}

// presenceStore is a store whose bank presence is scripted.
type presenceStore struct {
	Store
	has map[string]bool
	err error
}

func (p presenceStore) BankHasUnits(_ context.Context, bankID string) (bool, error) {
	if p.err != nil {
		return false, p.err
	}
	return p.has[bankID], nil
}

// TestWithFallbackChoosesTheTeamBankWhenItHoldsContent pins the redirect itself.
func TestWithFallbackChoosesTheTeamBankWhenItHoldsContent(t *testing.T) {
	presence := presenceStore{has: map[string]bool{"acme::Web": true}}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	value, servedBy, err := withFallback[string](context.Background(), presence,
		FallbackQuery{BankID: "alice::Web"}, scope,
		func(_ context.Context, bankID string) (string, error) { return bankID, nil })
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if value != "acme::Web" || servedBy != "acme::Web" {
		t.Fatalf("value = %q servedBy = %q, want the team bank", value, servedBy)
	}
}

// TestWithFallbackStaysPersonalWhenTheTeamIsEmpty pins the fallback decision being
// per-bank: an empty team bank means the member's own bank answers.
func TestWithFallbackStaysPersonalWhenTheTeamIsEmpty(t *testing.T) {
	presence := presenceStore{has: map[string]bool{"acme::Web": false}}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	value, servedBy, err := withFallback[string](context.Background(), presence,
		FallbackQuery{BankID: "alice::Web"}, scope,
		func(_ context.Context, bankID string) (string, error) { return bankID, nil })
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if value != "alice::Web" || servedBy != "alice::Web" {
		t.Fatalf("value = %q servedBy = %q, want the personal bank", value, servedBy)
	}
}

// TestWithFallbackReportsAPresenceFailure pins that a storage failure while
// deciding is reported rather than read as "the team is empty", which would
// silently serve the personal bank forever.
func TestWithFallbackReportsAPresenceFailure(t *testing.T) {
	presence := presenceStore{err: context.DeadlineExceeded}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	if _, _, err := withFallback[string](context.Background(), presence,
		FallbackQuery{BankID: "alice::Web"}, scope,
		func(_ context.Context, bankID string) (string, error) { return bankID, nil }); err == nil {
		t.Fatal("a presence failure must be reported")
	}
}

// TestRestoreBankIDRewritesTheListing pins that a client of a redirected listing
// still sees the bank it asked for, while unit ids stay the team bank's own.
func TestRestoreBankIDRewritesTheListing(t *testing.T) {
	units := []MemoryUnit{{ID: "u1", BankID: "acme::Web"}, {ID: "u2", BankID: "acme::Web"}}
	restored := restoreBankID(units, "alice::Web")
	for _, unit := range restored {
		if unit.BankID != "alice::Web" {
			t.Fatalf("unit %s bank = %q, want the requested bank", unit.ID, unit.BankID)
		}
	}
	if restored[0].ID != "u1" {
		t.Fatalf("unit id changed to %q; it must stay the team bank's own", restored[0].ID)
	}
}

// schemaAwarePresence records the schema its presence check ran under, which is
// what the PostgreSQL backend resolves a table name from.
type schemaAwarePresence struct {
	Store
	has     bool
	queried []string
}

func (p *schemaAwarePresence) BankHasUnits(ctx context.Context, bankID string) (bool, error) {
	schema, _ := SchemaFrom(ctx)
	p.queried = append(p.queried, schema+"/"+bankID)
	return p.has, nil
}

// TestFallbackChecksPresenceInTheTeamSchema pins the bug the live service showed:
// a store resolves its schema from the context, so asking whether the team bank
// holds units on the *caller's* context looks inside the member's schema and
// always answers false — the fallback then never fires.
func TestFallbackChecksPresenceInTheTeamSchema(t *testing.T) {
	presence := &schemaAwarePresence{has: true}
	callerCtx, err := WithSchema(context.Background(), "alice")
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	_, servedBy, err := withFallback[string](callerCtx, presence,
		FallbackQuery{BankID: "alice::Web"}, scope,
		func(_ context.Context, bankID string) (string, error) { return bankID, nil })
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if len(presence.queried) != 1 || presence.queried[0] != "acme/acme::Web" {
		t.Fatalf("presence was checked as %v, want acme/acme::Web", presence.queried)
	}
	if servedBy != "acme::Web" {
		t.Fatalf("servedBy = %q, want the team bank to serve the read", servedBy)
	}
}

// TestFallbackReadsTheTeamBankInItsSchema pins that the redirected read itself
// runs under the team's schema, not the caller's.
func TestFallbackReadsTheTeamBankInItsSchema(t *testing.T) {
	presence := &schemaAwarePresence{has: true}
	callerCtx, err := WithSchema(context.Background(), "alice")
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	scope := &FallbackScope{Schema: "acme", BankID: "acme::Web"}
	var readSchema string
	_, servedBy, err := withFallback[string](callerCtx, presence,
		FallbackQuery{BankID: "alice::Web"}, scope,
		func(ctx context.Context, bankID string) (string, error) {
			readSchema, _ = SchemaFrom(ctx)
			return bankID, nil
		})
	if err != nil {
		t.Fatalf("withFallback: %v", err)
	}
	if servedBy != "acme::Web" || readSchema != "acme" {
		t.Fatalf("read ran in schema %q servedBy %q, want acme/acme::Web", readSchema, servedBy)
	}
}
