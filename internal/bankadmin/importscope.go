package bankadmin

import (
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// ImportScope is what an archive import knows about the tenant it writes into.
//
// The two implementations spell a bank id differently: the reference names the
// harness that produced the bank (coding-agent::wenyuan), while this service
// names the tenant that owns it (studyzy::wenyuan). The prefix therefore cannot
// be carried across — the caller says which tenant the copy belongs to, and the
// import re-homes the bank under it.
type ImportScope struct {
	// MultiTenant is true when the deployment resolves schemas through the
	// tenant registry (tenant_mode: multi).
	MultiTenant bool
	// TenantID is the explicit --tenant selection. It is required in
	// multi-tenant deployments: the archive's bank id says nothing about which
	// tenant schema the copy belongs in.
	TenantID string
	// Schema is the explicit --schema override, possibly empty.
	Schema string
	// ConfiguredSchema is the deployment's base schema, used in single-tenant
	// mode when no override was given.
	ConfiguredSchema string
	// ActiveTenants is the registry's active set, populated in multi-tenant
	// mode so the tenant's own schema can be resolved.
	ActiveTenants []tenant.Tenant
}

// ResolveSchema decides which schema an archive import writes into.
func (s ImportScope) ResolveSchema() (string, error) {
	if !s.MultiTenant {
		schema := s.Schema
		if schema == "" {
			schema = s.ConfiguredSchema
		}
		if s.TenantID != "" && s.TenantID != schema {
			return "", NewUsageError("tenant %q is not the schema this deployment serves (%q); a single-tenant deployment has one tenant", s.TenantID, schema)
		}
		return schema, nil
	}
	if strings.TrimSpace(s.TenantID) == "" {
		return "", NewUsageError("--tenant is required to import a bank: this deployment keeps each tenant's banks in that tenant's own schema")
	}
	for _, active := range s.ActiveTenants {
		if active.ID != s.TenantID {
			continue
		}
		schema := active.Schema
		if schema == "" {
			schema = active.ID
		}
		if s.Schema != "" && s.Schema != schema {
			return "", NewUsageError("--schema %q does not match tenant %q, whose schema is %q", s.Schema, s.TenantID, schema)
		}
		return schema, nil
	}
	return "", fmt.Errorf("tenant %q is not an active tenant", s.TenantID)
}

// ImportTargetBankID decides the bank id an import writes under: the explicit
// override, or the archive's source name re-homed under the tenant.
//
// The reference's source bank is coding-agent::wenyuan, whose prefix names the
// harness; the part after it is the bank's name. A tenant import keeps that
// name and puts the tenant in front, so the same archive lands at
// studyzy::wenyuan without the caller restating the name.
func ImportTargetBankID(sourceBankID, tenantID, override string) (string, error) {
	target := strings.TrimSpace(override)
	switch {
	case target == "" && tenantID == "":
		target = sourceBankID
	case target == "":
		name := sourceBankID
		if index := strings.Index(name, "::"); index >= 0 {
			name = name[index+2:]
		}
		name = strings.TrimSpace(name)
		if name == "" {
			return "", fmt.Errorf("cannot derive a target bank id from %q; pass --target-bank", sourceBankID)
		}
		target = tenantID + "::" + name
	case tenantID != "" && !strings.HasPrefix(target, tenantID+"::"):
		return "", NewUsageError("--target-bank %q must be scoped to tenant %q (<tenant>::<name>)", target, tenantID)
	}
	if !storage.ValidBankID(target) {
		return "", NewUsageError("invalid bank id %q: it must be non-empty, at most %d bytes of valid UTF-8, free of control characters, and must not start with '_'", target, storage.BankIDMaxBytes)
	}
	return target, nil
}
