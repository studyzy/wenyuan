package config

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestReferenceRuntimeKeysAreWellFormed guards the contract table itself: a
// reference key must be spelled as one, and no key may appear twice. A
// duplicate would let one spelling silently shadow the other's decision.
func TestReferenceRuntimeKeysAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, k := range referenceRuntimeKeys {
		if !strings.HasPrefix(k.Name, "HINDSIGHT_API_") {
			t.Fatalf("reference key %q is not spelled as a reference key", k.Name)
		}
		if seen[k.Name] {
			t.Fatalf("duplicate reference key %q", k.Name)
		}
		seen[k.Name] = true
		if k.Delegated != "" && k.Reason != "" {
			t.Fatalf("reference key %q is delegated by %s but also carries a refusal reason", k.Name, k.Delegated)
		}
	}
}

// systemTestSuiteRuntimeKeys are the non-provider keys the reference's own
// system-test suite sets in the environment of the service under test. They
// must resolve: the suite refuses to start the service when any of them is
// refused, and that failure surfaces as a seven-minute end-to-end error rather
// than a unit failure. Keeping the list here turns it into a fast, checked-in
// assertion. Provider-family keys (LLM_*, EMBEDDINGS_*, RERANKER_*,
// LLM_LITELLMROUTER_CONFIG) are covered by the provider family's own contract
// test, not this table.
var systemTestSuiteRuntimeKeys = []string{
	EnvWorkerMaxRetries,
	EnvWorkerPollIntervalMS,
	EnvBankStatsCacheTTLSeconds,
	EnvMaintenanceStartJitterSeconds,
	EnvWebhookAllowedHosts,
	EnvConsolidationReconcileIntervalSeconds,
	EnvRunMigrationsOnStartup,
	EnvDatabaseURL,
	EnvHost,
	EnvPort,
	EnvLogLevel,
}

// TestSystemTestSuiteKeysResolve guards against a key the reference's own
// system-test suite sets being refused, which would stop that suite at startup.
func TestSystemTestSuiteKeysResolve(t *testing.T) {
	resolved := map[string]bool{}
	for _, k := range referenceRuntimeKeys {
		if k.Resolve != nil {
			resolved[k.Name] = true
		}
	}
	for _, key := range systemTestSuiteRuntimeKeys {
		if !resolved[key] {
			t.Errorf("%s is set by the reference system-test suite but is refused by this table", key)
		}
	}
}

// probeValues are non-default values the table test tries, in order, until one
// moves the resolved configuration. A single fixed sample per key would read
// better, but a value that is non-default for a boolean is the default for a
// string, so a small probe set keeps the assertion honest across the table
// without a hand-maintained sample per entry.
var probeValues = []string{
	"0", "false", "true", "1", "7", "s3", "native", "vchord", "debug",
	"a=b", "semantic:high", "/probe", "staging", "exponential",
}

// probeCompanions lists keys that only take effect alongside another key, so
// the probe sets both. The OpenTelemetry tracing flag is the one case: it is
// deliberately inert without an exporter endpoint.
var probeCompanions = map[string]map[string]string{
	EnvOTELTracesEnabled: {EnvOTELExporterOTLPEndpoint: "http://collector:4318"},
}

func envLookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

// expectedDefault parses the reference's recorded default into the concrete
// type the key's resolver returns, so the table test can hold the resolved
// default against the reference. It reports false for types it cannot parse
// (maps, slices, and an empty string default), which are compared only for
// emptiness by the resolver itself.
func expectedDefault(k referenceKey, got any) (any, bool) {
	switch got.(type) {
	case string:
		return k.Default, k.Default != ""
	case bool:
		value, err := strconv.ParseBool(k.Default)
		return value, err == nil
	case int:
		value, err := strconv.Atoi(k.Default)
		return value, err == nil
	case float64:
		value, err := strconv.ParseFloat(k.Default, 64)
		return value, err == nil
	case time.Duration:
		if k.Default == "" {
			return nil, false
		}
		unit := time.Second
		if strings.HasSuffix(k.Name, "_MS") {
			unit = time.Millisecond
		}
		seconds, err := strconv.ParseFloat(k.Default, 64)
		if err != nil {
			return nil, false
		}
		return time.Duration(seconds * float64(unit)), true
	default:
		return nil, false
	}
}

// TestReferenceRuntimeKeyContract is the table-driven contract for every
// reference runtime key. For each key it asserts exactly one of the two
// allowed outcomes:
//
//   - a resolved key changes the resolved configuration for some non-default
//     value, proving the build acts on it; or
//   - a refused key fails startup with an error that names it, proving a
//     copied reference .env is rejected rather than silently ignored.
func TestReferenceRuntimeKeyContract(t *testing.T) {
	base := FromEnv(envLookup(nil))
	if err := base.ValidateSupported(); err != nil {
		t.Fatalf("an empty environment must not be refused: %v", err)
	}

	for _, k := range referenceRuntimeKeys {
		t.Run(k.Name, func(t *testing.T) {
			if k.Resolve == nil {
				cfg := FromEnv(envLookup(map[string]string{k.Name: "probe"}))
				err := cfg.ValidateUnsupported()
				if k.Delegated != "" {
					// A key another package resolves must not be refused.
					if err != nil {
						t.Fatalf("%s is delegated to %s but was refused: %v", k.Name, k.Delegated, err)
					}
					return
				}
				if err == nil {
					t.Fatalf("%s is recorded as refused but startup accepted it", k.Name)
				}
				if !strings.Contains(err.Error(), k.Name) {
					t.Fatalf("refusal error does not name %s: %v", k.Name, err)
				}
				return
			}

			baseValue := k.Resolve(base)
			if k.Divergent == "" {
				if want, ok := expectedDefault(k, baseValue); ok && !reflect.DeepEqual(baseValue, want) {
					t.Errorf("%s default = %#v, want the reference default %q (%#v)",
						k.Name, baseValue, k.Default, want)
				}
			}
			for _, probe := range probeValues {
				values := map[string]string{k.Name: probe}
				for companionKey, companionValue := range probeCompanions[k.Name] {
					values[companionKey] = companionValue
				}
				with := FromEnv(envLookup(values))
				if err := with.ValidateSupported(); err != nil {
					t.Fatalf("%s is resolved but was refused: %v", k.Name, err)
				}
				if !reflect.DeepEqual(baseValue, k.Resolve(with)) {
					return
				}
			}
			t.Fatalf("%s never changed the resolved configuration for any probe value", k.Name)
		})
	}
}

// The extraction mode the deployment selects must be one the extractor
// implements: the reference refuses any other value at startup rather than
// silently extracting in a different shape.
func TestRetainExtractionModeValidation(t *testing.T) {
	for _, mode := range []string{"concise", "verbose", "custom", "verbatim", "chunks", "CONCISE"} {
		if !ValidRetainExtractionMode(mode) {
			t.Fatalf("%q must be accepted", mode)
		}
	}
	for _, mode := range []string{"", "concise-ish", "structured"} {
		if ValidRetainExtractionMode(mode) {
			t.Fatalf("%q must be refused", mode)
		}
	}
	if got := FromEnv(envMap(nil)).RetainExtractionMode; got != DefaultRetainExtractionMode {
		t.Fatalf("default mode = %q", got)
	}
	if got := FromEnv(envMap(map[string]string{EnvRetainExtractionMode: "verbose"})).RetainExtractionMode; got != "verbose" {
		t.Fatalf("resolved mode = %q", got)
	}
}
