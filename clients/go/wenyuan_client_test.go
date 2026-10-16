package wenyuan

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestClient starts an httptest server running handler and returns a client
// bound to it. The server is closed when the test ends.
func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewClientWithBaseURL(srv.URL)
}

func writeJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("writing response: %v", err)
	}
}

func TestRecallRoundTrip(t *testing.T) {
	var gotMethod, gotPath, gotQuery string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		gotPath = r.URL.Path
		var req RecallRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding recall request: %v", err)
		}
		gotQuery = req.Query
		writeJSON(t, w, `{"results":[{"id":"m1","text":"Alice likes Go"}]}`)
	})

	resp, err := client.Recall(context.Background(), "b1", *NewRecallRequest("what does Alice like"))
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}

	if gotMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", gotMethod)
	}
	if want := "/v1/default/banks/b1/memories/recall"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotQuery != "what does Alice like" {
		t.Errorf("query = %q, want %q", gotQuery, "what does Alice like")
	}
	if len(resp.Results) != 1 {
		t.Fatalf("results = %d, want 1", len(resp.Results))
	}
	if resp.Results[0].Id != "m1" || resp.Results[0].Text != "Alice likes Go" {
		t.Errorf("result = %+v", resp.Results[0])
	}
}

func TestRetainRoundTrip(t *testing.T) {
	var gotPath, gotContent string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var req RetainRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decoding retain request: %v", err)
		}
		if len(req.Items) > 0 && req.Items[0].Content.String != nil {
			gotContent = *req.Items[0].Content.String
		}
		writeJSON(t, w, `{"success":true,"bank_id":"b1","items_count":1,"async":false}`)
	})

	req := RetainRequest{Items: []MemoryItem{{Content: TextContent("hello world")}}}
	resp, err := client.Retain(context.Background(), "b1", req)
	if err != nil {
		t.Fatalf("Retain: %v", err)
	}

	if want := "/v1/default/banks/b1/memories"; gotPath != want {
		t.Errorf("path = %q, want %q", gotPath, want)
	}
	if gotContent != "hello world" {
		t.Errorf("content = %q, want %q", gotContent, "hello world")
	}
	if !resp.Success || resp.ItemsCount != 1 {
		t.Errorf("response = %+v", resp)
	}
}

func TestReflectRoundTrip(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if want := "/v1/default/banks/b1/reflect"; r.URL.Path != want {
			t.Errorf("path = %q, want %q", r.URL.Path, want)
		}
		writeJSON(t, w, `{"text":"Alice likes Go."}`)
	})

	resp, err := client.Reflect(context.Background(), "b1", *NewReflectRequest("what does Alice like"))
	if err != nil {
		t.Fatalf("Reflect: %v", err)
	}
	if resp.Text != "Alice likes Go." {
		t.Errorf("text = %q", resp.Text)
	}
}

func TestCreateAndGetBank(t *testing.T) {
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/v1/default/banks/b1":
			var req CreateBankRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Errorf("decoding create request: %v", err)
			}
			if req.GetName() != "Team" {
				t.Errorf("name = %q, want Team", req.GetName())
			}
			writeJSON(t, w, bankProfileJSON)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/default/banks/b1/config":
			writeJSON(t, w, bankConfigJSON)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	create := CreateBankRequest{}
	create.SetName("Team")
	created, err := client.CreateBank(context.Background(), "b1", create)
	if err != nil {
		t.Fatalf("CreateBank: %v", err)
	}
	if created.BankId != "b1" || created.Name != "Team" {
		t.Errorf("created = %+v", created)
	}

	// The profile read is retired by the contract (410), so the helper reads the
	// configuration endpoint instead.
	fetched, err := client.GetBankConfig(context.Background(), "b1")
	if err != nil {
		t.Fatalf("GetBankConfig: %v", err)
	}
	if fetched.BankId != "b1" {
		t.Errorf("fetched = %+v", fetched)
	}
	if got := fetched.Config["retain_extraction_mode"]; got != "concise" {
		t.Errorf("config[retain_extraction_mode] = %v, want concise", got)
	}
}

func TestDefaultConstructorReadsBaseURLEnv(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, `{"results":[]}`)
	}))
	defer srv.Close()

	t.Setenv(BaseURLEnvVar, srv.URL)
	client := NewClient()
	if got := client.cfg.Servers[0].URL; got != srv.URL {
		t.Fatalf("client base URL = %q, want %q", got, srv.URL)
	}
	if _, err := client.Recall(context.Background(), "b1", *NewRecallRequest("q")); err != nil {
		t.Fatalf("Recall: %v", err)
	}

	t.Setenv(BaseURLEnvVar, "")
	if got := BaseURL(); got != DefaultBaseURL {
		t.Errorf("BaseURL() = %q, want %q", got, DefaultBaseURL)
	}
}

func TestNewClientWithTokenSendsBearer(t *testing.T) {
	var gotAuth string
	client := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		writeJSON(t, w, `{"results":[]}`)
	})
	// Rebind the client to carry a token, keeping the same test server.
	withToken := NewClientWithToken(client.cfg.Servers[0].URL, "secret")
	if _, err := withToken.Recall(context.Background(), "b1", *NewRecallRequest("q")); err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if gotAuth != "Bearer secret" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer secret")
	}
}

const bankProfileJSON = `{"bank_id":"b1","name":"Team","disposition":{"skepticism":3,"literalism":3,"empathy":3},"mission":"help the team"}`

const bankConfigJSON = `{"bank_id":"b1","config":{"retain_extraction_mode":"concise"},"overrides":{}}`
