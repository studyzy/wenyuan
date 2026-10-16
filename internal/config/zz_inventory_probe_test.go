package config

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
)

// TestDumpKeyInventory is a throwaway probe for the configuration-template work.
// It records every key the resolver reads in two passes — an empty environment,
// and one where every provider key says "openai" so a conditional branch (a
// provider chain slot, for example) reads what it would read in a real
// deployment — plus the resolved/refused decision each key's table carries.
// Nothing is asserted; the JSON it writes drives the template and the docs.
func TestDumpKeyInventory(t *testing.T) {
	names := map[string]string{}
	for _, k := range referenceRuntimeKeys {
		names[k.Name] = k.Default
	}
	for _, keys := range providerFamilyKeys {
		for _, key := range keys {
			names[key] = ""
		}
	}

	record := func(lookup func(string) (string, bool)) []string {
		seen := map[string]bool{}
		var order []string
		func() {
			defer func() { _ = recover() }()
			FromEnv(func(key string) (string, bool) {
				if key != "" && !seen[key] {
					seen[key] = true
					order = append(order, key)
				}
				return lookup(key)
			})
		}()
		sort.Strings(order)
		return order
	}

	empty := record(func(string) (string, bool) { return "", false })
	allNames := make([]string, 0, len(names))
	for name := range names {
		allNames = append(allNames, name)
	}
	sort.Strings(allNames)
	populated := record(func(key string) (string, bool) {
		if value, ok := names[key]; ok && value != "" {
			return value, true
		}
		if !strings.HasSuffix(key, "_PROVIDER") {
			return "", false
		}
		// Answer "openai" for the family keys the resolver declares, plus
		// exactly the first indexed member of each chain, so the indexed-
		// member branch is exercised while the member scan still terminates
		// at index 2. An open-ended answer here would feed the resolver an
		// infinitely long chain and the probe would never return.
		if _, ok := names[key]; ok {
			return "openai", true
		}
		if strings.HasSuffix(key, "LLM_1_PROVIDER") || strings.HasSuffix(key, "RERANKER_1_PROVIDER") {
			return "openai", true
		}
		return "", false
	})

	type refKey struct {
		Key            string `json:"key"`
		UpstreamValue  string `json:"upstream_default"`
		Ours           string `json:"ours,omitempty"`
		Resolved       bool   `json:"resolved"`
		ValueDependent bool   `json:"value_dependent,omitempty"`
		Reason         string `json:"reason,omitempty"`
		Divergent      string `json:"divergent,omitempty"`
		Delegated      string `json:"delegated,omitempty"`
	}
	base := FromEnv(func(string) (string, bool) { return "", false })
	runtime := make([]refKey, 0, len(referenceRuntimeKeys))
	for _, k := range referenceRuntimeKeys {
		row := refKey{Key: k.Name, UpstreamValue: k.Default, Resolved: k.Resolve != nil, ValueDependent: k.UnsupportedWhen != nil, Reason: k.Reason, Divergent: k.Divergent, Delegated: k.Delegated}
		if k.Resolve != nil {
			row.Ours = fmt.Sprint(k.Resolve(base))
		}
		runtime = append(runtime, row)
	}

	families := map[string]any{}
	for family, keys := range providerFamilyKeys {
		resolved, refused := []string{}, []string{}
		for _, key := range keys {
			if resolvedProviderKeys[key] {
				resolved = append(resolved, key)
			} else {
				refused = append(refused, key)
			}
		}
		families[family] = map[string]any{"resolved": resolved, "refused": refused}
	}

	out := map[string]any{
		"asked_empty":       empty,
		"asked_populated":   populated,
		"reference_default": runtime,
		"provider_families": families,
		"false_defaults":    falseDefaultProviderKeys,
		"consolidation":     ConsolidationKeys(),
		"scopes":            ScopeKeys(),
	}
	b, err := json.MarshalIndent(out, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("/tmp/wenyuan_inventory.json", b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("asked empty=%d populated=%d runtime=%d", len(empty), len(populated), len(runtime))
}
