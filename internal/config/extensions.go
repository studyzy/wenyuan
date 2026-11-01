package config

import (
	"fmt"
	"os"
	"strings"
)

// This file holds the extension seam: which extension class a deployment
// selected (the tenant authenticator, the operation validator) and how its
// prefixed settings resolve. They live together here rather than in the central
// configuration file so that adding a supported extension class is a change to
// one family, not to the file that describes every key.
//
// The prefixed-setting readers use the same environment lookup the rest of the
// package does; an extension-prefixed key is simply another key.

// HttpExtensionPrefix is the environment prefix an HTTP extension's own
// settings are grouped under. A deployment selects an extension by its class
// path and passes that extension string settings under
// <prefix><setting>; the prefix is the selection's own namespace so an
// extension setting can never collide with a core key.
//
// The settings are read only when an extension is selected (see
// ResolveHTTPExtensions): an unselected deployment has no extension to hand
// them to, and the reference passes them with the extension class rather than
// to the application.
const HttpExtensionPrefix = "WENYUAN_API_EXTENSION_"

// ExtensionSettingsPrefix is the prefix of the members of ExtensionSettings
// that are extension configuration, as opposed to the destination's own
// passthrough-header key that syntactically shares the prefix.
const ExtensionSettingsPrefix = HttpExtensionPrefix + "SETTING_"

// HTTPExtensionSelection is one configured HTTP extension: the class path a
// deployment named, the extension-only settings that came with it, and the
// allowlist of request headers it may read.
type HTTPExtensionSelection struct {
	// Class is the configured selector, spelled "<module>:<class>" as the
	// reference spells an extension.
	Class string
	// Settings are the deployment's extension-prefixed values, keyed by the
	// setting name with the prefix removed and the name lower-cased. They are
	// handed to the extension and to nothing else.
	Settings map[string]string
	// PassthroughHeaders is the operator's allowlist of request headers copied
	// into the extension's request context. It belongs to the selection
	// because it is an extension-only setting: with no extension selected the
	// list is inert.
	PassthroughHeaders []string
}

// ResolveHTTPExtensions reads the deployment's extension selection and its
// prefixed settings. A missing selector resolves to an empty slice — no
// extension, not an error — while a selector that is set is returned as named,
// for the composition root to load or refuse.
//
// Only keys carrying the settings prefix are collected. The passthrough-header
// switch is the destination's own key, so it is excluded here and read through
// extensionPassThroughHeaders; the reverse case — a setting that happens to
// share the prefix — is kept, because the settings prefix is the narrower and
// more specific one.
func ResolveHTTPExtensions(lookup func(string) (string, bool)) []HTTPExtensionSelection {
	class := strings.TrimSpace(stringFromEnv(lookup, EnvTenantExtension))
	if class == "" {
		return nil
	}
	return []HTTPExtensionSelection{{
		Class:              class,
		Settings:           extensionSettings(lookup),
		PassthroughHeaders: extensionPassThroughHeaders(lookup),
	}}
}

// ExtensionSelected reports whether the deployment named an HTTP extension.
func (c Config) ExtensionSelected() bool {
	return strings.TrimSpace(c.TenantExtension) != ""
}

// ExtensionSettings returns the extension-only settings resolved for this
// deployment, or nil when none was selected. Ordinary routes never read these;
// they exist to be handed to the extension the operator selected.
func (c Config) ExtensionSettings() map[string]string {
	if !c.ExtensionSelected() {
		return nil
	}
	return c.httpExtensionSettings
}

// extensionSettings collects every <prefix>SETTING_* key the environment holds.
func extensionSettings(lookup func(string) (string, bool)) map[string]string {
	settings := map[string]string{}
	for _, key := range os.Environ() {
		name, _, found := strings.Cut(key, "=")
		if !found || !strings.HasPrefix(name, ExtensionSettingsPrefix) {
			continue
		}
		value, ok := lookup(name)
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		settings[strings.ToLower(strings.TrimPrefix(name, ExtensionSettingsPrefix))] = strings.TrimSpace(value)
	}
	return settings
}

// extensionPassThroughHeaders reads the header list an extension may see from
// the request, which the reference spells as one of the extension's own
// prefixed settings.
func extensionPassThroughHeaders(lookup func(string) (string, bool)) []string {
	return stringListFromEnv(lookup, EnvExtensionPassThroughHdr)
}

// BuiltinAPIKeyTenantExtension is the reference's built-in tenant authenticator,
// selected by WENYUAN_API_TENANT_EXTENSION from the package that ships it.
// Selecting it requires WENYUAN_API_TENANT_API_KEY.
const BuiltinAPIKeyTenantExtension = "hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension"

// TenantAuth is the resolved tenant authentication policy.
type TenantAuth struct {
	// APIKey is the credential the authenticator accepts. Empty means no
	// authentication is configured, which is the reference's default.
	APIKey string
	// MCPAuthDisabled exempts the MCP namespace from authentication.
	MCPAuthDisabled bool
}

// Enabled reports whether requests are authenticated at all.
func (a TenantAuth) Enabled() bool { return a.APIKey != "" }

// ResolveTenantAuth validates the tenant extension selection.
//
// The reference loads the configured class from an importable module. A Go
// build cannot import a Python class, so the one extension whose behavior is
// specified here is supported by name and every other selection is refused at
// startup: a deployment that asked for authentication and silently got none
// would be worse off than one that will not start.
func (c Config) ResolveTenantAuth() (TenantAuth, error) {
	if c.TenantExtension == "" {
		return TenantAuth{}, nil
	}
	if !strings.EqualFold(strings.TrimSpace(c.TenantExtension), BuiltinAPIKeyTenantExtension) {
		return TenantAuth{}, fmt.Errorf(
			"%s=%q is not supported by this build: only the built-in %q is implemented",
			EnvTenantExtension, c.TenantExtension, BuiltinAPIKeyTenantExtension)
	}
	if strings.TrimSpace(c.TenantAPIKey) == "" {
		return TenantAuth{}, fmt.Errorf(
			"%s is required when using ApiKeyTenantExtension", EnvTenantAPIKey)
	}
	return TenantAuth{APIKey: c.TenantAPIKey, MCPAuthDisabled: c.TenantMCPAuthDisabled}, nil
}
