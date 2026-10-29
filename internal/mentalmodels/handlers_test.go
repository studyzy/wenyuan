package mentalmodels

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// newServer builds the family's route table over an in-memory store with one
// bank, translating path patterns into the path values the real router sets.
func newServer(t *testing.T) (*http.ServeMux, *storage.MemoryStore) {
	t.Helper()
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handlers := NewHandlers(store)
	// The dry run needs the model boundary in production; the unit test supplies
	// a stub that reports the upstream result shape.
	handlers.DryRun = func(_ context.Context, _, modelID string) (*DryRunResult, error) {
		return &DryRunResult{
			MentalModelID: modelID,
			Name:          "stub",
			RequestedMode: "full",
			EffectiveMode: "full",
			Outcome:       "content_written",
			WouldPersist:  true,
			Facts:         DryRunFactCounts{Retrieved: map[string]int{}, Used: map[string]int{}},
			BasedOn:       map[string][]any{},
			Trace:         DryRunTrace{ToolCalls: []DryRunToolCall{}, LLMCalls: []DryRunLLMCall{}, Warnings: []string{}},
			Warnings:      []string{},
		}, nil
	}
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "b1")
			if strings.Contains(pattern, "{mental_model_id}") {
				r.SetPathValue("mental_model_id", lastSegment(r.URL.Path))
			}
			handler(w, r)
		})
	})
	return mux, store
}

// lastSegment returns the final path segment, skipping a trailing action word.
func lastSegment(path string) string {
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		switch segments[i] {
		case "history", "refresh", "dry-run-refresh", "clear":
			continue
		default:
			return segments[i]
		}
	}
	return ""
}

func send(mux *http.ServeMux, method, path, body string) *httptest.ResponseRecorder {
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	return rec
}

// valItem is one structured 422 item with its list positions decoded as
// numbers, the way the reference emits them.
type valItem struct {
	Type string `json:"type"`
	Loc  []any  `json:"loc"`
	Msg  string `json:"msg"`
}

// decodeValidationFailure reads the structured 422 envelope.
func decodeValidationFailure(t *testing.T, rec *httptest.ResponseRecorder) []valItem {
	t.Helper()
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422 (body %s)", rec.Code, rec.Body.String())
	}
	var body struct {
		Detail []valItem `json:"detail"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Detail
}

// The full create → list → get → update → history → clear → delete lifecycle.
func TestMentalModelLifecycle(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"

	created := send(mux, http.MethodPost, base, `{"name":"goals","source_query":"what are the goals?","tags":["work"]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create: %d, body = %s", created.Code, created.Body.String())
	}
	var createdResp CreateResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createdResp); err != nil {
		t.Fatal(err)
	}
	if createdResp.MentalModelID == "" {
		t.Fatalf("create must return a mental_model_id: %s", created.Body.String())
	}
	// Read the created record back.
	got := send(mux, http.MethodGet, base+"/"+createdResp.MentalModelID, "")
	var model Model
	if err := json.Unmarshal(got.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if model.Name != "goals" {
		t.Fatalf("created model = %+v", model)
	}

	listed := send(mux, http.MethodGet, base, "")
	var list ListResponse
	if err := json.Unmarshal(listed.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("list = %+v", list)
	}

	// The route has no subtype filter (the reference lists by tags only), so a
	// subtype query parameter is simply not part of the contract: it changes
	// nothing and is reported as ignored.
	if rec := send(mux, http.MethodGet, base+"?subtype=bogus", ""); rec.Code != http.StatusOK {
		t.Fatalf("an undeclared parameter must not change the response: %d", rec.Code)
	}

	if rec := send(mux, http.MethodGet, base+"/"+model.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("get: %d", rec.Code)
	}

	// A model no refresh has written to yet still carries `content`, as the empty
	// string. The frontend's detail pane counts its characters, so a response that
	// omits the key crashes the pane instead of showing an empty one.
	fresh := send(mux, http.MethodPost, base, `{"name":"untouched","source_query":"nothing yet"}`)
	var freshResp CreateResponse
	if err := json.Unmarshal(fresh.Body.Bytes(), &freshResp); err != nil {
		t.Fatal(err)
	}
	detail := send(mux, http.MethodGet, base+"/"+freshResp.MentalModelID, "")
	if detail.Code != http.StatusOK {
		t.Fatalf("get fresh: %d", detail.Code)
	}
	var detailBody map[string]json.RawMessage
	if err := json.Unmarshal(detail.Body.Bytes(), &detailBody); err != nil {
		t.Fatal(err)
	}
	content, ok := detailBody["content"]
	if !ok {
		t.Fatalf("a model with no content must still report the key: %s", detail.Body.String())
	}
	if string(content) != `""` {
		t.Fatalf("content before a refresh = %s, want an empty string", content)
	}

	updated := send(mux, http.MethodPatch, base+"/"+model.ID, `{"name":"the current goals"}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update: %d, body = %s", updated.Code, updated.Body.String())
	}

	if rec := send(mux, http.MethodGet, base+"/"+model.ID+"/history", ""); rec.Code != http.StatusOK {
		t.Fatalf("history: %d", rec.Code)
	}
	if rec := send(mux, http.MethodPost, base+"/"+model.ID+"/clear", ""); rec.Code != http.StatusOK {
		t.Fatalf("clear: %d", rec.Code)
	}
	dry := send(mux, http.MethodPost, base+"/"+model.ID+"/dry-run-refresh", "")
	if dry.Code != http.StatusOK {
		t.Fatalf("dry-run refresh: %d", dry.Code)
	}
	var dryBody map[string]any
	if err := json.Unmarshal(dry.Body.Bytes(), &dryBody); err != nil {
		t.Fatal(err)
	}
	if dryBody["effective_mode"] == nil || dryBody["mental_model_id"] != model.ID {
		t.Fatalf("dry run must report the upstream result shape: %v", dryBody)
	}
	refresh := send(mux, http.MethodPost, base+"/"+model.ID+"/refresh", "")
	if refresh.Code != http.StatusOK {
		t.Fatalf("refresh: %d, body = %s", refresh.Code, refresh.Body.String())
	}
	var submitted SubmitResponse
	if err := json.Unmarshal(refresh.Body.Bytes(), &submitted); err != nil {
		t.Fatal(err)
	}
	if submitted.OperationID == "" || submitted.Status != "queued" {
		t.Fatalf("refresh must return the submit envelope: %+v", submitted)
	}
	if rec := send(mux, http.MethodDelete, base+"/"+model.ID, ""); rec.Code != http.StatusOK {
		t.Fatalf("delete: %d", rec.Code)
	}
	if rec := send(mux, http.MethodGet, base+"/"+model.ID, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete: %d, want 404", rec.Code)
	}
}

// Body-validation failures on create and update carry the structured 422
// envelope, not a bare 400.
func TestMentalModelCreateValidation(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"
	detail := decodeValidationFailure(t, send(mux, http.MethodPost, base, `{"source_query":"x"}`))
	if len(detail) == 0 || detail[0].Loc[0] != "body" {
		t.Fatalf("missing name detail = %+v", detail)
	}
	detail = decodeValidationFailure(t, send(mux, http.MethodPost, base, `{"name":"x"}`))
	if len(detail) == 0 || detail[0].Loc[1] != "source_query" {
		t.Fatalf("missing source_query detail = %+v", detail)
	}
	// An unmodelled body field is reported as ignored, never honoured or
	// rejected: the reference's create model has no subtype.
	if rec := send(mux, http.MethodPost, base, `{"name":"x","source_query":"q","subtype":"bogus"}`); rec.Code != http.StatusOK {
		t.Fatalf("an unknown body field must be ignored: %d, body = %s", rec.Code, rec.Body.String())
	}
	decodeValidationFailure(t, send(mux, http.MethodPost, base, `not json`))

	// A patch with a malformed body is a validation failure too.
	decodeValidationFailure(t, send(mux, http.MethodPatch, base+"/x", `not json`))
}

// A trigger is parsed, validated and echoed; an invalid member is a 422.
func TestMentalModelTriggerValidationAndEcho(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"

	created := send(mux, http.MethodPost, base, `{"name":"x","source_query":"q","trigger":{"mode":"delta","keep_trace":true}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("create with trigger: %d, body = %s", created.Code, created.Body.String())
	}
	var createdResp CreateResponse
	if err := json.Unmarshal(created.Body.Bytes(), &createdResp); err != nil {
		t.Fatal(err)
	}
	got := send(mux, http.MethodGet, base+"/"+createdResp.MentalModelID, "")
	var model Model
	if err := json.Unmarshal(got.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if mode, _ := model.Trigger.Value("mode"); mode != "delta" {
		t.Fatalf("trigger not echoed: %+v", model.Trigger)
	}
	if keepTrace, _ := model.Trigger.Value("keep_trace"); keepTrace != true {
		t.Fatalf("trigger not echoed: %+v", model.Trigger)
	}

	// An invalid enum member is rejected with the structured envelope.
	detail := decodeValidationFailure(t, send(mux, http.MethodPost, base,
		`{"name":"x","source_query":"q","trigger":{"mode":"sideways"}}`))
	if len(detail) == 0 || detail[0].Loc[2] != "mode" {
		t.Fatalf("invalid mode detail = %+v", detail)
	}
	// Mutually exclusive triggers are rejected.
	decodeValidationFailure(t, send(mux, http.MethodPost, base,
		`{"name":"x","source_query":"q","trigger":{"refresh_after_consolidation":true,"refresh_cron":"0 3 * * *"}}`))
	// An empty fact_types list is rejected.
	decodeValidationFailure(t, send(mux, http.MethodPost, base,
		`{"name":"x","source_query":"q","trigger":{"fact_types":[]}}`))

	// Patching a trigger overlays only the sent member.
	patched := send(mux, http.MethodPatch, base+"/"+createdResp.MentalModelID, `{"trigger":{"refresh_after_consolidation":true}}`)
	if patched.Code != http.StatusOK {
		t.Fatalf("patch trigger: %d, body = %s", patched.Code, patched.Body.String())
	}
	if err := json.Unmarshal(patched.Body.Bytes(), &model); err != nil {
		t.Fatal(err)
	}
	if mode, _ := model.Trigger.Value("mode"); mode != "delta" {
		t.Fatalf("trigger patch did not overlay: %+v", model.Trigger)
	}
	if after, _ := model.Trigger.Value("refresh_after_consolidation"); after != true {
		t.Fatalf("trigger patch did not overlay: %+v", model.Trigger)
	}
}

// A client-supplied id is honored, validated and conflicts with an existing one.
func TestMentalModelClientSuppliedID(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"

	rec := send(mux, http.MethodPost, base, `{"id":"team-comms","name":"Team comms","source_query":"q"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create with id: %d, body = %s", rec.Code, rec.Body.String())
	}
	var created CreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.MentalModelID != "team-comms" {
		t.Fatalf("id not honored: %+v", created)
	}
	// A collision is a conflict.
	if rec := send(mux, http.MethodPost, base, `{"id":"team-comms","name":"again","source_query":"q"}`); rec.Code != http.StatusConflict {
		t.Fatalf("collision: %d, want 409", rec.Code)
	}
	// The reference declares id a plain string with no shape validator, so a
	// non-slug id is accepted verbatim.
	if rec := send(mux, http.MethodPost, base, `{"id":"Bad Id","name":"x","source_query":"q"}`); rec.Code != http.StatusOK {
		t.Fatalf("id shape must not be validated: %d, body = %s", rec.Code, rec.Body.String())
	}
}

// History is a bare JSON array whose entries carry the reference's keys.
func TestMentalModelHistoryBareArray(t *testing.T) {
	mux, store := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"
	rec := send(mux, http.MethodPost, base, `{"name":"x","source_query":"q"}`)
	var created CreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	previous := "old content"
	if err := store.RecordMentalModelHistory(context.Background(), "b1", created.MentalModelID, storage.MentalModelHistoryEntry{
		ChangedAt:       time.Unix(1000, 0).UTC(),
		PreviousContent: &previous,
	}); err != nil {
		t.Fatal(err)
	}
	newer := "newer content"
	if err := store.RecordMentalModelHistory(context.Background(), "b1", created.MentalModelID, storage.MentalModelHistoryEntry{
		ChangedAt:       time.Unix(2000, 0).UTC(),
		PreviousContent: &newer,
	}); err != nil {
		t.Fatal(err)
	}
	history := send(mux, http.MethodGet, base+"/"+created.MentalModelID+"/history", "")
	var entries []map[string]any
	if err := json.Unmarshal(history.Body.Bytes(), &entries); err != nil {
		t.Fatalf("history must be a bare array: %s", history.Body.String())
	}
	if len(entries) != 2 {
		t.Fatalf("history = %+v", entries)
	}
	// Most recent first.
	if entries[0]["previous_content"] != newer {
		t.Fatalf("history is not most-recent-first: %+v", entries)
	}
	if entries[1]["previous_content"] != previous {
		t.Fatalf("previous_content missing: %+v", entries[1])
	}
	if _, ok := entries[0]["changed_at"]; !ok {
		t.Fatalf("changed_at missing: %+v", entries[0])
	}
}

// A create on an unknown bank lazily creates the bank instead of 404ing.
func TestMentalModelCreateLazilyCreatesBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	handlers := NewHandlers(store)
	handlers.DryRun = func(context.Context, string, string) (*DryRunResult, error) { return &DryRunResult{}, nil }
	handlers.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "fresh")
			if strings.Contains(pattern, "{mental_model_id}") {
				r.SetPathValue("mental_model_id", lastSegment(r.URL.Path))
			}
			handler(w, r)
		})
	})
	rec := send(mux, http.MethodPost, "/v1/default/banks/fresh/mental-models", `{"name":"x","source_query":"q"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create on unknown bank: %d, body = %s", rec.Code, rec.Body.String())
	}
	if _, err := store.GetBank(context.Background(), "fresh"); err != nil {
		t.Fatalf("bank was not created: %v", err)
	}
}

// A read against an unknown bank answers not-found.
func TestMentalModelUnknownBank(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	if rec := send(mux, http.MethodGet, "/v1/default/banks/missing/mental-models", ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank: %d, want 404", rec.Code)
	}
}

// A trigger's integer members are named the way the reference's framework names
// them: a string that will not parse, a value of another kind, and a number with
// a fraction each get their own entry rather than one shared message.
func TestMentalModelTriggerIntegerVocabulary(t *testing.T) {
	mux, _ := newServer(t)
	const base = "/v1/default/banks/b1/mental-models"
	cases := []struct {
		name     string
		body     string
		wantType string
		wantMsg  string
	}{
		{
			name:     "string that will not parse",
			body:     `{"name":"x","source_query":"q","trigger":{"min_refresh_interval_seconds":"soon"}}`,
			wantType: "int_parsing",
			wantMsg:  "Input should be a valid integer, unable to parse string as an integer",
		},
		{
			name:     "value of another kind",
			body:     `{"name":"x","source_query":"q","trigger":{"reflect_search_observations_max_tokens":{}}}`,
			wantType: "int_type",
			wantMsg:  "Input should be a valid integer",
		},
		{
			name:     "number with a fraction",
			body:     `{"name":"x","source_query":"q","trigger":{"min_refresh_interval_seconds":1.5}}`,
			wantType: "int_from_float",
			wantMsg:  "Input should be a valid integer, got a number with a fractional part",
		},
		{
			name:     "below its bound",
			body:     `{"name":"x","source_query":"q","trigger":{"reflect_search_observations_max_tokens":0}}`,
			wantType: "greater_than_equal",
			wantMsg:  "Input should be greater than or equal to 1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := decodeValidationFailure(t, send(mux, http.MethodPost, base, tc.body))
			if len(detail) == 0 {
				t.Fatal("empty detail")
			}
			if detail[0].Type != tc.wantType {
				t.Fatalf("type = %q, want %q", detail[0].Type, tc.wantType)
			}
			if detail[0].Msg != tc.wantMsg {
				t.Fatalf("msg = %q, want %q", detail[0].Msg, tc.wantMsg)
			}
			if len(detail[0].Loc) != 3 || detail[0].Loc[0] != "body" || detail[0].Loc[1] != "trigger" {
				t.Fatalf("loc = %v, want [body trigger <member>]", detail[0].Loc)
			}
		})
	}
}

// asHTTPError names the storage sentinels a route can hit and treats anything
// it does not name as a server fault, which is the reference's catch-all.
func TestMentalModelErrorMapping(t *testing.T) {
	cases := []struct {
		err    error
		expect int
	}{
		{storage.ErrBankNotFound, http.StatusNotFound},
		{storage.ErrMentalModelNotFound, http.StatusNotFound},
		{storage.ErrInvalidMentalModelSubtype, http.StatusBadRequest},
		{storage.ErrEmbeddingDimensions, http.StatusBadRequest},
		{storage.ErrInvalidBankID, http.StatusBadRequest},
		{storage.ErrAliasConflict, http.StatusConflict},
		// The dry-run route surfaces the reflection boundary's model failure.
		{storage.ErrReflectModelUnavailable, http.StatusBadRequest},
		{storage.ErrWritesUnavailable, http.StatusServiceUnavailable},
		{errors.New("storage exploded"), http.StatusInternalServerError},
	}
	for _, tc := range cases {
		if got := asHTTPError(tc.err); got == nil || got.Status != tc.expect {
			t.Fatalf("error %v mapped to %+v, want status %d", tc.err, got, tc.expect)
		}
	}
	if asHTTPError(nil) != nil {
		t.Fatal("a nil error must map to nil")
	}
}

// The framework validates the list's constrained query parameters before the
// handler runs, so a bad parameter is its 422 even on a bank the handler's bank
// probe would refuse; the probe alone still answers not-found.
func TestListValidatesParamsBeforeBankProbe(t *testing.T) {
	store := storage.NewMemoryStore("public")
	mux := http.NewServeMux()
	NewHandlers(store).BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, func(w http.ResponseWriter, r *http.Request) {
			r.SetPathValue("bank_id", "missing")
			handler(w, r)
		})
	})
	const base = "/v1/default/banks/missing/mental-models"
	if rec := send(mux, http.MethodGet, base, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown bank alone: %d, want 404", rec.Code)
	}
	for _, query := range []string{
		"detail=bogus", "tags_match=bogus", "limit=bogus", "offset=bogus",
		"exclude_knowledge_pages=bogus",
	} {
		detail := decodeValidationFailure(t, send(mux, http.MethodGet, base+"?"+query, ""))
		if len(detail) != 1 {
			t.Fatalf("%s: detail = %+v, want one item", query, detail)
		}
	}
}

// A deployment with no LLM provider refuses refresh work before any lookup, so
// a model that does not exist is the same 400 refusal rather than a 404.
func TestRefreshRefusesWithoutModelConfigured(t *testing.T) {
	store := storage.NewMemoryStore("public")
	h := NewHandlers(store)
	h.RefuseWithoutRefreshModel = true

	req := httptest.NewRequest(http.MethodPost,
		"/v1/default/banks/b1/mental-models/no-such-model/refresh", nil)
	req.SetPathValue("bank_id", "b1")
	req.SetPathValue("mental_model_id", "no-such-model")
	rec := httptest.NewRecorder()
	h.Refresh(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("refresh = %d, want 400: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	const want = "Mental model refresh requires an LLM provider. Current provider is set to 'none'. Set WENYUAN_API_LLM_PROVIDER to a real provider (e.g., openai, anthropic, gemini)."
	if body["detail"] != want {
		t.Fatalf("detail = %v, want %q", body["detail"], want)
	}
}

// The reference binds RefreshMentalModelRequest before the handler runs, so a
// body the request model refuses is its 422 before anything the handler would
// answer otherwise: an unknown model is still a 422, and the no-provider gate
// does not pre-empt the body either. The refusal itself is unchanged on an
// acceptable (or absent) body.
func TestRefreshBodyValidatedBeforeGates(t *testing.T) {
	cases := []struct {
		name   string
		refuse bool
		want   int
	}{
		{"unknown model", false, http.StatusUnprocessableEntity},
		{"no provider configured", true, http.StatusUnprocessableEntity},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := NewHandlers(storage.NewMemoryStore("public"))
			h.RefuseWithoutRefreshModel = tc.refuse
			req := httptest.NewRequest(http.MethodPost,
				"/v1/default/banks/b1/mental-models/no-such-model/refresh",
				strings.NewReader(`{"mode":"bogus"}`))
			req.Header.Set("Content-Type", "application/json")
			req.SetPathValue("bank_id", "b1")
			req.SetPathValue("mental_model_id", "no-such-model")
			rec := httptest.NewRecorder()
			h.Refresh(rec, req)

			detail := decodeValidationFailure(t, rec)
			if len(detail) != 1 || detail[0].Loc[1] != "mode" {
				t.Fatalf("detail = %+v, want one literal_error item under mode", detail)
			}
		})
	}
}
