package app

// Audit trail integration oracles.
//
// These cases assert the served application records its mutating and core
// operations on the audit trail, honours the deployment switch and the
// per-bank override, and leaves a record when an operation fails.

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"testing"

	"github.com/studyzy/wenyuan/internal/audit"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// auditApp composes the served application over an in-memory store with the
// bank configuration API available and the audit switch set as given.
func auditApp(t *testing.T, enabled bool) *App {
	t.Helper()
	cfg := testConfig()
	cfg.EnableBankConfigAPI = true
	cfg.AuditLogEnabled = enabled
	// Port 0 lets the listen probe take a free port rather than colliding with
	// a server already bound to the default.
	cfg.Port = 0
	a, err := New(context.Background(), cfg, Dependencies{Store: storage.NewMemoryStore("public")})
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// auditRows reads one bank's audit trail through the served route, applying the
// given query string.
func auditRows(t *testing.T, a *App, bank, query string) []map[string]any {
	t.Helper()
	rec := exchange(a, http.MethodGet, "/v1/default/banks/"+bank+"/audit-logs?"+query, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("audit logs: status %d, body = %s", rec.Code, rec.Body.String())
	}
	raw, _ := decodeJSON(t, rec)["items"].([]any)
	rows := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if row, ok := item.(map[string]any); ok {
			rows = append(rows, row)
		}
	}
	return rows
}

// actionsByTransport indexes a page of rows by action name.
func actionsByTransport(rows []map[string]any) map[string]string {
	indexed := map[string]string{}
	for _, row := range rows {
		action, _ := row["action"].(string)
		transport, _ := row["transport"].(string)
		indexed[action] = transport
	}
	return indexed
}

// Every HTTP route's action is part of the deployment-wide enumeration, so the
// roster and the route table cannot drift apart.
func TestHTTPAuditActionsAreEnumerated(t *testing.T) {
	known := map[string]bool{}
	for _, action := range audit.Actions {
		known[action] = true
	}
	for key, route := range httpAuditRoutes {
		if !known[route.action] {
			t.Errorf("route %q maps to unlisted action %q", key, route.action)
		}
	}
}

// A retain, a recall, a reflect and a bank create/update each record one row
// with the reference's action name and the http transport.
func TestAuditRecordsCoreOperations(t *testing.T) {
	a := auditApp(t, true)
	const bankPath = "/v1/default/banks/audit-core"

	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Audit"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/memories", `{"items":[{"content":"the deployment window is Thursday"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("retain: status %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/memories/recall", `{"query":"deployment window"}`); rec.Code != http.StatusOK {
		t.Fatalf("recall: status %d, body = %s", rec.Code, rec.Body.String())
	}
	// This deployment has no LLM provider, so reflect is refused; the route is
	// still audited, which is what this case is about.
	if rec := exchange(a, http.MethodPost, bankPath+"/reflect", `{"query":"when is the deployment window?"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("reflect: status %d, body = %s", rec.Code, rec.Body.String())
	}
	if rec := exchange(a, http.MethodPatch, bankPath, `{"name":"Audit Renamed"}`); rec.Code != http.StatusOK {
		t.Fatalf("update bank: status %d, body = %s", rec.Code, rec.Body.String())
	}

	rows := auditRows(t, a, "audit-core", "limit=50")
	indexed := actionsByTransport(rows)
	for _, action := range []string{"create_bank", "retain", "recall", "reflect", "update_bank"} {
		if indexed[action] != "http" {
			t.Errorf("action %q: transport = %q (want http); rows = %v", action, indexed[action], rows)
		}
	}

	// The record carries the request and response payloads the reference
	// records.
	for _, row := range rows {
		if row["action"] == "recall" {
			if request, _ := row["request"].(map[string]any); request["query"] != "deployment window" {
				t.Errorf("recall request not recorded: %v", row["request"])
			}
			if _, ok := row["response"].(map[string]any); !ok {
				t.Errorf("recall response not recorded: %v", row["response"])
			}
		}
	}
}

// The action and transport filters narrow the served listing.
func TestAuditListingFilters(t *testing.T) {
	a := auditApp(t, true)
	const bankPath = "/v1/default/banks/audit-filter"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Filter"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d", rec.Code)
	}
	if rec := exchange(a, http.MethodPost, bankPath+"/memories", `{"items":[{"content":"hello"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("retain: status %d", rec.Code)
	}

	onlyRetain := auditRows(t, a, "audit-filter", "action=retain&limit=50")
	if len(onlyRetain) == 0 {
		t.Fatalf("action filter returned no rows")
	}
	for _, row := range onlyRetain {
		if row["action"] != "retain" {
			t.Errorf("action filter leaked %v", row["action"])
		}
	}
	onlyMCP := auditRows(t, a, "audit-filter", "transport=mcp&limit=50")
	if len(onlyMCP) != 0 {
		t.Errorf("transport filter returned %d rows, want 0", len(onlyMCP))
	}
}

// With the deployment switch off, no operation records a row.
func TestAuditDisabledProducesNoRows(t *testing.T) {
	a := auditApp(t, false)
	if rec := exchange(a, http.MethodPut, "/v1/default/banks/audit-off", `{"name":"Off"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d", rec.Code)
	}
	if rec := exchange(a, http.MethodPost, "/v1/default/banks/audit-off/memories", `{"items":[{"content":"hello"}]}`); rec.Code != http.StatusOK {
		t.Fatalf("retain: status %d", rec.Code)
	}
	if rows := auditRows(t, a, "audit-off", "limit=50"); len(rows) != 0 {
		t.Fatalf("disabled switch recorded %d rows: %v", len(rows), rows)
	}
}

// A per-bank override turns auditing on for one bank while the deployment
// default stays off, so only that bank records rows.
func TestAuditBankOverrideFlipsOneBank(t *testing.T) {
	a := auditApp(t, false)
	for _, bank := range []string{"override-on", "override-off"} {
		if rec := exchange(a, http.MethodPut, "/v1/default/banks/"+bank, `{"name":"B"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
			t.Fatalf("create %s: status %d", bank, rec.Code)
		}
	}
	// Turn auditing on for one bank only, through the bank configuration API.
	if rec := exchange(a, http.MethodPatch, "/v1/default/banks/override-on/config",
		`{"updates":{"audit_log_enabled":true}}`); rec.Code != http.StatusOK {
		t.Fatalf("enable override: status %d, body = %s", rec.Code, rec.Body.String())
	}

	for _, bank := range []string{"override-on", "override-off"} {
		if rec := exchange(a, http.MethodPost, "/v1/default/banks/"+bank+"/memories", `{"items":[{"content":"hello"}]}`); rec.Code != http.StatusOK {
			t.Fatalf("retain %s: status %d", bank, rec.Code)
		}
	}

	on := auditRows(t, a, "override-on", "limit=50")
	if len(on) == 0 {
		t.Fatalf("overridden bank recorded no rows")
	}
	for _, row := range on {
		if row["action"] != "retain" {
			t.Errorf("overridden bank recorded %v", row["action"])
		}
	}
	if off := auditRows(t, a, "override-off", "limit=50"); len(off) != 0 {
		t.Fatalf("default-off bank recorded %d rows: %v", len(off), off)
	}
}

// A failed operation still records a row, carrying the failure text.
func TestAuditRecordsFailingOperation(t *testing.T) {
	a := auditApp(t, true)
	const bankPath = "/v1/default/banks/audit-fail"
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Fail"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d", rec.Code)
	}
	// Cancelling an operation that does not exist fails with 404.
	fail := exchange(a, http.MethodDelete, bankPath+"/operations/00000000-0000-4000-8000-000000000000", "")
	if fail.Code != http.StatusNotFound {
		t.Fatalf("cancel unknown operation: status %d, body = %s", fail.Code, fail.Body.String())
	}

	rows := auditRows(t, a, "audit-fail", "action=cancel_operation&limit=50")
	if len(rows) != 1 {
		t.Fatalf("expected one failure row, got %d: %v", len(rows), rows)
	}
	if rows[0]["transport"] != "http" {
		t.Errorf("failure transport = %v", rows[0]["transport"])
	}
	metadata, _ := rows[0]["metadata"].(map[string]any)
	if metadata == nil {
		t.Fatalf("failure row carries no metadata: %v", rows[0])
	}
	if text, _ := metadata["error"].(string); text == "" {
		t.Fatalf("failure text not recorded: %v", metadata)
	}
}

// auditedBodyCase names one audited route that takes a request body: the action
// it records, a request that exercises its model, the exact set of declared
// keys the reference's model dump carries, and optional checks of that dump's
// defaults. Every sample body sends an "unknown_field" the dump must drop.
type auditedBodyCase struct {
	action string
	method string
	path   string
	body   string
	keys   []string
	check  func(*testing.T, map[string]any)
}

// auditedBodyCases enumerates the audited routes that take a request body. The
// declared key sets and defaults come from the reference's request models; its
// audit logging is off, so this could not be probed live.
func auditedBodyCases(bank string) []auditedBodyCase {
	bankCaseKeys := []string{
		"name", "disposition", "disposition_skepticism", "disposition_literalism", "disposition_empathy",
		"mission", "background", "reflect_mission", "retain_mission", "retain_extraction_mode",
		"retain_custom_instructions", "retain_chunk_size", "retain_structured_chunk_size",
		"retain_max_attachments_per_chunk", "enable_observations", "observations_mission",
		"enable_text_search", "enable_temporal_retrieval", "enable_graph_retrieval", "enable_reranking",
	}
	return []auditedBodyCase{
		{
			action: "retain", method: http.MethodPost, path: bank + "/memories",
			body: `{"items":[{"content":"x"}],"unknown_field":1}`,
			keys: []string{"items", "async_", "document_tags", "operation_id"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["async_"] != false {
					t.Errorf("retain async_ = %v, want false", request["async_"])
				}
				if request["document_tags"] != nil || request["operation_id"] != nil {
					t.Errorf("retain nullable defaults not null: %v", request)
				}
				items, _ := request["items"].([]any)
				if len(items) != 1 {
					t.Fatalf("retain items = %v", request["items"])
				}
				item, _ := items[0].(map[string]any)
				if item["resolve_entities"] != true {
					t.Errorf("memory item resolve_entities = %v, want true", item["resolve_entities"])
				}
				if item["context"] != nil || item["document_id"] != nil {
					t.Errorf("memory item nullable defaults not null: %v", item)
				}
			},
		},
		{
			action: "recall", method: http.MethodPost, path: bank + "/memories/recall",
			body: `{"query":"q","unknown_field":1}`,
			keys: []string{
				"query", "types", "prefer_observations", "budget", "max_tokens", "trace",
				"query_timestamp", "include", "tags", "tags_match", "tag_groups", "min_scores", "temporal_window",
			},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["budget"] != "mid" || request["max_tokens"] != float64(4096) || request["tags_match"] != "any" {
					t.Errorf("recall scalar defaults = %v", request)
				}
				if request["types"] != nil || request["tag_groups"] != nil {
					t.Errorf("recall nullable defaults not null: %v", request)
				}
				include, _ := request["include"].(map[string]any)
				entities, _ := include["entities"].(map[string]any)
				if entities["max_tokens"] != float64(500) {
					t.Errorf("recall include.entities = %v", include["entities"])
				}
				if include["chunks"] != nil || include["source_facts"] != nil {
					t.Errorf("recall include sections not null: %v", include)
				}
			},
		},
		{
			action: "reflect", method: http.MethodPost, path: bank + "/reflect",
			body: `{"query":"q","unknown_field":1}`,
			keys: []string{
				"reflect_search_observations_max_tokens", "reflect_search_observations_include_entities",
				"query", "budget", "context", "max_tokens", "include", "response_schema", "tags",
				"tags_match", "tag_groups", "apply_all_directives", "fact_types", "exclude_mental_models",
				"exclude_mental_model_ids",
			},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["budget"] != "low" || request["max_tokens"] != float64(4096) {
					t.Errorf("reflect scalar defaults = %v", request)
				}
				include, _ := request["include"].(map[string]any)
				if include["facts"] != nil || include["tool_calls"] != nil {
					t.Errorf("reflect include = %v", include)
				}
			},
		},
		{
			action: "update_memory", method: http.MethodPatch, path: bank + "/memories/no-such-memory",
			body: `{"text":"x","unknown_field":1}`,
			keys: []string{"text", "context", "occurred_start", "occurred_end", "fact_type", "entities", "resolve_entities", "state", "reason"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["resolve_entities"] != true {
					t.Errorf("update_memory resolve_entities = %v, want true", request["resolve_entities"])
				}
				if request["context"] != nil || request["entities"] != nil {
					t.Errorf("update_memory nullable defaults not null: %v", request)
				}
			},
		},
		{
			action: "create_bank", method: http.MethodPut, path: bank,
			body: `{"mission":"m","unknown_field":1}`,
			keys: bankCaseKeys,
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["mission"] != "m" || request["name"] != nil || request["enable_reranking"] != nil {
					t.Errorf("create_bank = %v", request)
				}
			},
		},
		{
			action: "update_bank", method: http.MethodPatch, path: bank,
			body: `{"name":"n","unknown_field":1}`,
			keys: bankCaseKeys,
		},
		{
			action: "create_bank_alias", method: http.MethodPost, path: bank + "/aliases",
			body: `{"alias":"a","unknown_field":1}`,
			keys: []string{"alias", "primary"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["primary"] != false {
					t.Errorf("create_bank_alias primary = %v, want false", request["primary"])
				}
			},
		},
		{
			action: "set_bank_alias_primary", method: http.MethodPatch, path: bank + "/aliases/a",
			body: `{"primary":true,"unknown_field":1}`,
			keys: []string{"primary"},
		},
		{
			action: "update_bank_config", method: http.MethodPatch, path: bank + "/config",
			body: `{"updates":{"enable_text_search":true},"unknown_field":1}`,
			keys: []string{"updates"},
		},
		{
			action: "create_mental_model", method: http.MethodPost, path: bank + "/mental-models",
			body: `{"name":"n","source_query":"s","unknown_field":1}`,
			keys: []string{"id", "name", "source_query", "tags", "max_tokens", "trigger"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["max_tokens"] != float64(2048) || request["id"] != nil {
					t.Errorf("create_mental_model = %v", request)
				}
				if tags, ok := request["tags"].([]any); !ok || len(tags) != 0 {
					t.Errorf("create_mental_model tags = %v, want []", request["tags"])
				}
				trigger, _ := request["trigger"].(map[string]any)
				if trigger["mode"] != "full" || trigger["keep_trace"] != false {
					t.Errorf("create_mental_model trigger = %v", trigger)
				}
			},
		},
		{
			action: "update_mental_model", method: http.MethodPatch, path: bank + "/mental-models/no-such",
			body: `{"name":"n","unknown_field":1}`,
			keys: []string{"name", "source_query", "max_tokens", "tags", "trigger"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["source_query"] != nil || request["trigger"] != nil {
					t.Errorf("update_mental_model nullable defaults not null: %v", request)
				}
			},
		},
		{
			action: "create_directive", method: http.MethodPost, path: bank + "/directives",
			body: `{"name":"n","content":"c","unknown_field":1}`,
			keys: []string{"name", "content", "priority", "is_active", "tags"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["is_active"] != true || request["priority"] != float64(0) {
					t.Errorf("create_directive = %v", request)
				}
				if tags, ok := request["tags"].([]any); !ok || len(tags) != 0 {
					t.Errorf("create_directive tags = %v, want []", request["tags"])
				}
			},
		},
		{
			action: "update_directive", method: http.MethodPatch, path: bank + "/directives/no-such",
			body: `{"content":"c","unknown_field":1}`,
			keys: []string{"name", "content", "priority", "is_active", "tags"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["name"] != nil || request["priority"] != nil || request["is_active"] != nil {
					t.Errorf("update_directive nullable defaults not null: %v", request)
				}
			},
		},
		{
			action: "update_document", method: http.MethodPatch, path: bank + "/documents/no-such",
			body: `{"tags":["a"],"unknown_field":1}`,
			keys: []string{"tags"},
		},
		{
			action: "consolidation", method: http.MethodPost, path: bank + "/consolidate",
			body: `{"observation_scopes":[["a"]],"unknown_field":1}`,
			keys: []string{"observation_scopes"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				scopes, _ := request["observation_scopes"].([]any)
				if len(scopes) != 1 {
					t.Errorf("consolidation observation_scopes = %v", request["observation_scopes"])
				}
			},
		},
		{
			action: "create_webhook", method: http.MethodPost, path: bank + "/webhooks",
			body: `{"url":"http://example.com","unknown_field":1}`,
			keys: []string{"url", "secret", "event_types", "enabled", "http_config"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["enabled"] != true || request["secret"] != nil {
					t.Errorf("create_webhook = %v", request)
				}
				events, _ := request["event_types"].([]any)
				if len(events) != 1 || events[0] != "consolidation.completed" {
					t.Errorf("create_webhook event_types = %v", request["event_types"])
				}
				config, _ := request["http_config"].(map[string]any)
				if config["method"] != "POST" || config["timeout_seconds"] != float64(30) {
					t.Errorf("create_webhook http_config = %v", config)
				}
				if headers, ok := config["headers"].(map[string]any); !ok || len(headers) != 0 {
					t.Errorf("create_webhook http_config.headers = %v, want {}", config["headers"])
				}
			},
		},
		{
			action: "update_webhook", method: http.MethodPatch, path: bank + "/webhooks/no-such",
			body: `{"url":"http://example.com","unknown_field":1}`,
			keys: []string{"url", "secret", "event_types", "enabled", "http_config"},
			check: func(t *testing.T, request map[string]any) {
				t.Helper()
				if request["secret"] != nil || request["event_types"] != nil || request["enabled"] != nil || request["http_config"] != nil {
					t.Errorf("update_webhook nullable defaults not null: %v", request)
				}
			},
		},
	}
}

// requestKeys returns the top-level keys of a recorded request object, sorted.
func requestKeys(request map[string]any) []string {
	keys := make([]string, 0, len(request))
	for key := range request {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// The audit trail's request object is the declared request model's dump of the
// captured body, not the bytes as sent: every declared key is present (a
// nullable one as null, a defaulted one carrying its default) and a key the
// model does not declare is dropped.
func TestAuditRecordsDeclaredRequestBody(t *testing.T) {
	a := auditApp(t, true)
	const bank = "audit-body"
	bankPath := "/v1/default/banks/" + bank
	if rec := exchange(a, http.MethodPut, bankPath, `{"name":"Audit Body"}`); rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create bank: status %d, body = %s", rec.Code, rec.Body.String())
	}

	for _, tc := range auditedBodyCases(bankPath) {
		exchange(a, tc.method, tc.path, tc.body)
		rows := auditRows(t, a, bank, "action="+tc.action+"&limit=50")
		if len(rows) == 0 {
			t.Errorf("%s (%s %s): no audit row", tc.action, tc.method, tc.path)
			continue
		}
		want := append([]string{}, tc.keys...)
		sort.Strings(want)
		var match map[string]any
		for _, row := range rows {
			request, ok := row["request"].(map[string]any)
			if !ok {
				continue
			}
			if _, dropped := request["unknown_field"]; dropped {
				t.Errorf("%s: unknown body key recorded: %v", tc.action, request)
			}
			if match == nil && slices.Equal(requestKeys(request), want) {
				match = request
			}
		}
		if match == nil {
			t.Errorf("%s: no recorded request with the declared keys %v; rows = %v", tc.action, want, rows)
			continue
		}
		if tc.check != nil {
			tc.check(t, match)
		}
	}
}

// Each audited body route's sample reproduces the reference's declared-model
// dump byte for byte. The expected strings are the reference's own
// model_dump(mode="json") output, so a field's presence, null default, or
// position here is the contract. Reference audit logging is off, so this could
// not be probed live.
func TestAuditBodySamplesMatchReference(t *testing.T) {
	cases := []struct {
		name   string
		sample httpx.BodySample
		body   string
		want   string
	}{
		{
			name: "retain", sample: sampleRetain,
			body: `{"items":[{"content":"hi"}]}`,
			want: `{"items":[{"content":"hi","timestamp":null,"context":null,"metadata":null,"document_id":null,"entities":null,"resolve_entities":true,"tags":null,"observation_scopes":null,"strategy":null,"update_mode":null}],"async_":false,"document_tags":null,"operation_id":null}`,
		},
		{
			name: "recall", sample: sampleRecall,
			body: `{"query":"q"}`,
			want: `{"query":"q","types":null,"prefer_observations":false,"budget":"mid","max_tokens":4096,"trace":false,"query_timestamp":null,"include":{"entities":{"max_tokens":500},"chunks":null,"source_facts":null},"tags":null,"tags_match":"any","tag_groups":null,"min_scores":null,"temporal_window":null}`,
		},
		{
			name: "reflect", sample: sampleReflect,
			body: `{"query":"q"}`,
			want: `{"reflect_search_observations_max_tokens":null,"reflect_search_observations_include_entities":null,"query":"q","budget":"low","context":null,"max_tokens":4096,"include":{"facts":null,"tool_calls":null},"response_schema":null,"tags":null,"tags_match":"any","tag_groups":null,"apply_all_directives":false,"fact_types":null,"exclude_mental_models":false,"exclude_mental_model_ids":null}`,
		},
		{
			name: "update_memory", sample: sampleUpdateMemory,
			body: `{"text":"x"}`,
			want: `{"text":"x","context":null,"occurred_start":null,"occurred_end":null,"fact_type":null,"entities":null,"resolve_entities":true,"state":null,"reason":null}`,
		},
		{
			name: "create_bank", sample: sampleCreateBank,
			body: `{}`,
			want: `{"name":null,"disposition":null,"disposition_skepticism":null,"disposition_literalism":null,"disposition_empathy":null,"mission":null,"background":null,"reflect_mission":null,"retain_mission":null,"retain_extraction_mode":null,"retain_custom_instructions":null,"retain_chunk_size":null,"retain_structured_chunk_size":null,"retain_max_attachments_per_chunk":null,"enable_observations":null,"observations_mission":null,"enable_text_search":null,"enable_temporal_retrieval":null,"enable_graph_retrieval":null,"enable_reranking":null}`,
		},
		{
			name: "create_bank_alias", sample: sampleCreateBankAlias,
			body: `{"alias":"a"}`,
			want: `{"alias":"a","primary":false}`,
		},
		{
			name: "set_bank_alias_primary", sample: sampleSetBankAliasPrimary,
			body: `{"primary":true}`,
			want: `{"primary":true}`,
		},
		{
			name: "update_bank_config", sample: sampleBankConfigUpdate,
			body: `{"updates":{"a":1}}`,
			want: `{"updates":{"a":1}}`,
		},
		{
			name: "create_mental_model", sample: sampleCreateMentalModel,
			body: `{"name":"n","source_query":"s"}`,
			want: `{"id":null,"name":"n","source_query":"s","tags":[],"max_tokens":2048,"trigger":{"budget":null,"mode":"full","refresh_after_consolidation":false,"refresh_cron":null,"min_refresh_interval_seconds":null,"fact_types":null,"exclude_mental_models":false,"exclude_mental_model_ids":null,"tags_match":null,"tag_groups":null,"include_chunks":null,"recall_max_tokens":null,"recall_chunks_max_tokens":null,"reflect_search_observations_max_tokens":null,"reflect_search_observations_include_entities":null,"response_schema":null,"keep_trace":false}}`,
		},
		{
			name: "update_mental_model", sample: sampleUpdateMentalModel,
			body: `{"name":"n"}`,
			want: `{"name":"n","source_query":null,"max_tokens":null,"tags":null,"trigger":null}`,
		},
		{
			name: "create_directive", sample: sampleCreateDirective,
			body: `{"name":"n","content":"c"}`,
			want: `{"name":"n","content":"c","priority":0,"is_active":true,"tags":[]}`,
		},
		{
			name: "update_document", sample: sampleUpdateDocument,
			body: `{"tags":["a"]}`,
			want: `{"tags":["a"]}`,
		},
		{
			name: "consolidation", sample: sampleConsolidation,
			body: `{"observation_scopes":[["a"]]}`,
			want: `{"observation_scopes":[["a"]]}`,
		},
		{
			name: "create_webhook", sample: sampleCreateWebhook,
			body: `{"url":"http://x"}`,
			want: `{"url":"http://x","secret":null,"event_types":["consolidation.completed"],"enabled":true,"http_config":{"method":"POST","timeout_seconds":30,"headers":{},"params":{}}}`,
		},
		{
			name: "update_webhook", sample: sampleUpdateWebhook,
			body: `{"url":"u"}`,
			want: `{"url":"u","secret":null,"event_types":null,"enabled":null,"http_config":null}`,
		},
	}
	for _, tc := range cases {
		model := tc.sample()
		if err := json.Unmarshal([]byte(tc.body), model); err != nil {
			t.Errorf("%s: decode: %v", tc.name, err)
			continue
		}
		got, err := json.Marshal(model)
		if err != nil {
			t.Errorf("%s: marshal: %v", tc.name, err)
			continue
		}
		if string(got) != tc.want {
			t.Errorf("%s dump:\n got  %s\n want %s", tc.name, got, tc.want)
		}
	}
}
