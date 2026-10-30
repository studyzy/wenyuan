package storage

import "context"

// The team read fallback: when a bank holds nothing, its read is served from the
// team bank the caller is a member of.
//
// The fallback lives here rather than in the handlers because the decision changes
// which *schema* a read runs in, and everything downstream of a recall or a list —
// tag scoping, graph seeds, chunks, source facts, entity names — is already keyed
// by the query's bank. Doing it once, at the entry point, keeps one answer to
// "which bank did this request read" instead of one per call site.

// FallbackBank resolves the bank a read should use in place of one that holds
// nothing, or returns the empty scope when the read must stay where it is.
type FallbackPicker func(ctx context.Context, q FallbackQuery) (*FallbackScope, error)

// FallbackQuery is what a picker is told about a read it may redirect.
type FallbackQuery struct {
	// BankID is the bank the request named.
	BankID string
	// DocumentID and State are the filters a fallback cannot serve: the team's
	// bank holds no documents and no invalidated rows, so redirecting would
	// turn "this bank has archived facts" into "there is nothing here".
	DocumentID string
	State      string
}

// CanFallBack reports whether a filter combination may be redirected at all.
func (q FallbackQuery) CanFallBack() bool {
	if q.DocumentID != "" {
		return false
	}
	if q.State != "" && q.State != "valid" {
		return false
	}
	return true
}

// withFallback runs read against the team bank when the named bank is empty.
//
// The reads are not merged: a request is answered by exactly one bank. That is
// what makes the team view stable — a query the team's content does not match
// answers empty rather than silently falling through to the member's own facts.
func withFallback[T any](
	ctx context.Context,
	store Store,
	query FallbackQuery,
	fallback *FallbackScope,
	read func(ctx context.Context, bankID string) (T, error),
) (T, string, error) {
	var zero T
	if fallback == nil || fallback.BankID == "" || fallback.Schema == "" || !query.CanFallBack() {
		value, err := read(ctx, query.BankID)
		return value, query.BankID, err
	}
	// A store without the capability never falls back. That is the clause that
	// lets a backend with no team concept — and every test double — behave
	// exactly as it did before teams existed.
	presence, ok := store.(BankPresence)
	if !ok || presence == nil {
		value, err := read(ctx, query.BankID)
		return value, query.BankID, err
	}
	// The presence check runs in the *fallback's* schema, not the caller's. A
	// store resolves its schema from the context, so asking about the team bank on
	// the caller's context would look for "acme::Web" inside the member's own
	// schema and find nothing — the fallback would then never fire.
	scoped, err := WithSchema(ctx, fallback.Schema)
	if err != nil {
		return zero, "", err
	}
	hasUnits, err := presence.BankHasUnits(scoped, fallback.BankID)
	if err != nil {
		return zero, "", err
	}
	if !hasUnits {
		// The team has nothing yet: the member's own bank is the answer, which is
		// what a project looks like before its first merge.
		value, err := read(ctx, query.BankID)
		return value, query.BankID, err
	}
	value, err := read(scoped, fallback.BankID)
	return value, fallback.BankID, err
}

// restoreBankID rewrites the units a fallback read produced so the caller sees the
// bank it asked for.
//
// The unit ids stay the team bank's own: a client that fetched a unit by id must
// be able to address it again, and inventing a personal id would make the same
// fact look like two.
func restoreBankID(units []MemoryUnit, bankID string) []MemoryUnit {
	for i := range units {
		units[i].BankID = bankID
	}
	return units
}
