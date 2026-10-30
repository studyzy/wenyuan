package storage

import "context"

// BankPresence is the optional capability recall and the memory list need to
// decide the team fallback: whether a bank holds any unit at all.
//
// "Any" is what makes the fallback decision per-bank rather than per-query. Once
// a team bank holds content it is the source, even for a query that matches none
// of it, so one member asking two questions sees one stable view rather than the
// team's answer to one and their own to the other.
//
// It is a capability rather than a Store method so a backend with no team concept
// — and every test double — keeps compiling untouched. A store that does not
// implement it never falls back, which is exactly the single-tenant default.
type BankPresence interface {
	// BankHasUnits reports whether a bank holds any unit of any fact type.
	BankHasUnits(ctx context.Context, bankID string) (bool, error)
}

// FallbackScope names another tenant's bank to read when this one is empty.
//
// It is how a team member's read reaches their project's shared content: the
// member's own bank is the caller's, and the team's bank holds what the merge
// produced. The schema is explicit rather than derived from the bank id, because
// a bank id can be forged while a resolved scope cannot — the resolver looks the
// membership up in the registry first.
type FallbackScope struct {
	// Schema is the other tenant's schema.
	Schema string
	// BankID is the bank to read in it.
	BankID string
}
