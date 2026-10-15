package config

import (
	"fmt"
	"strings"
)

// UnsupportedKey is one configured environment key this build recognizes but
// cannot act on, with an optional short note for the operator.
type UnsupportedKey struct {
	Key    string
	Reason string
}

// Unsupported collects the environment keys that were set but cannot be
// honoured. It is the explicit answer to a silent no-op: a reference key this
// build parses but does not implement is refused at startup rather than
// accepted and ignored, so an operator who configured a feature is told it is
// doing nothing instead of discovering later that it never did.
type Unsupported struct {
	Keys []UnsupportedKey
}

// Add records key as unsupported. A blank key is ignored, and a repeated key
// keeps its first reason so a key can never be recorded twice.
func (u *Unsupported) Add(key, reason string) {
	key = strings.TrimSpace(key)
	if key == "" {
		return
	}
	for i := range u.Keys {
		if u.Keys[i].Key == key {
			return
		}
	}
	u.Keys = append(u.Keys, UnsupportedKey{Key: key, Reason: reason})
}

// AddIfSet records key when the environment supplies a value for it.
func (u *Unsupported) AddIfSet(lookup func(string) (string, bool), key, reason string) {
	if _, ok := lookup(key); ok {
		u.Add(key, reason)
	}
}

// Empty reports whether no unsupported key was recorded.
func (u Unsupported) Empty() bool { return len(u.Keys) == 0 }

// Err returns nil when nothing was recorded, and otherwise an error naming
// every unsupported key so the operator can remove them in one pass.
func (u Unsupported) Err() error {
	if u.Empty() {
		return nil
	}
	names := make([]string, 0, len(u.Keys))
	for _, k := range u.Keys {
		names = append(names, k.Key)
	}
	return fmt.Errorf("these settings are not supported by this build: %s. "+
		"Remove them, or configure the feature that implements them",
		strings.Join(names, ", "))
}

// ValidateUnsupported reports the runtime keys the deployment set that this
// build cannot act on. ValidateSupported is the composition root's single check
// — it unions these with the provider family — while this narrower accessor
// lets a caller reason about the runtime family alone.
func (c Config) ValidateUnsupported() error {
	if c.Unsupported == nil {
		return nil
	}
	return c.Unsupported.Err()
}

// unsupportedFromProviderFields folds provider settings a caller set directly on
// the struct — rather than through the environment — into the same list, so the
// single check sees them too.
func (c *Config) recordUnsupportedProviderFields() {
	if c.Unsupported == nil {
		c.Unsupported = &Unsupported{}
	}
	for _, key := range c.UnsupportedProviderKeys() {
		c.Unsupported.Add(key, ProviderRefusalReason(key))
	}
}
