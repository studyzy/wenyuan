package config

import (
	"strings"
	"testing"
)

// t-extension-selector: a missing selector resolves no extension rather than
// an error, which is the reference's default.
func TestResolveHTTPExtensionsWithoutSelectorResolvesNone(t *testing.T) {
	if got := ResolveHTTPExtensions(envLookup(map[string]string{})); got != nil {
		t.Fatalf("ResolveHTTPExtensions() = %v, want nil", got)
	}
	var zero Config
	if zero.ExtensionSelected() {
		t.Fatal("a config with no selector must not report an extension")
	}
	if zero.ExtensionSettings() != nil {
		t.Fatal("a config with no selector must expose no extension settings")
	}
}

// t-extension-selector: a selector is returned as named, without this package
// deciding whether it is loadable — that is the composition root's refusal.
func TestResolveHTTPExtensionsReturnsTheSelectorAsNamed(t *testing.T) {
	class := "hindsight_api.extensions.builtin.tenant:ApiKeyTenantExtension"
	got := ResolveHTTPExtensions(envLookup(map[string]string{EnvTenantExtension: class}))
	if len(got) != 1 {
		t.Fatalf("ResolveHTTPExtensions() = %v, want one selection", got)
	}
	if got[0].Class != class {
		t.Fatalf("Class = %q, want %q", got[0].Class, class)
	}
}

// t-extension-selector: the extension-prefixed settings are collected only when
// an extension is selected, and the destination's own passthrough-header key is
// neither renamed nor duplicated as a setting.
func TestExtensionSettingsRequireASelectionAndExcludeThePassthroughKey(t *testing.T) {
	settingKey := ExtensionSettingsPrefix + "REGION"
	env := map[string]string{
		EnvTenantExtension:         "pkg:Class",
		settingKey:                 "eu-west ",
		EnvExtensionPassThroughHdr: "X-Tenant-Id",
	}

	envLookupFn := func(key string) (string, bool) {
		value, ok := env[key]
		if !ok || strings.TrimSpace(value) == "" {
			return "", false
		}
		return value, true
	}

	// The settings reader walks the process environment, so the keys have to be
	// exported for the duration of the test; unset by the test framework's own
	// restore so a failure cannot leak them into another case.
	for key, value := range env {
		t.Setenv(key, value)
	}
	cfg := FromEnv(envLookupFn)
	settings := cfg.ExtensionSettings()
	if settings["region"] != "eu-west" {
		t.Fatalf("settings = %v, want region=eu-west (trimmed, prefix stripped, lower-cased)", settings)
	}
	if _, ok := settings["passthrough_headers"]; ok {
		t.Fatalf("the passthrough-header key must not be duplicated as a setting: %v", settings)
	}
	if _, ok := settings["pass_through_hdr"]; ok {
		t.Fatalf("the passthrough-header key must not be duplicated as a setting: %v", settings)
	}
}

// A deployment that never selected an extension gets no settings even when
// extension-prefixed keys are present: there is nothing to hand them to.
func TestExtensionSettingsAreAbsentWithoutASelection(t *testing.T) {
	t.Setenv(ExtensionSettingsPrefix+"REGION", "eu-west")
	cfg := FromEnv(envLookup(map[string]string{}))
	if got := cfg.ExtensionSettings(); got != nil {
		t.Fatalf("ExtensionSettings() = %v, want nil without a selection", got)
	}
}
