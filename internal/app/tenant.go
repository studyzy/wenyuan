package app

import (
	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// NewTenantExtension builds the tenant extension the process resolves schemas
// through.
//
// The default is the single-tenant extension: one schema, taken from the
// configuration, with no authentication. It reproduces the behaviour the
// service had before the abstraction existed, so a deployment that configures
// nothing is unaffected. A deployment needing real isolation supplies its own
// implementation here and no caller changes.
func NewTenantExtension(cfg config.Config) tenant.Extension {
	return tenant.NewDefault(cfg.DatabaseSchema)
}
