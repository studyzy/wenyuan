package memoryops

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

func timePtr(t time.Time) *time.Time { return &t }

func intPtr(v int) *int { return &v }

// errSentinel is an unclassified read failure mapped to the internal-error
// status.
var errSentinel = errors.New("unexpected read failure")

// ts-0001: recall of a matching query returns a 200 with a ranked list.
func TestRecallMatchingQueryReturnsRankedList(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{
		ID: "m1", Text: "hello world", Type: "world",
		MentionedAt: timePtr(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)),
	})
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 || response.Results[0].Text != "hello world" {
		t.Fatalf("expected the matching memory text: %s", rec.Body.String())
	}
}

// ts-0002 / ec-0001: a query that matches nothing returns a 200 with an empty
// collection, not null.
func TestRecallWithNoMatchesReturnsEmptyCollection(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"zzzzz"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Results == nil {
		t.Fatalf("results must be an empty collection, not null: %s", rec.Body.String())
	}
	if len(response.Results) != 0 {
		t.Fatalf("expected no results, got %d", len(response.Results))
	}
}

// ts-0003: omitting the fact-type selection recalls every supported type.
func TestRecallOmittedTypesRecallsAll(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "w", Text: "alpha", Type: "world"})
	store.SeedMemory("b1", storage.RecallMatch{ID: "e", Text: "alpha", Type: "experience"})
	store.SeedMemory("b1", storage.RecallMatch{ID: "o", Text: "alpha", Type: "observation"})
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha","max_tokens":100}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 3 {
		t.Fatalf("omitted selection must recall every type, got %d: %s", len(response.Results), rec.Body.String())
	}
}

// ts-0003: an explicit selection recalls only the named types.
func TestRecallExplicitTypesFilters(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{ID: "w", Text: "alpha", Type: "world"})
	store.SeedMemory("b1", storage.RecallMatch{ID: "e", Text: "alpha", Type: "experience"})
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha","types":["world"],"max_tokens":100}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Type != "world" {
		t.Fatalf("explicit selection must recall only the named types: %s", rec.Body.String())
	}
}

// ts-0004: each accepted budget level is honored.
func TestRecallAcceptsEachBudgetLevel(t *testing.T) {
	mux, _ := newServer(t)
	for _, level := range []string{"low", "mid", "high"} {
		rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q","budget":"`+level+`"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("budget %q status = %d, body = %s", level, rec.Code, rec.Body.String())
		}
	}
}

// ts-0016: a malformed query-time anchor is a 400; a valid one is accepted.
func TestRecallQueryTimestampValidation(t *testing.T) {
	mux, _ := newServer(t)
	bad := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q","query_timestamp":"not-a-date"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("malformed anchor status = %d, want 400", bad.Code)
	}
	good := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q","query_timestamp":"2026-01-02T03:04:05Z"}`)
	if good.Code != http.StatusOK {
		t.Fatalf("valid anchor status = %d, body = %s", good.Code, good.Body.String())
	}
}

// ts-0018: an invalid budget value, unsupported fact type and malformed body
// are each rejected with the validation status.
func TestRecallValidationFailures(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"invalid budget", `{"query":"q","budget":"extra"}`, http.StatusUnprocessableEntity},
		{"unsupported type", `{"query":"q","types":["bogus"]}`, http.StatusUnprocessableEntity},
		{"malformed body", `{"query":`, http.StatusUnprocessableEntity},
		{"mutually exclusive tags", `{"query":"q","tags":["t"],"tag_groups":[["x"]]}`, http.StatusUnprocessableEntity},
		{"out of range pagination", `{"query":"q","limit":5000}`, http.StatusUnprocessableEntity},
	}
	mux, _ := newServer(t)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := post(t, mux, "/v1/default/banks/b1/memories/recall", tc.body)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d, body = %s", rec.Code, tc.want, rec.Body.String())
			}
		})
	}
}

// ts-0019 / err-0001: a query with no word characters is rejected.
func TestRecallRejectsQueryWithoutWordCharacters(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"!!!"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422, body = %s", rec.Code, rec.Body.String())
	}
}

// ts-0018: the unsupported fact-type message names the accepted types.
func TestRecallUnsupportedTypeMessageNamesAccepted(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q","types":["bogus"]}`)
	raw := rec.Body.String()
	for _, want := range []string{"world", "experience", "observation"} {
		if !strings.Contains(raw, want) {
			t.Fatalf("message must name accepted type %q: %s", want, raw)
		}
	}
}

// ts-0020: a recall against a bank that does not exist is 404.
func TestRecallUnknownBankReturnsNotFound(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/missing/memories/recall", `{"query":"q"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d", rec.Code)
	}
}

// ts-0021: a read exceeding its timeout is 504 with a message; a shed request
// is 503 with a retry-after header.
func TestRecallTimeoutReturnsGatewayTimeout(t *testing.T) {
	mux, store := newServer(t)
	store.SetReadError(storage.ErrReadTimeout)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"slow"}`)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
	var envelope map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope["detail"] == "" {
		t.Fatalf("timeout must carry an explanatory message: %s", rec.Body.String())
	}
}

func TestRecallShedReturnsServiceUnavailableWithRetryAfter(t *testing.T) {
	mux, store := newServer(t)
	store.SetReadError(storage.ErrReadShed)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	if rec.Header().Get("Retry-After") == "" {
		t.Fatalf("shed response must carry Retry-After")
	}
}

// ts-0022: an unhandled failure is 500 carrying the detail.
func TestRecallUnhandledErrorReturnsInternal(t *testing.T) {
	mux, store := newServer(t)
	store.SetReadError(errSentinel)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q"}`)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

// ts-0008: entity states are included by default; chunks and source facts are
// excluded unless their sub-object is supplied.
func TestRecallIncludeDefaults(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{
		ID: "m1", Text: "alpha beta", Type: "world",
		Entities: []string{"e1"}, ChunkID: "c1", SourceMemoryIDs: []string{"f1"},
	})
	def := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha","max_tokens":100}`)
	var defResp RecallResponse
	if err := json.Unmarshal(def.Body.Bytes(), &defResp); err != nil {
		t.Fatal(err)
	}
	if defResp.Chunks != nil || defResp.SourceFacts != nil {
		t.Fatalf("chunks and source facts must be excluded by default: %s", def.Body.String())
	}
	withChunks := post(t, mux, "/v1/default/banks/b1/memories/recall",
		`{"query":"alpha","max_tokens":100,"include":{"chunks":{"max_tokens":10},"source_facts":{"max_tokens":10}}}`)
	var rich RecallResponse
	if err := json.Unmarshal(withChunks.Body.Bytes(), &rich); err != nil {
		t.Fatal(err)
	}
	if rich.Chunks == nil {
		t.Fatalf("chunks must be present when included: %s", withChunks.Body.String())
	}
}

// ts-0023: result item fields are reported under their exact names.
func TestRecallResultExposesDocumentedFields(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{
		ID:              "m1",
		Text:            "hello world",
		Type:            "world",
		Context:         "ctx",
		ChunkID:         "c1",
		DocumentID:      "d1",
		Metadata:        map[string]any{"k": "v"},
		SourceMemoryIDs: []string{"f1"},
		Scores:          map[string]float64{"semantic": 0.9},
		Entities:        []string{"e1"},
		OccurredStart:   timePtr(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)),
		OccurredEnd:     timePtr(time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)),
		MentionedAt:     timePtr(time.Date(2026, 1, 3, 0, 0, 0, 0, time.UTC)),
	})
	body := `{"query":"hello","max_tokens":100,"include":{"entities":{},"chunks":{},"source_facts":{}},"trace":true}`
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	raw := rec.Body.String()
	for _, field := range []string{
		"results", "id", "text", "type", "context", "chunk_id", "document_id",
		"metadata", "source_fact_ids", "scores", "final", "entities", "tags",
		"attachments", "trace", "occurred_start", "occurred_end", "mentioned_at",
	} {
		if !strings.Contains(raw, `"`+field+`"`) {
			t.Fatalf("recall result missing documented field %q: %s", field, raw)
		}
	}
}

// ts-0010: the trace object is present only when requested.
func TestRecallTracePresentWhenRequested(t *testing.T) {
	mux, _ := newServer(t)
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"q","trace":true}`)
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Trace == nil {
		t.Fatalf("trace object must be present when requested: %s", rec.Body.String())
	}
}

// ts-0015: caller metadata is preserved onto the result.
func TestRecallPreservesCallerMetadata(t *testing.T) {
	mux, store := newServer(t)
	store.SeedMemory("b1", storage.RecallMatch{
		ID: "m1", Text: "alpha", Type: "world", Metadata: map[string]any{"src": "caller"},
	})
	rec := post(t, mux, "/v1/default/banks/b1/memories/recall", `{"query":"alpha","max_tokens":10}`)
	var response RecallResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Metadata["src"] != "caller" {
		t.Fatalf("metadata must be preserved: %s", rec.Body.String())
	}
}

// The recall route answers on the documented path with the documented method.
func TestRecallPathAndMethod(t *testing.T) {
	mux, _ := newServer(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/default/banks/b1/memories/recall", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("recall must be POST-only, got %d", rec.Code)
	}
}
