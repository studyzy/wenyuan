package app

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/adminapi"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
)

// newTeamRegistry opens the team registry over the store's database, or an
// in-process one beside the in-memory tenant registry.
//
// The in-memory pair shares the tenant map: a team created through the control
// plane is the same row the tenant routes see, which is what lets the team tests
// run without PostgreSQL.
func newTeamRegistry(cfg config.Config, store storage.Store, tenants storage.TenantRegistry) (storage.TeamRegistry, error) {
	provider, ok := store.(adminDBProvider)
	if !ok {
		if memory, ok := tenants.(*storage.MemoryTenantRegistry); ok {
			return storage.NewMemoryTeamRegistry(memory), nil
		}
		return storage.NewMemoryTeamRegistry(nil), nil
	}
	// The same PostgreSQL registry implements both interfaces: teams live in the
	// same schema as the tenants they reference.
	return storage.NewPostgresTenantRegistry(provider.AdminDB()), nil
}

// newBankAdmin returns the management plane's handle on a team's banks.
//
// A store with a database gets the multi-tenant PostgreSQL handle, which
// addresses the team's own schema. The in-memory backend is one schema, so its
// handle goes to the store directly; creating and deleting a project then work
// without PostgreSQL, which is what keeps the team plane testable offline.
func newBankAdmin(store storage.Store) adminapi.BankAdmin {
	provider, ok := store.(adminDBProvider)
	if !ok {
		return &memoryBankAdmin{store: store}
	}
	db := provider.AdminDB()
	if db == nil {
		return &memoryBankAdmin{store: store}
	}
	return &bankAdmin{store: storage.NewMultiTenantPostgresStore(db, storage.AdminSchema)}
}

// memoryBankAdmin runs the plane's bank operations on a single-schema store.
//
// The team schema in the path is not used: an in-memory store has one schema, so
// every tenant's banks live in it and the path's schema names nothing there.
type memoryBankAdmin struct {
	store storage.Store
}

// CreateBank creates bankID, reporting storage.ErrBankExists when it is there.
func (b *memoryBankAdmin) CreateBank(ctx context.Context, _, bankID, name string) error {
	_, err := b.store.CreateBank(ctx, storage.Bank{BankID: bankID, Name: name})
	if errors.Is(err, storage.ErrOperationConflict) {
		// The in-memory create reports a taken id as an operation conflict.
		return storage.ErrBankExists
	}
	return err
}

// ListBanks returns the bank ids that start with prefix, in id order.
func (b *memoryBankAdmin) ListBanks(ctx context.Context, _, prefix string) ([]string, error) {
	page, err := b.store.ListBanks(ctx, storage.BankQuery{Limit: -1})
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0)
	for _, bank := range page.Banks {
		if strings.HasPrefix(bank.BankID, prefix) {
			ids = append(ids, bank.BankID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

// DeleteBank removes one bank and its content.
func (b *memoryBankAdmin) DeleteBank(ctx context.Context, _, bankID string) (int, error) {
	return b.store.DeleteBank(ctx, bankID)
}

// bankAdmin runs bank operations against a named tenant schema.
type bankAdmin struct {
	store *storage.PostgresStore
}

// CreateBank creates the bank a project owns in the team's schema.
//
// It runs on a multi-tenant store, so the schema on the context is the one the
// row lands in; nothing here falls back to the process schema. A bank that is
// already there is reported as storage.ErrBankExists, the same answer
// CreateBank gives, so the caller distinguishes "taken" from "failed" without
// looking at the database's error text.
func (b *bankAdmin) CreateBank(ctx context.Context, schema, bankID, name string) error {
	schemaCtx, err := storage.WithSchema(ctx, schema)
	if err != nil {
		return err
	}
	_, err = b.store.CreateBank(schemaCtx, storage.Bank{BankID: bankID, Name: name})
	return err
}

// ListBanks returns the bank ids in schema that start with prefix.
//
// The prefix match uses left() rather than LIKE so a project name carrying "_"
// or "." cannot turn into a wildcard: the name is part of a bank id, not a
// pattern.
func (b *bankAdmin) ListBanks(ctx context.Context, schema, prefix string) ([]string, error) {
	schemaCtx, err := storage.WithSchema(ctx, schema)
	if err != nil {
		return nil, err
	}
	return b.store.ListBankIDs(schemaCtx, prefix)
}

// DeleteBank removes one bank and its content.
func (b *bankAdmin) DeleteBank(ctx context.Context, schema, bankID string) (int, error) {
	schemaCtx, err := storage.WithSchema(ctx, schema)
	if err != nil {
		return 0, err
	}
	return b.store.DeleteBank(schemaCtx, bankID)
}
