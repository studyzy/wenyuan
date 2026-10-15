package fileparse

import (
	"reflect"
	"testing"
)

// FromEnv resolves the chain, allowlist, batch limits and remote credentials.
func TestSettingsFromEnv(t *testing.T) {
	env := map[string]string{
		EnvFileParser:                   "iris, markitdown",
		EnvFileParserAllowlist:          "markitdown",
		EnvFileParserIrisToken:          "tok",
		EnvFileParserIrisOrgID:          "org",
		EnvFileParserLlamaParseAPIKey:   "llx-key",
		EnvFileConversionMaxBatchSize:   "3",
		EnvFileConversionMaxBatchSizeMB: "2",
		EnvFileDeleteAfterRetain:        "false",
	}
	settings := FromEnv(func(key string) string { return env[key] })
	if !reflect.DeepEqual(settings.Chain, []string{"iris", "markitdown"}) {
		t.Fatalf("chain = %v", settings.Chain)
	}
	if !reflect.DeepEqual(settings.Allowlist, []string{"markitdown"}) {
		t.Fatalf("allowlist = %v", settings.Allowlist)
	}
	if settings.MaxBatchCount != 3 {
		t.Fatalf("batch count = %d", settings.MaxBatchCount)
	}
	if settings.MaxBatchBytes != 2<<20 {
		t.Fatalf("batch bytes = %d, want 2MiB", settings.MaxBatchBytes)
	}
	if settings.DeleteAfterRetain {
		t.Fatal("delete_after_retain should be false")
	}
	// Every remote parser with credentials is registered alongside the local one.
	registry := settings.Registry()
	names := registry.List()
	for _, want := range []string{"markitdown", "iris", "llama_parse"} {
		if !containsString(names, want) {
			t.Fatalf("registry %v missing %s", names, want)
		}
	}
}

// The defaults apply when nothing is configured: the local chain, the reference
// batch limits, and delete-after-retain on.
func TestSettingsDefaults(t *testing.T) {
	settings := FromEnv(func(string) string { return "" })
	if !reflect.DeepEqual(settings.Chain, []string{"markitdown"}) {
		t.Fatalf("default chain = %v", settings.Chain)
	}
	if settings.Allowlist != nil {
		t.Fatalf("default allowlist = %v, want nil (all parsers)", settings.Allowlist)
	}
	if settings.MaxBatchCount != 10 || settings.MaxBatchBytes != 100<<20 {
		t.Fatalf("default batch limits = %d / %d", settings.MaxBatchCount, settings.MaxBatchBytes)
	}
	if !settings.DeleteAfterRetain {
		t.Fatal("delete_after_retain defaults to true")
	}
	if got := settings.Registry().List(); !reflect.DeepEqual(got, []string{"markitdown"}) {
		t.Fatalf("default registry = %v", got)
	}
}

func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}
