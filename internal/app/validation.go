package app

import (
	"fmt"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/httpx"
)

// tagScopeValidatorName is the class path the reference's system-test suite
// configures. A deployment that names it gets the native implementation of the
// same contract, so a validator that is only ever a Python extension for the
// reference is an ordinary configuration here.
const tagScopeValidatorName = "UserTagScope"

// apiKeyTenantClassName is the class the built-in tenant authenticator is known
// by. It is the one HTTP extension class this build loads, and selecting it is
// what enables API-key authentication (see Config.ResolveTenantAuth).
const apiKeyTenantClassName = "ApiKeyTenantExtension"

// registerConfiguredValidator installs the operation validator a deployment's
// configuration names. An empty name leaves the host without one, which is the
// unextended behaviour: every operation allowed and every bank write accepted.
func registerConfiguredValidator(host *httpx.ExtensionHost, cfg config.Config) error {
	name := strings.TrimSpace(cfg.OperationValidatorExtension)
	if name == "" {
		return nil
	}
	class := extensionClassName(name)
	switch class {
	case tagScopeValidatorName:
		host.RegisterOperationValidator(instrumentOperationValidator(httpx.UserTagScopeValidator{}))
		return nil
	default:
		return fmt.Errorf("%s names an operation validator this build does not implement: %q",
			config.EnvOperationValidatorExtension, name)
	}
}

// extensionClassName is the class part of a "<module>:<class>" selector. The
// reference identifies an extension by its class and leaves the module to the
// deployment's file layout, so a bare class name is accepted too.
func extensionClassName(name string) string {
	_, class, found := strings.Cut(name, ":")
	if !found {
		class = name
	}
	return strings.TrimSpace(class)
}

// registerConfiguredExtension loads the HTTP extension a deployment selected.
// A missing selector loads nothing, which is the reference's default. A selector
// naming a class this build cannot load refuses startup instead of mounting an
// empty host: a deployment that asked for an extension and silently got none
// would be worse off than one that will not start.
func registerConfiguredExtension(host *httpx.ExtensionHost, cfg config.Config) error {
	class := extensionClassName(cfg.TenantExtension)
	if class == "" {
		return nil
	}
	if strings.EqualFold(class, apiKeyTenantClassName) {
		// The authenticator is applied by the router's credential middleware
		// rather than registered as an extension: it is part of the request
		// path, not a route family. Loading it here is what makes the selection
		// a successful load.
		return nil
	}
	return fmt.Errorf("%s names an HTTP extension this build does not implement: %q",
		config.EnvTenantExtension, cfg.TenantExtension)
}
