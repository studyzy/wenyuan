package provider

import (
	"sort"
	"strings"
)

// Constructor builds a provider from resolved settings.
type Constructor func(Settings) (Provider, error)

// Registration describes one selectable provider.
type Registration struct {
	// Name is the provider name an operator configures.
	Name string
	// RequiresCredential reports whether selection fails without an API key.
	RequiresCredential bool
	// DefaultBaseURL is applied when the operator configures no endpoint.
	DefaultBaseURL string
	// Build constructs the adapter.
	Build Constructor
}

// Registry maps provider names to adapters. It is built explicitly by the
// composition root so no package-level mutable state is shared between tests.
type Registry struct {
	registrations map[string]Registration
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{registrations: map[string]Registration{}}
}

// Register adds or replaces one provider. A blank name is ignored.
func (r *Registry) Register(reg Registration) {
	if r.registrations == nil {
		r.registrations = map[string]Registration{}
	}
	name := strings.TrimSpace(reg.Name)
	if name == "" {
		return
	}
	reg.Name = name
	r.registrations[name] = reg
}

// Lookup returns the registration for a name.
func (r *Registry) Lookup(name string) (Registration, bool) {
	reg, ok := r.registrations[strings.TrimSpace(name)]
	return reg, ok
}

// Names returns the registered provider names in sorted order, so error
// messages and configuration checks agree on one canonical list.
func (r *Registry) Names() []string {
	out := make([]string, 0, len(r.registrations))
	for name := range r.registrations {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}
