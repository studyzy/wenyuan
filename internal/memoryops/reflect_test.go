package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// ts-0001 / beh-0001: a reflection with a non-blank query returns 200 with the
// required answer string.
func TestReflectSuccessReturnsAnswer(t *testing.T) {
	mux, store := reflectServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "the synthesized basis", Type: "world"})
	code, body := reflectBody(t, mux, "b1", `{"query":"synthesized"}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d, body = %v", code, body)
	}
	text, _ := body["text"].(string)
	if strings.TrimSpace(text) == "" {
		t.Fatalf("answer text must be non-empty: %v", body)
	}
	if _, ok := body["based_on"]; ok {
		t.Fatalf("cited evidence must be omitted without the facts gate: %v", body)
	}
}

// ts-0002 / beh-0002: the facts gate includes the cited-evidence object with the
// three collections; its absence omits the object.
func TestReflectFactsGate(t *testing.T) {
	mux, store := reflectServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "cited fact", Type: "world"})
	code, body := reflectBody(t, mux, "b1", `{"query":"fact","include":{"facts":{}}}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	based, ok := body["based_on"].(map[string]any)
	if !ok {
		t.Fatalf("based_on must be an object: %v", body)
	}
	for _, key := range []string{"memories", "mental_models", "directives"} {
		if _, ok := based[key].([]any); !ok {
			t.Fatalf("based_on.%s must be an array: %v", key, based)
		}
	}
	memories := based["memories"].([]any)
	if len(memories) == 0 {
		t.Fatal("cited memories must not be empty when a fact matched")
	}
}

// beh-0002 / neg-0004: the cited-evidence object is never a bare list.
func TestReflectFactsGateIsObjectNotList(t *testing.T) {
	mux, store := reflectServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "cited fact", Type: "world"})
	rec := post(t, mux, "/v1/default/banks/b1/reflect", `{"query":"fact","include":{"facts":{}}}`)
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw["based_on"]) == 0 || raw["based_on"][0] != '{' {
		t.Fatalf("based_on must serialize as an object: %s", rec.Body.String())
	}
}

// ts-0003 / beh-0003: the trace gate includes the trace; tool entries carry
// their output only when the output flag is not false.
func TestReflectTraceGateOutputFlag(t *testing.T) {
	cases := []struct {
		name       string
		body       string
		wantOutput bool
	}{
		{"output absent carries payload", `{"query":"q","include":{"tool_calls":{}}}`, true},
		{"output true carries payload", `{"query":"q","include":{"tool_calls":{"output":true}}}`, true},
		{"output false strips payload", `{"query":"q","include":{"tool_calls":{"output":false}}}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mux, store := reflectServer(t)
			store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "q fact", Type: "world"})
			code, body := reflectBody(t, mux, "b1", tc.body)
			if code != http.StatusOK {
				t.Fatalf("status = %d", code)
			}
			trace, ok := body["trace"].(map[string]any)
			if !ok {
				t.Fatalf("trace must be present: %v", body)
			}
			if _, ok := trace["tool_calls"].([]any); !ok {
				t.Fatalf("trace.tool_calls must be an array: %v", trace)
			}
			if _, ok := trace["llm_calls"].([]any); !ok {
				t.Fatalf("trace.llm_calls must be an array: %v", trace)
			}
			calls := trace["tool_calls"].([]any)
			if len(calls) == 0 {
				t.Fatal("a non-blank query with facts must record at least one tool call")
			}
			first := calls[0].(map[string]any)
			_, hasOutput := first["output"]
			if hasOutput != tc.wantOutput {
				t.Fatalf("output presence = %v, want %v: %v", hasOutput, tc.wantOutput, first)
			}
			for _, key := range []string{"tool", "input", "duration_ms", "iteration"} {
				if _, ok := first[key]; !ok {
					t.Fatalf("tool entry missing %q: %v", key, first)
				}
			}
			llm := trace["llm_calls"].([]any)
			if len(llm) == 0 {
				t.Fatal("model calls must be recorded")
			}
			for _, key := range []string{"scope", "duration_ms"} {
				if _, ok := llm[0].(map[string]any)[key]; !ok {
					t.Fatalf("model call missing %q: %v", key, llm[0])
				}
			}
		})
	}
}

// beh-0003: without the trace gate the trace is omitted.
func TestReflectTraceOmittedWithoutGate(t *testing.T) {
	mux, _ := reflectServer(t)
	_, body := reflectBody(t, mux, "b1", `{"query":"q"}`)
	if _, ok := body["trace"]; ok {
		t.Fatalf("trace must be omitted without the gate: %v", body)
	}
}

// ts-0004 / beh-0004 / out-0003: a structured-output schema yields a parsed
// object on success and nothing on nothing-found; an invalid schema is
// unprocessable.
func TestReflectStructuredOutput(t *testing.T) {
	schema := `{"type":"object","required":["answer"],"properties":{"answer":{"type":"string"}}}`
	mux, store := reflectServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "m1", Text: "q", Type: "world"})
	code, body := reflectBody(t, mux, "b1", `{"query":"q","response_schema":`+schema+`}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if _, hasOut := body["structured_output"]; hasOut {
		t.Fatalf("nothing-found must omit the structured output: %v", body)
	}
	if _, hasErr := body["structured_output_error"]; hasErr {
		t.Fatalf("nothing-found must omit the error: %v", body)
	}

	h := NewHandlers(storage.NewMemoryStore("public"), 5, 30000)
	h.ReflectSchemaValidator = func(map[string]any) error { return errSentinel }
	rec := post(t, bindHandlers(t, h), "/v1/default/banks/b1/reflect", `{"query":"q","response_schema":`+schema+`}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid schema must be unprocessable, got %d", rec.Code)
	}
}

// ts-0005 / beh-0005 / ec-0002: context is appended to the query; a blank query
// with non-blank context is accepted, while a blank query with no context is
// unprocessable before any model work.
func TestReflectContextConcatenationAndBlankGuard(t *testing.T) {
	mux, _ := reflectServer(t)
	code, body := reflectBody(t, mux, "b1", `{"context":"only context"}`)
	if code != http.StatusOK {
		t.Fatalf("context-only must be accepted: %d %v", code, body)
	}
	if _, ok := body["text"].(string); !ok {
		t.Fatalf("answer missing: %v", body)
	}
	for _, blank := range []string{`{"query":""}`, `{"query":"   "}`, `{}`} {
		code, _ := reflectBody(t, mux, "b1", blank)
		if code != http.StatusUnprocessableEntity {
			t.Fatalf("blank query %q must be 422, got %d", blank, code)
		}
	}
}

// ts-0006 / beh-0007: an empty fact-type list is unprocessable; an omitted
// filter is accepted.
func TestReflectEmptyFactTypes(t *testing.T) {
	mux, _ := reflectServer(t)
	if code, _ := reflectBody(t, mux, "b1", `{"query":"q","fact_types":[]}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("empty fact_types must be 422, got %d", code)
	}
	if code, _ := reflectBody(t, mux, "b1", `{"query":"q"}`); code != http.StatusOK {
		t.Fatalf("omitted fact_types must be accepted, got %d", code)
	}
}

// ts-0007 / beh-0008: both tag scope and tag groups is unprocessable; each
// alone is accepted.
func TestReflectTagsAndTagGroupsMutualExclusion(t *testing.T) {
	mux, _ := reflectServer(t)
	both := `{"query":"q","tags":["a"],"tag_groups":{"tag":"b"}}`
	if code, _ := reflectBody(t, mux, "b1", both); code != http.StatusUnprocessableEntity {
		t.Fatalf("tags plus tag_groups must be 422, got %d", code)
	}
	if code, _ := reflectBody(t, mux, "b1", `{"query":"q","tags":["a"]}`); code != http.StatusOK {
		t.Fatalf("tags alone must be accepted, got %d", code)
	}
	if code, _ := reflectBody(t, mux, "b1", `{"query":"q","tag_groups":{"tag":"b"}}`); code != http.StatusOK {
		t.Fatalf("tag_groups alone must be accepted, got %d", code)
	}
}

// ts-0008 / beh-0009 / ec-0001: a bank with no matching memories still answers,
// and the cited-evidence object carries three empty arrays.
func TestReflectNoMatchesStillAnswers(t *testing.T) {
	mux, _ := reflectServer(t)
	code, body := reflectBody(t, mux, "b1", `{"query":"nothing","include":{"facts":{}}}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if text, _ := body["text"].(string); strings.TrimSpace(text) == "" {
		t.Fatalf("answer must be non-empty: %v", body)
	}
	based, ok := body["based_on"].(map[string]any)
	if !ok {
		t.Fatalf("based_on must be present: %v", body)
	}
	for _, key := range []string{"memories", "mental_models", "directives"} {
		arr, ok := based[key].([]any)
		if !ok || len(arr) != 0 {
			t.Fatalf("based_on.%s must be an empty array: %v", key, based)
		}
	}
}

// ts-0009 / beh-0010 / err-0002: a no-answer run is a 500 with the underlying
// message, never a placeholder success.
func TestReflectNoAnswerIsInternalError(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(noAnswerStore{Store: store}, 5, 30000)
	rec := post(t, bindHandlers(t, h), "/v1/default/banks/b1/reflect", `{"query":"q"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), storage.ErrReflectNoAnswer.Error()) {
		t.Fatalf("underlying message must be present: %s", rec.Body.String())
	}
}

// beh-0011: a tool-driving failure is a 500 with the message; a shed retrieval
// is a 503 with the retry header.
func TestReflectToolDrivingAndShed(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(failingStore{Store: store, err: storage.ErrReflectToolDriving}, 5, 30000)
	if rec := post(t, bindHandlers(t, h), "/v1/default/banks/b1/reflect", `{"query":"q"}`); rec.Code != http.StatusInternalServerError {
		t.Fatalf("tool-driving failure = %d, want 500", rec.Code)
	}
	h2 := NewHandlers(failingStore{Store: store, err: storage.ErrReadShed}, 5, 30000)
	rec := post(t, bindHandlers(t, h2), "/v1/default/banks/b1/reflect", `{"query":"q"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("shed retrieval = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatal("shed retrieval must carry Retry-After")
	}
}

// ts-0010 / beh-0012 / err-0003: a wall-clock timeout is a 504 with a message.
func TestReflectTimeoutFallback(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(failingStore{Store: store, err: storage.ErrReflectTimeout}, 5, 30000)
	rec := post(t, bindHandlers(t, h), "/v1/default/banks/b1/reflect", `{"query":"q"}`)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	var envelope map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["detail"] == "" {
		t.Fatal("timeout must carry a message")
	}
}

// ts-0011 / beh-0013 / err-0005: an unavailable model is a 400; an unexpected
// failure is a 500 with a generic detail.
func TestReflectModelUnavailableAndUnexpected(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	unavail := NewHandlers(failingStore{Store: store, err: storage.ErrReflectModelUnavailable}, 5, 30000)
	if rec := post(t, bindHandlers(t, unavail), "/v1/default/banks/b1/reflect", `{"query":"q"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unavailable model = %d, want 400", rec.Code)
	}
	unexpected := NewHandlers(failingStore{Store: store, err: errSentinel}, 5, 30000)
	rec := post(t, bindHandlers(t, unexpected), "/v1/default/banks/b1/reflect", `{"query":"q"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("unexpected = %d, want 500", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "internal server error") {
		t.Fatalf("unexpected failure must be generic: %s", rec.Body.String())
	}
}

// ts-0012 / beh-0025: a cited fact carries the read-path field names and the
// same per-fact attachment references.
func TestReflectCitedFactFieldsAndAttachments(t *testing.T) {
	mux, store := reflectServer(t)
	occurred := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	mentioned := time.Date(2026, 2, 3, 4, 5, 6, 0, time.UTC)
	store.SeedMemory("b1", storage.RecallMatch{
		ID: "fact-1", Text: "cited", Type: "world", Context: "ctx",
		OccurredStart: &occurred, OccurredEnd: &occurred, MentionedAt: &mentioned,
		ChunkID: "chunk-1", DocumentID: "doc-1", Metadata: map[string]any{"k": "v"},
		Attachments: []storage.RecallAttachment{{ID: "att-1", Kind: "image", URI: "file://a"}},
	})
	code, body := reflectBody(t, mux, "b1", `{"query":"cited","include":{"facts":{}}}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	fact := body["based_on"].(map[string]any)["memories"].([]any)[0].(map[string]any)
	for _, key := range []string{"id", "text", "type", "context", "occurred_start", "occurred_end", "mentioned_at", "document_id", "chunk_id", "metadata", "attachments"} {
		if _, ok := fact[key]; !ok {
			t.Fatalf("cited fact missing %q: %v", key, fact)
		}
	}
	att := fact["attachments"].([]any)[0].(map[string]any)
	if att["id"] != "att-1" || att["kind"] != "image" || att["uri"] != "file://a" {
		t.Fatalf("attachment must match the read path: %v", att)
	}
}

// ts-0013 / beh-0023: cited directives expose identifier, name and content;
// usage exposes the five token counts.
func TestReflectCitedDirectivesAndUsage(t *testing.T) {
	mux, store := reflectServer(t)
	store.SeedDirective("b1", storage.ReflectDirective{ID: "d1", Name: "Be concise", Content: "keep it short", Active: true})
	code, body := reflectBody(t, mux, "b1", `{"query":"q","include":{"facts":{}}}`)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	dirs := body["based_on"].(map[string]any)["directives"].([]any)
	if len(dirs) != 1 {
		t.Fatalf("expected one applied directive: %v", dirs)
	}
	dir := dirs[0].(map[string]any)
	for _, key := range []string{"id", "name", "content"} {
		if _, ok := dir[key]; !ok {
			t.Fatalf("directive missing %q: %v", key, dir)
		}
	}
	usage := body["usage"].(map[string]any)
	for _, key := range []string{"input_tokens", "output_tokens", "total_tokens", "cached_tokens", "thoughts_tokens"} {
		if _, ok := usage[key]; !ok {
			t.Fatalf("usage missing %q: %v", key, usage)
		}
	}
}

// ts-0014 / beh-0001: each effort level is accepted.
func TestReflectBudgetLevelsAccepted(t *testing.T) {
	for _, level := range []string{"low", "mid", "high"} {
		mux, _ := reflectServer(t)
		code, _ := reflectBody(t, mux, "b1", `{"query":"q","budget":"`+level+`"}`)
		if code != http.StatusOK {
			t.Fatalf("budget %q must be accepted, got %d", level, code)
		}
	}
	mux, _ := reflectServer(t)
	if code, _ := reflectBody(t, mux, "b1", `{"query":"q","budget":"extreme"}`); code != http.StatusUnprocessableEntity {
		t.Fatalf("invalid budget must be 422, got %d", code)
	}
}

// ts-0015 / beh-0026: the two per-bank defaults apply unless the request
// overrides them.
func TestReflectPerBankDefaults(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	bankBudget := "high"
	maxTokens := 128
	includeEntities := true
	h := NewHandlers(store, 5, 30000)
	h.BankReflectDefaults = &BankReflectDefaults{
		Budget:                            bankBudget,
		SearchObservationsMaxTokens:       &maxTokens,
		SearchObservationsIncludeEntities: &includeEntities,
	}
	query := h.buildReflectQuery("b1", ReflectRequest{Query: "q"})
	if query.Budget != bankBudget {
		t.Fatalf("bank budget default = %q, want %q", query.Budget, bankBudget)
	}
	if query.SearchObservationsMaxTokens != maxTokens || !query.SearchObservationsIncludeEntities {
		t.Fatalf("bank observation defaults not applied: %+v", query)
	}
	override := 7
	overrideEntities := false
	explicit := "low"
	query = h.buildReflectQuery("b1", ReflectRequest{
		Query: "q", Budget: explicit,
		ReflectSearchObservationsMaxTokens:       &override,
		ReflectSearchObservationsIncludeEntities: &overrideEntities,
	})
	if query.Budget != explicit || query.SearchObservationsMaxTokens != override || query.SearchObservationsIncludeEntities {
		t.Fatalf("explicit request must override the bank default: %+v", query)
	}
}

// ts-0016 / beh-0024: an untagged directive always applies; a tagged directive
// applies only on a tag match; apply-all applies every active directive; a
// non-empty execution scope must match.
func TestReflectDirectiveSelection(t *testing.T) {
	seed := func(t *testing.T) *http.ServeMux {
		store := storage.NewMemoryStore("public")
		if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
			t.Fatal(err)
		}
		store.SeedDirective("b1", storage.ReflectDirective{ID: "untagged", Name: "u", Content: "c", Active: true})
		store.SeedDirective("b1", storage.ReflectDirective{ID: "tagged", Name: "t", Content: "c", Tags: []string{"x"}, Active: true})
		store.SeedDirective("b1", storage.ReflectDirective{ID: "scoped", Name: "s", Content: "c", Scope: "y", Active: true})
		store.SeedDirective("b1", storage.ReflectDirective{ID: "inactive", Name: "i", Content: "c", Active: false})
		h := NewHandlers(store, 5, 30000)
		h.ReflectCatalog = storeCatalog{store: store}
		return bindHandlers(t, h)
	}
	applied := func(t *testing.T, body string) []string {
		t.Helper()
		mux := seed(t)
		code, decoded := reflectBody(t, mux, "b1", body)
		if code != http.StatusOK {
			t.Fatalf("status = %d", code)
		}
		dirs := decoded["based_on"].(map[string]any)["directives"].([]any)
		out := make([]string, 0, len(dirs))
		for _, d := range dirs {
			out = append(out, d.(map[string]any)["id"].(string))
		}
		return out
	}

	withoutTag := applied(t, `{"query":"q","include":{"facts":{}}}`)
	if containsValue(withoutTag, "tagged") || containsValue(withoutTag, "scoped") || containsValue(withoutTag, "inactive") {
		t.Fatalf("only the untagged active directive must apply: %v", withoutTag)
	}
	withTag := applied(t, `{"query":"q","tags":["x"],"include":{"facts":{}}}`)
	if !containsValue(withTag, "tagged") {
		t.Fatalf("tagged directive must apply on a tag match: %v", withTag)
	}
	applyAll := applied(t, `{"query":"q","tags":["y"],"apply_all_directives":true,"include":{"facts":{}}}`)
	if len(applyAll) != 3 {
		t.Fatalf("apply-all must include every active directive whose scope matches: %v", applyAll)
	}
}

// ts-0017 / beh-0017 / neg-0002: a fabricated identifier is replaced by a
// neutral placeholder; real identifiers are kept.
func TestReflectIdentifierResolution(t *testing.T) {
	req := ReflectRequest{Query: "q"}
	outcome := storage.ReflectOutcome{
		Answer: "see id-1234567890 and also real-fact-id here",
		Facts:  []storage.ReflectCitation{{ID: "real-fact-id", Text: "t"}},
	}
	got := resolveAnswerIdentifiers(req, outcome)
	if strings.Contains(got, "id-1234567890") {
		t.Fatalf("fabricated identifier must be masked: %q", got)
	}
	if !strings.Contains(got, neutralPlaceholder) {
		t.Fatalf("placeholder must appear: %q", got)
	}
	if !strings.Contains(got, "real-fact-id") {
		t.Fatalf("real identifier must be kept: %q", got)
	}
}
