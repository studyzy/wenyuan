package config

import (
	"fmt"
	"strings"
)

const (
	// TenantModeSingle serves one schema fixed by the process configuration.
	TenantModeSingle = "single"
	// TenantModeMulti resolves the schema from the tenant registry per request.
	TenantModeMulti = "multi"
)

// ValidateTenancy checks the control-plane configuration. The management plane
// is fail-closed: enabling its listener without an operator key would expose
// tenant administration to any caller that can reach the port.
func (c Config) ValidateTenancy() error {
	switch mode := strings.ToLower(strings.TrimSpace(c.TenantMode)); mode {
	case "", TenantModeSingle, TenantModeMulti:
	default:
		return fmt.Errorf("%s must be %q or %q, got %q", EnvTenantMode, TenantModeSingle, TenantModeMulti, c.TenantMode)
	}
	if strings.TrimSpace(c.AdminListen) == "" {
		return nil
	}
	if strings.TrimSpace(c.AdminAPIKey) == "" {
		return fmt.Errorf("%s is required when %s is set", EnvAdminAPIKey, EnvAdminListen)
	}
	// The supervisor shares the data-plane listener through an inherited file
	// descriptor; the management listener has no such handoff yet, and two
	// workers binding it would collide. Refuse rather than bind at random.
	if c.Workers > 1 {
		return fmt.Errorf("%s requires a single worker process for now (workers=%d)", EnvAdminListen, c.Workers)
	}
	return nil
}
