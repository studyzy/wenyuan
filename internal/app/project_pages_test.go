package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// TestProjectBankStartsWithKnowledgePages drives the whole path through the
// served handlers: the management plane creates a team and a project, and the
// data plane — as the team — reads the project bank's knowledge tree back.
//
// The pages come from the administration plane rather than from the
// coding-agents package, which only seeds the banks its own sessions own, so
// this is the one assertion that the two halves are actually wired together.
func TestProjectBankStartsWithKnowledgePages(t *testing.T) {
	application := multiTenantApp(t)
	admin := application.AdminHandler

	recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/teams",
		`{"tenant_id":"acme","display_name":"Acme"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create team = %d: %s", recorder.Code, recorder.Body.String())
	}
	var team struct {
		Plaintext string `json:"plaintext"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &team); err != nil {
		t.Fatalf("decode team: %v", err)
	}
	if recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/teams/acme/projects",
		`{"project_id":"web","name":"Web App"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}

	tree := httptest.NewRequest(http.MethodGet,
		"/v1/default/banks/"+url.PathEscape("acme::web")+"/knowledge-base/tree", nil)
	tree.Header.Set("Authorization", "Bearer "+team.Plaintext)
	treeRecorder := httptest.NewRecorder()
	application.Handler.ServeHTTP(treeRecorder, tree)
	if treeRecorder.Code != http.StatusOK {
		t.Fatalf("knowledge tree = %d: %s", treeRecorder.Code, treeRecorder.Body.String())
	}
	var payload struct {
		Roots []struct {
			Name string `json:"name"`
			Kind string `json:"kind"`
		} `json:"roots"`
	}
	if err := json.Unmarshal(treeRecorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode tree: %v", err)
	}
	want := map[string]bool{
		"Component map":                false,
		"Core concepts":                false,
		"Conventions and patterns":     false,
		"Key decisions and rationale":  false,
		"Initiatives and enhancements": false,
	}
	for _, root := range payload.Roots {
		if root.Kind != "page" {
			t.Errorf("%q is a %s, want a page", root.Name, root.Kind)
			continue
		}
		if _, ok := want[root.Name]; !ok {
			t.Errorf("unexpected seeded page %q", root.Name)
			continue
		}
		want[root.Name] = true
	}
	for name, seen := range want {
		if !seen {
			t.Errorf("the project bank has no %q page", name)
		}
	}
}
