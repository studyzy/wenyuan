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

// registerConfiguredValidator installs the operation validator a deployment's
// configuration names. An empty name leaves the host without one, which is the
// unextended behaviour: every operation allowed and every bank write accepted.
func registerConfiguredValidator(host *httpx.ExtensionHost, cfg config.Config) error {
	name := strings.TrimSpace(cfg.OperationValidatorExtension)
	if name == "" {
		return nil
	}
	// The reference names an extension by "<module>:<class>", so the class is
	// what identifies it; the module is the deployment's file layout.
	_, class, found := strings.Cut(name, ":")
	if !found {
		class = name
	}
	switch strings.TrimSpace(class) {
	case tagScopeValidatorName:
		host.RegisterOperationValidator(instrumentOperationValidator(httpx.UserTagScopeValidator{}))
		return nil
	default:
		return fmt.Errorf("%s names an operation validator this build does not implement: %q",
			config.EnvOperationValidatorExtension, name)
	}
}
