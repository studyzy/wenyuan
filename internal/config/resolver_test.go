package config

import "testing"

// The bank's override wins a key the tenant and the process also define, per
// key: a key only the process sets survives, and the bank only shadows the keys
// it actually names.
func TestResolveLayersBankWinsPerKey(t *testing.T) {
	bank := map[string]any{"retain_chunk_size": 100}
	tenant := map[string]any{"retain_chunk_size": 200, "enable_text_search": false}
	process := map[string]any{"retain_chunk_size": 300, "enable_reranking": false}

	got := ResolveLayers(bank, tenant, process)

	if got["retain_chunk_size"] != 100 {
		t.Fatalf("retain_chunk_size = %v, want the bank's 100", got["retain_chunk_size"])
	}
	if got["enable_text_search"] != false {
		t.Fatalf("enable_text_search = %v, want the tenant's false", got["enable_text_search"])
	}
	if got["enable_reranking"] != false {
		t.Fatalf("enable_reranking = %v, want the process's false", got["enable_reranking"])
	}
}

// The tenant wins a key the process defines and the bank does not, so a tenant
// override layers over the process without touching the bank.
func TestResolveLayersTenantOverProcess(t *testing.T) {
	bank := map[string]any{}
	tenant := map[string]any{"recall_budget": "high"}
	process := map[string]any{"recall_budget": "low", "retain_mission": "process mission"}

	got := ResolveLayers(bank, tenant, process)

	if got["recall_budget"] != "high" {
		t.Fatalf("recall_budget = %v, want the tenant's high", got["recall_budget"])
	}
	if got["retain_mission"] != "process mission" {
		t.Fatalf("retain_mission = %v, want the process value untouched", got["retain_mission"])
	}
}

// A layer that does not name a key leaves it absent rather than defaulted, so
// the call site keeps applying its own shipped default.
func TestResolveLayersLeavesUndefinedKeysAbsent(t *testing.T) {
	got := ResolveLayers(nil, map[string]any{"enable_graph_retrieval": false}, nil)
	if _, ok := got["enable_text_search"]; ok {
		t.Fatalf("resolver invented a default: %v", got)
	}
	if got["enable_graph_retrieval"] != false {
		t.Fatalf("enable_graph_retrieval = %v", got["enable_graph_retrieval"])
	}
}

// A deployment with no tenant layer resolves to exactly the bank's own
// overrides, which is the behaviour that predates the layer.
func TestResolveLayersWithoutTenantIsBankOnly(t *testing.T) {
	bank := map[string]any{"retain_chunk_size": 42}
	got := ResolveLayers(bank, nil, nil)
	if len(got) != 1 || got["retain_chunk_size"] != 42 {
		t.Fatalf("resolved = %v, want exactly the bank's overrides", got)
	}
}

// Every layer absent yields an empty, non-nil map so a caller can index it
// without a nil check.
func TestResolveLayersAllAbsentIsEmpty(t *testing.T) {
	got := ResolveLayers(nil, nil, nil)
	if got == nil {
		t.Fatal("resolved must be non-nil")
	}
	if len(got) != 0 {
		t.Fatalf("resolved = %v, want empty", got)
	}
}
