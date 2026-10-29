package cli

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type tenantRequest struct {
	Method string
	Path   string
	Body   map[string]any
}

type tenantStub struct {
	server   *httptest.Server
	requests []tenantRequest
}

func newTenantStub(t *testing.T, status int, response string) *tenantStub {
	t.Helper()
	stub := &tenantStub{}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		if len(body) > 0 {
			_ = json.Unmarshal(body, &decoded)
		}
		stub.requests = append(stub.requests, tenantRequest{Method: r.Method, Path: r.URL.Path, Body: decoded})
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(response))
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func runTenantCommand(t *testing.T, baseURL string, args ...string) (string, string, error) {
	t.Helper()
	registry := NewRegistry()
	RegisterTenantCommands(registry)
	cmd, rest, err := registry.Resolve(args)
	if err != nil {
		return "", "", err
	}
	positionals, err := cmd.ParseArgs(rest)
	if err != nil {
		return "", "", err
	}
	var out, errBuf strings.Builder
	deps := &Deps{
		Ctx:    context.Background(),
		Args:   positionals,
		Flags:  cmd.Flags(),
		Stdout: &out,
		Stderr: &errBuf,
		Stdin:  strings.NewReader(""),
		Format: FormatJSON,
		UI:     NewUI(&out, &errBuf, ColorEnv{Term: "dumb"}),
		Client: NewClient(baseURL, "operator"),
	}
	err = cmd.Execute(deps.Ctx, deps)
	return out.String(), errBuf.String(), err
}

func TestTenantCreate(t *testing.T) {
	stub := newTenantStub(t, http.StatusCreated, "{\"tenant_id\":\"acme\",\"display_name\":\"Acme\",\"status\":\"active\"}")
	out, _, err := runTenantCommand(t, stub.server.URL, "tenant", "create", "acme", "--display-name", "Acme")
	if err != nil {
		t.Fatalf("tenant create: %v", err)
	}
	if len(stub.requests) != 1 {
		t.Fatalf("requests = %d, want 1", len(stub.requests))
	}
	request := stub.requests[0]
	if request.Method != http.MethodPost || request.Path != "/admin/v1/tenants" {
		t.Fatalf("request = %+v", request)
	}
	if request.Body["tenant_id"] != "acme" || request.Body["display_name"] != "Acme" {
		t.Fatalf("body = %+v", request.Body)
	}
	if !strings.Contains(out, "\"tenant_id\": \"acme\"") {
		t.Fatalf("output = %q", out)
	}
}

func TestTenantList(t *testing.T) {
	stub := newTenantStub(t, http.StatusOK, "{\"tenants\":[{\"tenant_id\":\"acme\",\"status\":\"active\"}]}")
	out, _, err := runTenantCommand(t, stub.server.URL, "tenant", "list", "--status", "active")
	if err != nil {
		t.Fatalf("tenant list: %v", err)
	}
	if len(stub.requests) != 1 || stub.requests[0].Path != "/admin/v1/tenants" {
		t.Fatalf("requests = %+v", stub.requests)
	}
	if !strings.Contains(out, "acme") {
		t.Fatalf("output = %q", out)
	}
}

func TestTenantKeyCreate(t *testing.T) {
	stub := newTenantStub(t, http.StatusCreated, "{\"plaintext\":\"wyk_secret\",\"key\":{\"key_id\":\"k1\",\"prefix\":\"wyk_abcd\",\"label\":\"ci\"}}")
	out, _, err := runTenantCommand(t, stub.server.URL, "tenant", "key", "create", "acme", "--label", "ci")
	if err != nil {
		t.Fatalf("tenant key create: %v", err)
	}
	request := stub.requests[0]
	if request.Method != http.MethodPost || request.Path != "/admin/v1/tenants/acme/keys" {
		t.Fatalf("request = %+v", request)
	}
	if request.Body["label"] != "ci" {
		t.Fatalf("body = %+v", request.Body)
	}
	if !strings.Contains(out, "wyk_secret") {
		t.Fatalf("output = %q, want the plaintext", out)
	}
}

func TestTenantSession(t *testing.T) {
	stub := newTenantStub(t, http.StatusCreated, "{\"plaintext\":\"wyk_session\",\"key\":{\"key_id\":\"k2\",\"ephemeral\":true}}")
	out, _, err := runTenantCommand(t, stub.server.URL, "tenant", "session", "acme")
	if err != nil {
		t.Fatalf("tenant session: %v", err)
	}
	if stub.requests[0].Path != "/admin/v1/tenants/acme/session" {
		t.Fatalf("request = %+v", stub.requests[0])
	}
	if !strings.Contains(out, "wyk_session") {
		t.Fatalf("output = %q", out)
	}
}

func TestTenantCreateRequiresID(t *testing.T) {
	stub := newTenantStub(t, http.StatusCreated, "{}")
	_, _, err := runTenantCommand(t, stub.server.URL, "tenant", "create")
	if err == nil {
		t.Fatal("tenant create without an id should fail")
	}
	if len(stub.requests) != 0 {
		t.Fatalf("made %d requests, want none", len(stub.requests))
	}
}
