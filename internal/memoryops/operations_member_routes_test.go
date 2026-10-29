package memoryops

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// memberMux binds the operation member routes over the given store, leaving
// the operation identifier to the URL the way the real router does.
func memberMux(t *testing.T, store storage.Store) http.Handler {
	t.Helper()
	h := NewHandlers(store, 5, 30000)
	mux := http.NewServeMux()
	h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
		mux.HandleFunc(method+" "+pattern, handler)
	})
	host := httpx.NewExtensionHost()
	host.RegisterOperationValidator(httpx.UserTagScopeValidator{})
	return httpx.Chain(mux, httpx.OperationValidationMiddleware(host))
}

// A malformed operation identifier is answered with its 400 before the
// scoped-caller refusal: the reference's route parses the UUID first, so a
// scoped caller sees the format error, not a 403 naming the refusal
// (api/http.py:8575-8578, 8601-8615, 8640-8650).
func TestMalformedOperationIDPrecedesScopedRefusal(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	handler := memberMux(t, store)
	const malformed = "not-a-uuid"
	cases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "cancel", method: http.MethodDelete, path: "/v1/default/banks/b1/operations/" + malformed},
		{name: "retry", method: http.MethodPost, path: "/v1/default/banks/b1/operations/" + malformed + "/retry"},
		{name: "delete", method: http.MethodDelete, path: "/v1/default/banks/b1/operations/" + malformed + "/delete"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("X-API-Key", "dan")
			scoped := httptest.NewRecorder()
			handler.ServeHTTP(scoped, req)
			if scoped.Code != http.StatusBadRequest {
				t.Fatalf("scoped = %d, want 400 (body %s)", scoped.Code, scoped.Body.String())
			}
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(scoped.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if want := "Invalid operation_id format: " + malformed; envelope.Detail != want {
				t.Fatalf("detail = %q, want %q", envelope.Detail, want)
			}
		})
	}
}

// A tag-scoped caller never gets payloads: the single-operation read forces
// include_payload off however it was queried, because the payload is the raw
// request someone else stored (engine/memory_engine.py:22064-22068).
func TestScopedCallerGetsNoTaskPayload(t *testing.T) {
	store := storage.NewMemoryStore("public")
	if _, err := store.CreateBank(context.Background(), storage.Bank{BankID: "b1", Name: "Bank"}); err != nil {
		t.Fatal(err)
	}
	opID := "22222222-2222-2222-2222-222222222222"
	if _, err := store.PutOperation(context.Background(), storage.Operation{
		OperationID: opID, BankID: "b1", OperationType: storage.OperationTypeRetain,
		Status:  storage.StatusPending,
		Payload: map[string]any{"items": []any{map[string]any{"content": "someone else's text"}}},
	}); err != nil {
		t.Fatal(err)
	}
	handler := memberMux(t, store)
	path := "/v1/default/banks/b1/operations/" + opID + "?include_payload=true"
	cases := []struct {
		name    string
		scoped  bool
		wantSet bool
	}{
		{name: "scoped caller", scoped: true, wantSet: false},
		{name: "bank administrator", scoped: false, wantSet: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			if tc.scoped {
				req.Header.Set("X-API-Key", "dan")
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (body %s)", rec.Code, rec.Body.String())
			}
			var body struct {
				TaskPayload *map[string]any `json:"task_payload"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if tc.wantSet && body.TaskPayload == nil {
				t.Fatal("task_payload = null, want the stored payload for an unscoped caller")
			}
			if !tc.wantSet && body.TaskPayload != nil {
				t.Fatalf("task_payload = %v, want it withheld from a scoped caller", body.TaskPayload)
			}
		})
	}
}

// A cancellation that loses the guarded write's race — the row reached a
// terminal state between the read and the write — answers the race's own 409
// text, not the plain status conflict (engine/memory_engine.py:22269-22276).
func TestCancelRaceAnswersConcurrentTerminalConflict(t *testing.T) {
	const opID = "33333333-3333-3333-3333-333333333333"
	cases := []struct {
		name    string
		err     error
		op      storage.Operation
		wantIn  string // a substring the detail must carry
		wantNot string // a substring the detail must not carry
	}{
		{
			name:    "concurrent terminal state",
			err:     storage.ErrOperationConcurrentlyTerminal,
			wantIn:  "it reached a terminal state concurrently",
			wantNot: "status is '",
		},
		{
			name:    "plain conflict keeps the status text",
			err:     storage.ErrOperationConflict,
			op:      storage.Operation{Status: storage.StatusCompleted},
			wantIn:  "status is 'completed'",
			wantNot: "concurrently",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := cancelRaceStore{Store: storage.NewMemoryStore("public"), op: tc.op, err: tc.err}
			h := NewHandlers(store, 5, 30000)
			mux := http.NewServeMux()
			h.BindRoutes(func(method, pattern string, handler http.HandlerFunc) {
				mux.HandleFunc(method+" "+pattern, handler)
			})
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/default/banks/b1/operations/"+opID, nil))
			if rec.Code != http.StatusConflict {
				t.Fatalf("status = %d, want 409 (body %s)", rec.Code, rec.Body.String())
			}
			var envelope struct {
				Detail string `json:"detail"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(envelope.Detail, tc.wantIn) || strings.Contains(envelope.Detail, tc.wantNot) {
				t.Fatalf("detail = %q, want %q without %q", envelope.Detail, tc.wantIn, tc.wantNot)
			}
		})
	}
}

// cancelRaceStore answers every cancellation with the failure under test.
type cancelRaceStore struct {
	storage.Store
	op  storage.Operation
	err error
}

func (s cancelRaceStore) CancelOperation(context.Context, string, string) (storage.Operation, error) {
	return s.op, s.err
}

// The typed detail's failure_reason is validated with the outcome: a value
// outside the enum yields no detail at all, exactly as the reference's model
// validation does (engine/memory_engine.py:2295-2303).
func TestOperationDetailsValidatesFailureReason(t *testing.T) {
	tests := []struct {
		name     string
		metadata map[string]any
		wantNil  bool
		wantGeo  string // the failure_reason the detail must carry, when not nil
	}{
		{
			name:     "absent reason projects as null",
			metadata: map[string]any{"outcome": "content_written"},
			wantGeo:  "",
		},
		{
			name:     "recognised reason is projected",
			metadata: map[string]any{"outcome": "refresh_failed_error", "failure_reason": "retrieval_failed"},
			wantGeo:  "retrieval_failed",
		},
		{
			name:     "unrecognised reason yields no detail",
			metadata: map[string]any{"outcome": "refresh_failed_error", "failure_reason": "bogus"},
			wantNil:  true,
		},
		{
			name:     "a non-string reason yields no detail",
			metadata: map[string]any{"outcome": "refresh_failed_error", "failure_reason": 42},
			wantNil:  true,
		},
		{
			name:     "an unrecognised outcome still yields no detail",
			metadata: map[string]any{"outcome": "bogus", "failure_reason": "retrieval_failed"},
			wantNil:  true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			details := operationDetails("refresh_mental_model", test.metadata)
			if test.wantNil {
				if details != nil {
					t.Fatalf("details = %v, want nil", details)
				}
				return
			}
			if details == nil {
				t.Fatal("details = nil, want a projection")
			}
			reason, _ := details["failure_reason"].(string)
			if reason != test.wantGeo {
				t.Fatalf("failure_reason = %v, want %q", details["failure_reason"], test.wantGeo)
			}
		})
	}
}
