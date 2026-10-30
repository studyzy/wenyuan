package app

import (
	"context"

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

// newBankAdmin returns the management plane's handle on a team's banks, when the
// store has a database the plane can address. Without one — the in-memory
// backend — deleting a project cannot delete its banks, so the toggle is left
// unset and that one route answers 501 rather than pretending.
func newBankAdmin(store storage.Store) adminapi.BankAdmin {
	provider, ok := store.(adminDBProvider)
	if !ok {
		return nil
	}
	db := provider.AdminDB()
	if db == nil {
		return nil
	}
	return &bankAdmin{store: storage.NewMultiTenantPostgresStore(db, storage.AdminSchema)}
}

// bankAdmin runs bank operations against a named tenant schema.
type bankAdmin struct {
	store *storage.PostgresStore
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
