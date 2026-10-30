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
			// A single-tenant deployment has no teams, so it does not get the
			// team tables: the registry it needs is 0001 alone.
			if err := storage.MigrateAdminSchemaFor(ctx, db, cfg.TenantMode == config.TenantModeMulti); err != nil {
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
	extension tenant.Extension,
	provisioner func(context.Context, string, bool) error,
	migrator func(context.Context, string) error,
	dropper func(context.Context, string) error,
	teams storage.TeamRegistry,
	banks adminapi.BankAdmin,
	merges adminapi.MergeAdmin,
	membershipChanged func(string),
) (http.Handler, error) {
	adminConfig := adminapi.Config{
		Registry:    registry,
		OperatorKey: cfg.AdminAPIKey,
		Actor:       "operator",
		Provisioner: provisioner,
		Migrator:    migrator,
		Dropper:     dropper,
		// Teams stay nil in single-tenant mode, where every team route answers
		// 501; that is the one place the plane differs between the two modes.
		TeamConfig: adminapi.TeamConfig{
			Teams: teams, Banks: banks, Merges: merges, MembershipChanged: membershipChanged,
		},
	}
	// The console turns a tenant key into a console session, and it needs the
	// tenant id to renew that session. Resolving through the same extension the
	// data plane authenticates with keeps one answer to "is this key valid": a
	// single-tenant deployment has no extension, so the endpoint stays 501.
	if extension != nil {
		adminConfig.ResolveKey = func(ctx context.Context, credential string) (string, error) {
			resolved, err := extension.Authenticate(ctx, tenant.Request{Credential: credential})
			if err != nil {
				return "", err
			}
			return resolved.ID, nil
		}
	}
	return adminapi.New(adminConfig)
}
