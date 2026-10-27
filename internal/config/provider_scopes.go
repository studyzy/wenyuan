package config

import (
	"fmt"
	"strconv"
	"strings"
)

// Strict-schema and temperature configuration keys.
const (
	EnvLLMStrictSchema              = "WENYUAN_API_LLM_STRICT_SCHEMA"
	EnvLLMStrictSchemaConsolidation = "WENYUAN_API_LLM_STRICT_SCHEMA_CONSOLIDATION"
	EnvLLMStrictSchemaReflect       = "WENYUAN_API_LLM_STRICT_SCHEMA_REFLECT"
	EnvLLMStrictSchemaRetain        = "WENYUAN_API_LLM_STRICT_SCHEMA_RETAIN"

	EnvLLMTemperature              = "WENYUAN_API_LLM_TEMPERATURE"
	EnvLLMTemperatureConsolidation = "WENYUAN_API_LLM_TEMPERATURE_CONSOLIDATION"
	EnvLLMTemperatureReflect       = "WENYUAN_API_LLM_TEMPERATURE_REFLECT"
	EnvLLMTemperatureRetain        = "WENYUAN_API_LLM_TEMPERATURE_RETAIN"
	EnvLLMTemperatureVerification  = "WENYUAN_API_LLM_TEMPERATURE_VERIFICATION"
)

// TemperatureOmit is the documented sentinel that removes the temperature
// parameter entirely at any layer.
const TemperatureOmit = "omit"

// DefaultLLMStrictSchema is the built-in structured-output strictness.
const DefaultLLMStrictSchema = false

// Task-appropriate built-in temperature defaults per operation. Verification
// and consolidation are deterministic; retain extracts conservatively;
// reflect thinks more freely.
const (
	DefaultTemperatureVerification  = 0.0
	DefaultTemperatureConsolidation = 0.0
	DefaultTemperatureRetain        = 0.2
	DefaultTemperatureReflect       = 0.9
)

// scopeKeys returns the strict-schema and temperature key list so resolution
// can be checked against the contract.
func scopeKeys() []string {
	return []string{
		EnvLLMStrictSchema, EnvLLMStrictSchemaConsolidation, EnvLLMStrictSchemaReflect,
		EnvLLMStrictSchemaRetain,
		EnvLLMTemperature, EnvLLMTemperatureConsolidation, EnvLLMTemperatureReflect,
		EnvLLMTemperatureRetain, EnvLLMTemperatureVerification,
	}
}

// ScopeKeys returns every strict-schema and temperature key.
func ScopeKeys() []string { return append([]string(nil), scopeKeys()...) }

// ScopedConfig is the resolved per-operation override configuration.
type ScopedConfig struct {
	// StrictSchema is the global structured-output strictness.
	StrictSchema bool
	// StrictSchemaByOperation materializes the resolved strictness per
	// operation so a per-operation false can override a global true rather
	// than being treated as unset.
	StrictSchemaByOperation map[string]bool
	// TemperatureByOperation holds the resolved temperature per operation; a
	// nil entry means the parameter is removed for that operation.
	TemperatureByOperation map[string]*float64
	// Raw retains every scope key's value so a key is never dropped.
	Raw map[string]string
}

// Operations the strict-schema and temperature families have overrides for.
var strictSchemaOperations = []string{"retain", "reflect", "consolidation"}

// temperatureOperations is the operations with a temperature default.
var temperatureOperations = []string{"verification", "consolidation", "retain", "reflect"}

// ResolveScopes resolves the strict-schema and temperature families.
func ResolveScopes(lookup func(string) (string, bool)) ScopedConfig {
	cfg := ScopedConfig{
		StrictSchemaByOperation: map[string]bool{},
		TemperatureByOperation:  map[string]*float64{},
		Raw:                     map[string]string{},
	}
	for _, key := range scopeKeys() {
		if v, ok := lookup(key); ok {
			cfg.Raw[key] = v
		}
	}

	globalStrict, ok := boolFromLookup(lookup, EnvLLMStrictSchema)
	if !ok {
		globalStrict = DefaultLLMStrictSchema
	}
	cfg.StrictSchema = globalStrict
	for _, op := range strictSchemaOperations {
		resolved, present := boolFromLookup(lookup, strictSchemaKey(op))
		if !present {
			resolved = globalStrict
		}
		cfg.StrictSchemaByOperation[op] = resolved
	}

	globalTemp := temperatureFromLookup(lookup, EnvLLMTemperature)
	for _, op := range temperatureOperations {
		cfg.TemperatureByOperation[op] = resolveTemperature(
			temperatureFromLookup(lookup, temperatureKey(op)),
			globalTemp,
			defaultTemperatureFor(op),
		)
	}
	return cfg
}

// resolveTemperature applies per-operation, then global, then the built-in
// per-operation default. An explicit omit at any layer removes the parameter.
func resolveTemperature(perOp, global temperatureValue, fallback float64) *float64 {
	switch {
	case perOp.set && perOp.omit:
		return nil
	case perOp.set:
		v := perOp.value
		return &v
	case global.set && global.omit:
		return nil
	case global.set:
		v := global.value
		return &v
	default:
		v := fallback
		return &v
	}
}

// temperatureValue distinguishes an unset layer from an explicit omit and
// from a concrete numeric value.
type temperatureValue struct {
	set   bool
	omit  bool
	value float64
}

// temperatureFromLookup reads a temperature key, recognizing the omit sentinel.
func temperatureFromLookup(lookup func(string) (string, bool), key string) temperatureValue {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return temperatureValue{}
	}
	trimmed := strings.TrimSpace(v)
	if strings.EqualFold(trimmed, TemperatureOmit) || strings.EqualFold(trimmed, "off") ||
		strings.EqualFold(trimmed, "default") {
		return temperatureValue{set: true, omit: true}
	}
	f, err := strconv.ParseFloat(trimmed, 64)
	if err != nil {
		return temperatureValue{}
	}
	return temperatureValue{set: true, value: f}
}

// boolFromLookup reads a strict boolean key, reporting whether it was set and
// recognized. An ambiguous value is reported as unrecognized so the caller
// keeps the documented default rather than inventing one.
func boolFromLookup(lookup func(string) (string, bool), key string) (bool, bool) {
	v, ok := lookup(key)
	if !ok || strings.TrimSpace(v) == "" {
		return false, false
	}
	parsed, valid := parseBoolValue(v)
	if !valid {
		return false, false
	}
	return parsed, true
}

// StrictSchemaKeyFor returns the per-operation strict-schema key.
func StrictSchemaKeyFor(operation string) string { return strictSchemaKey(operation) }

// TemperatureKeyFor returns the per-operation temperature key.
func TemperatureKeyFor(operation string) string { return temperatureKey(operation) }

func strictSchemaKey(operation string) string {
	return EnvLLMStrictSchema + "_" + strings.ToUpper(operation)
}

func temperatureKey(operation string) string {
	return EnvLLMTemperature + "_" + strings.ToUpper(operation)
}

// defaultTemperatureFor returns the task-appropriate built-in default.
func defaultTemperatureFor(operation string) float64 {
	switch operation {
	case "verification":
		return DefaultTemperatureVerification
	case "consolidation":
		return DefaultTemperatureConsolidation
	case "retain":
		return DefaultTemperatureRetain
	case "reflect":
		return DefaultTemperatureReflect
	default:
		return DefaultTemperatureRetain
	}
}

// ParseCapabilityFlag parses a capability flag strictly, rejecting an
// ambiguous value rather than defaulting silently.
func ParseCapabilityFlag(key, value string) (bool, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return false, fmt.Errorf("%s must not be blank", key)
	}
	if parsed, valid := parseBoolValue(trimmed); valid {
		return parsed, nil
	}
	return false, fmt.Errorf("%s has an ambiguous value %q; use true or false", key, value)
}
