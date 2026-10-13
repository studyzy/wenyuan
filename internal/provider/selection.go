package provider

import (
	"fmt"
	"strings"
)

// Select maps resolved settings to a concrete provider. An unknown name is
// rejected with a typed invalid-provider error listing the accepted names; a
// provider that needs a credential raises a configuration error when none is
// set. There is never a silent fallback to a default provider.
func Select(registry *Registry, settings Settings) (Provider, error) {
	name := strings.TrimSpace(settings.Provider)
	reg, err := resolveName(registry, name)
	if err != nil {
		return nil, err
	}
	if reg.RequiresCredential && strings.TrimSpace(settings.APIKey) == "" {
		return nil, &MissingCredentialError{
			Provider:  reg.Name,
			ConfigKey: ConfigKeyAPIKey,
		}
	}
	resolved := settings
	resolved.Provider = reg.Name
	if strings.TrimSpace(resolved.BaseURL) == "" && reg.DefaultBaseURL != "" {
		resolved.BaseURL = reg.DefaultBaseURL
	}
	built, err := reg.Build(resolved)
	if err != nil {
		return nil, fmt.Errorf("build provider %q: %w", reg.Name, err)
	}
	return built, nil
}

// resolveName looks up a name, returning a typed invalid-provider error that
// lists the accepted names when it is unknown or blank.
func resolveName(reg *Registry, name string) (Registration, error) {
	if reg == nil {
		return Registration{}, &InvalidProviderError{Name: name, Accepted: nil}
	}
	accepted := reg.Names()
	if name == "" {
		return Registration{}, &InvalidProviderError{Name: name, Accepted: accepted}
	}
	found, ok := reg.Lookup(name)
	if !ok {
		return Registration{}, &InvalidProviderError{Name: name, Accepted: accepted}
	}
	return found, nil
}

// ValidateSelection reports whether a resolved name is selectable without
// building the adapter, so configuration checks can agree with selection.
func ValidateSelection(reg *Registry, name string) error {
	_, err := resolveName(reg, strings.TrimSpace(name))
	return err
}

// AcceptedNames returns the provider names a registry accepts.
func AcceptedNames(reg *Registry) []string {
	if reg == nil {
		return nil
	}
	return reg.Names()
}
