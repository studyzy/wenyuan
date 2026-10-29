package app

import (
	"context"
	"fmt"
	"net/http"

	"github.com/studyzy/wenyuan/internal/adminapi"
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// adminDBProvider is implemented by a store whose pool can also address the
// control plane's own tables. PostgresStore returns its migration handle when
// one is configured, so admin DDL never runs over the serving role.
type adminDBProvider interface {
	AdminDB() storage.DB
}

// newTenantRegistry opens the control-plane registry over the store's database.
// A backend without a database (the in-memory store) gets an in-process
// registry, which is what keeps development and tests working without
// PostgreSQL.
func newTenantRegistry(ctx context.Context, cfg config.Config, store storage.Store) (storage.TenantRegistry, error) {
	if provider, ok := store.(adminDBProvider); ok {
		db := provider.AdminDB()
		if cfg.RunMigrationsOnStartup {
			if err := storage.MigrateAdminSchema(ctx, db); err != nil {
				return nil, fmt.Errorf("admin migrations failed: %w", err)
			}
		}
		return storage.NewPostgresTenantRegistry(db), nil
	}
	return storage.NewMemoryTenantRegistry(), nil
}

// OpenTenantExtension returns the extension a background process resolves
// tenants through: the single-tenant default, or the registry in multi mode.
func OpenTenantExtension(ctx context.Context, cfg config.Config, store storage.Store) (tenant.Extension, error) {
	if cfg.TenantMode != config.TenantModeMulti {
		return NewTenantExtension(cfg), nil
	}
	registry, err := newTenantRegistry(ctx, cfg, store)
	if err != nil {
		return nil, err
	}
	return NewRegistryExtension(registry), nil
}

// newAdminHandler builds the management plane over an already-opened registry.
// provisioner creates or adopts a tenant's schema; nil treats provisioning as
// a no-op, which is what the in-memory backend needs.
func newAdminHandler(
	cfg config.Config,
	registry storage.TenantRegistry,
	provisioner func(context.Context, string, bool) error,
	migrator func(context.Context, string) error,
	dropper func(context.Context, string) error,
) (http.Handler, error) {
	return adminapi.New(adminapi.Config{
		Registry:    registry,
		OperatorKey: cfg.AdminAPIKey,
		Actor:       "operator",
		Provisioner: provisioner,
		Migrator:    migrator,
		Dropper:     dropper,
	})
}
