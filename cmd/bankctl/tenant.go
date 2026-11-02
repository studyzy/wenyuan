package main

import (
	"context"
	"strings"

	"github.com/studyzy/wenyuan/internal/app"
	"github.com/studyzy/wenyuan/internal/bankadmin"
	"github.com/studyzy/wenyuan/internal/config"
)

// splitTenantOption lifts the --tenant/--tenant-id selector out of the argument
// list. It is a global option rather than a command flag because the tenant
// decides which schema the store is opened against, and that happens before the
// command runs.
func splitTenantOption(args []string) (string, []string, error) {
	tenant := ""
	rest := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--tenant" || arg == "--tenant-id":
			if i+1 >= len(args) {
				return "", nil, bankadmin.NewUsageError("flag %s needs a value", arg)
			}
			i++
			tenant = args[i]
		case strings.HasPrefix(arg, "--tenant=") || strings.HasPrefix(arg, "--tenant-id="):
			tenant = arg[strings.Index(arg, "=")+1:]
		default:
			rest = append(rest, arg)
		}
	}
	return strings.TrimSpace(tenant), rest, nil
}

// resolveImportSchema decides which schema an archive import writes into. In a
// multi-tenant deployment the tenant registry is the authority: the archive's
// bank id names the harness that produced it (coding-agent::wenyuan), not a
// tenant, so the caller's --tenant is what places the copy.
func resolveImportSchema(ctx context.Context, cfg config.Config, explicitSchema, tenantID string) (string, error) {
	scope := bankadmin.ImportScope{
		MultiTenant:      cfg.TenantMode == config.TenantModeMulti,
		TenantID:         tenantID,
		Schema:           explicitSchema,
		ConfiguredSchema: cfg.DatabaseSchema,
	}
	if scope.MultiTenant && tenantID != "" {
		base, err := openStore(ctx, "")
		if err != nil {
			return "", err
		}
		extension, err := app.OpenTenantExtension(ctx, cfg, base)
		if err != nil {
			return "", err
		}
		active, err := extension.ListTenants(ctx)
		if err != nil {
			return "", err
		}
		scope.ActiveTenants = active
	}
	return scope.ResolveSchema()
}
