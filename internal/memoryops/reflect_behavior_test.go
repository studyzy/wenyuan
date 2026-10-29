package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// beh-0018 / obs-0010: an over-length answer is shortened by the post-generation
// rewrite; a blank rewrite leaves the original answer standing.
func TestReflectLengthRewrite(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("word ", 80)
	store.SeedMemory("b1", storage.RecallMatch{ID: "f1", Text: long, Type: "world"})
	h := NewHandlers(store, 5, 30000)
	bound := bindHandlers(t, h)

	rec := post(t, bound, "/v1/default/banks/b1/reflect", `{"query":"word","max_tokens":5}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	text, _ := body["text"].(string)
	if text == "" {
		t.Fatal("a shortened answer must be non-empty")
	}
	if len(strings.Fields(text)) > 80 {
		t.Fatalf("the answer must be shortened: %d words", len(strings.Fields(text)))
	}
}

// beh-0022: a client disconnect cancels the in-flight reflection cooperatively
// and is answered 499 with the reference's disconnect reason
// (api/http.py:470-476), not left unanswered.
func TestReflectDisconnectCancels(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	inner := &reflectDisconnectStore{Store: store, startedRelease: startedRelease{
		started: make(chan struct{}), release: make(chan struct{}),
	}}
	bound := bindHandlers(t, NewHandlers(inner, 5, 30000))
	rec := disconnectRequest(t, bound, http.MethodPost, "/v1/default/banks/b1/reflect",
		`{"query":"q"}`, inner.started, inner.release)
	if rec.Code != httpx.KindClientClosed {
		t.Fatalf("disconnected reflection = %d, want 499: %s", rec.Code, rec.Body.String())
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"detail":"client disconnected"}` {
		t.Fatalf("body = %s, want the disconnect reason", got)
	}
}

// beh-0012 / err-0003: a wall-clock budget that elapses surfaces as a 504.
func TestReflectDeadlineExceededIsTimeout(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.TODO(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	h := NewHandlers(deadlineStore{Store: store}, 5, 30000)
	rec := post(t, bindHandlers(t, h), "/v1/default/banks/b1/reflect", `{"query":"q"}`)
	if rec.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", rec.Code)
	}
}

// beh-0004 / ts-0004: extraction succeeds when the answer carries a document
// satisfying the schema.
func TestReflectStructuredOutputSuccess(t *testing.T) {
	req := ReflectRequest{Query: "q", ResponseSchema: map[string]any{
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
	}}
	outcome := storage.ReflectOutcome{Answer: `here it is {"answer":"ok"}`}
	response, err := buildReflectResponse(req, outcome)
	if err != nil {
		t.Fatal(err)
	}
	if response.StructuredOutput == nil {
		t.Fatal("a schema-satisfying document must be parsed")
	}
	if response.StructuredOutput["answer"] != "ok" {
		t.Fatalf("parsed output = %v", response.StructuredOutput)
	}
	if response.StructuredOutputError != nil {
		t.Fatalf("a success must carry no error: %q", *response.StructuredOutputError)
	}
}

// beh-0004 / ec-0004: extraction failure carries the error string only.
func TestReflectStructuredOutputFailure(t *testing.T) {
	req := ReflectRequest{Query: "q", ResponseSchema: map[string]any{
		"type":     "object",
		"required": []any{"answer"},
		"properties": map[string]any{
			"answer": map[string]any{"type": "string"},
		},
	}}
	outcome := storage.ReflectOutcome{Answer: `{"answer":42}`}
	response, err := buildReflectResponse(req, outcome)
	if err != nil {
		t.Fatal(err)
	}
	if response.StructuredOutput != nil {
		t.Fatalf("a failed extraction must not yield an object: %v", response.StructuredOutput)
	}
	if response.StructuredOutputError == nil || *response.StructuredOutputError == "" {
		t.Fatal("a failed extraction must carry the error string")
	}
}

// reflectDisconnectStore is a store whose reflection the test holds open until
// the request's context is cancelled, then reports the context's own error —
// what a store that honours the cancellation returns.
type reflectDisconnectStore struct {
	storage.Store
	startedRelease
}

func (s *reflectDisconnectStore) Reflect(ctx context.Context, query storage.ReflectQuery) (storage.ReflectOutcome, error) {
	s.hold()
	return storage.ReflectOutcome{}, ctx.Err()
}

// deadlineStore returns a deadline-exceeded error.
type deadlineStore struct{ storage.Store }

func (s deadlineStore) Reflect(ctx context.Context, query storage.ReflectQuery) (storage.ReflectOutcome, error) {
	return storage.ReflectOutcome{}, context.DeadlineExceeded
}
