package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// fakeBanks is an in-memory BankAdmin: it holds one team's bank ids and records
// what it deleted.
type fakeBanks struct {
	mu    sync.Mutex
	banks map[string][]string // schema -> bank ids
	gone  []string
}

func newFakeBanks() *fakeBanks { return &fakeBanks{banks: map[string][]string{}} }

func (f *fakeBanks) put(schema string, bankIDs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.banks[schema] = append(f.banks[schema], bankIDs...)
}

func (f *fakeBanks) ListBanks(_ context.Context, schema, prefix string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, 0)
	for _, id := range f.banks[schema] {
		if strings.HasPrefix(id, prefix) {
			out = append(out, id)
		}
	}
	return out, nil
}

func (f *fakeBanks) DeleteBank(_ context.Context, schema, bankID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	kept := f.banks[schema][:0]
	for _, id := range f.banks[schema] {
		if id != bankID {
			kept = append(kept, id)
		}
	}
	f.banks[schema] = kept
	f.gone = append(f.gone, bankID)
	return 1, nil
}

// teamFixture is one management plane over an in-process registry.
type teamFixture struct {
	handler *Handler
	tenants *storage.MemoryTenantRegistry
	teams   *storage.MemoryTeamRegistry
	banks   *fakeBanks
}

func newTeamFixture(t *testing.T) teamFixture {
	t.Helper()
	tenants := storage.NewMemoryTenantRegistry()
	teams := storage.NewMemoryTeamRegistry(tenants)
	banks := newFakeBanks()
	handler, err := New(Config{
		Registry:    tenants,
		OperatorKey: testOperatorKey,
		TeamConfig:  TeamConfig{Teams: teams, Banks: banks},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return teamFixture{handler: handler, tenants: tenants, teams: teams, banks: banks}
}

// seed creates an active tenant for the fixture.
func (f teamFixture) seed(t *testing.T, id string, kind tenant.Kind) {
	t.Helper()
	if err := f.tenants.CreateTenant(context.Background(), tenant.Record{
		ID: id, DisplayName: id, Status: tenant.StatusActive, Kind: kind,
	}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// do issues an authenticated management request and returns the recorder.
func (f teamFixture) do(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	request := adminRequest(method, path, body)
	if body == "" {
		request.Body = http.NoBody
		request.ContentLength = 0
	}
	return serve(t, f.handler, request)
}

// decodeMap decodes a response body into a generic object.
func decodeMap(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &value); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
	return value
}

func TestTeamRoutesRejectWithoutATeamRegistry(t *testing.T) {
	registry := storage.NewMemoryTenantRegistry()
	handler, err := New(Config{Registry: registry, OperatorKey: testOperatorKey})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/admin/v1/teams"},
		{http.MethodPost, "/admin/v1/teams"},
		{http.MethodGet, "/admin/v1/teams/acme"},
		{http.MethodGet, "/admin/v1/teams/acme/members"},
		{http.MethodGet, "/admin/v1/teams/acme/projects"},
	} {
		recorder := serve(t, handler, adminRequest(route.method, route.path, "{}"))
		if recorder.Code != http.StatusNotImplemented {
			t.Fatalf("%s %s = %d, want 501", route.method, route.path, recorder.Code)
		}
	}
}

func TestTeamLifecycleThroughTheAPI(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "alice", tenant.KindPersonal)
	fixture.seed(t, "bob", tenant.KindPersonal)

	// Create: a team tenant, provisioned, with one key handed back once.
	recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams", `{"tenant_id":"acme","display_name":"Acme"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create team = %d: %s", recorder.Code, recorder.Body.String())
	}
	created := decodeMap(t, recorder)
	plaintext, _ := created["plaintext"].(string)
	if !strings.HasPrefix(plaintext, "wyk_") {
		t.Fatalf("plaintext = %q, want a minted key", plaintext)
	}
	team, _ := created["team"].(map[string]any)
	if team["tenant_id"] != "acme" || team["kind"] != "team" || team["status"] != "active" {
		t.Fatalf("team = %v", team)
	}
	if _, ok := created["key"]; !ok {
		t.Fatal("response carries no key record")
	}
	// The minted key is a real credential for the team tenant.
	keys, err := fixture.tenants.ListKeys(context.Background(), "acme")
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v, want the issued one", keys, err)
	}

	// The team appears in the team list and not in the personal ones.
	list := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams", ""))
	teams, _ := list["teams"].([]any)
	if len(teams) != 1 {
		t.Fatalf("teams = %v, want one", teams)
	}
	tenantList := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/tenants?kind=personal", ""))
	for _, entry := range tenantList["tenants"].([]any) {
		if entry.(map[string]any)["tenant_id"] == "acme" {
			t.Fatal("the team is listed among the personal tenants")
		}
	}

	// Members: added, idempotent, and the role is updatable.
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{"role":"owner"}`); recorder.Code != http.StatusOK {
		t.Fatalf("add alice = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{"role":"member"}`); recorder.Code != http.StatusOK {
		t.Fatalf("re-add alice = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/bob", ``); recorder.Code != http.StatusOK {
		t.Fatalf("add bob = %d: %s", recorder.Code, recorder.Body.String())
	}
	members := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/members", ""))
	if got := len(members["members"].([]any)); got != 2 {
		t.Fatalf("members = %d, want 2", got)
	}

	// A tenant that is not active cannot join.
	fixture.seed(t, "carol", tenant.KindPersonal)
	suspended := tenant.StatusSuspended
	if _, err := fixture.tenants.UpdateTenant(context.Background(), "carol", storage.TenantUpdate{Status: &suspended}); err != nil {
		t.Fatalf("suspend carol: %v", err)
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/carol", `{}`); recorder.Code != http.StatusConflict {
		t.Fatalf("add suspended carol = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/ghost", `{}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("add unknown member = %d, want 404: %s", recorder.Code, recorder.Body.String())
	}

	// Projects: created with a computed bank id, and the id/name refused when bad.
	recorder = fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}
	project := decodeMap(t, recorder)
	if project["bank_id"] != "acme::Web" || project["project_id"] != "web" {
		t.Fatalf("project = %v", project)
	}
	for _, body := range []string{
		`{"project_id":"web2","name":"Web"}`,     // same name in the same team
		`{"project_id":"Web","name":"Other"}`,    // a capitalised id
		`{"project_id":"other","name":"a:b"}`,    // a colon would split the bank id
		`{"project_id":"web2","name":"Other"} }`, // two objects: malformed
	} {
		if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", body); recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusConflict {
			t.Fatalf("create project %s = %d, want 400 or 409: %s", body, recorder.Code, recorder.Body.String())
		}
	}

	// An existing bank under the project's id is a conflict unless adopted.
	other := newTeamFixture(t)
	other.banks.put("acme", "acme::Web")
	other.seed(t, "acme", tenant.KindTeam)
	if recorder := other.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("create over an existing bank = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := other.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web","adopt_bank":true}`); recorder.Code != http.StatusCreated {
		t.Fatalf("adopt existing bank = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}

	// Project members: only a team member can take part, repeats are fine.
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``); recorder.Code != http.StatusOK {
		t.Fatalf("add project member = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``); recorder.Code != http.StatusOK {
		t.Fatalf("re-add project member = %d: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/carol", ``); recorder.Code != http.StatusConflict {
		t.Fatalf("non-member on project = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	projectMembers := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/members", ""))
	if got := len(projectMembers["members"].([]any)); got != 1 {
		t.Fatalf("project members = %d, want 1", got)
	}

	// Rename: only display_name, and only display_name.
	if recorder := fixture.do(t, http.MethodPatch, "/admin/v1/teams/acme", `{"display_name":"Acme Team"}`); recorder.Code != http.StatusOK {
		t.Fatalf("rename = %d: %s", recorder.Code, recorder.Body.String())
	} else if decodeMap(t, recorder)["display_name"] != "Acme Team" {
		t.Fatalf("rename did not apply: %s", recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodPatch, "/admin/v1/teams/acme", `{"status":"deleted"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("patch status = %d, want 400: %s", recorder.Code, recorder.Body.String())
	}

	// Counts on the detail view.
	detail := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme", ""))
	if detail["member_count"].(float64) != 2 || detail["project_count"].(float64) != 1 {
		t.Fatalf("detail counts = %v", detail)
	}
}

func TestTeamMemberRemovalDropsProjectMembership(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.seed(t, "alice", tenant.KindPersonal)
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{}`); recorder.Code != http.StatusOK {
		t.Fatalf("add member = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``); recorder.Code != http.StatusOK {
		t.Fatalf("add project member = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove member = %d: %s", recorder.Code, recorder.Body.String())
	}
	members := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/members", ""))
	if got := len(members["members"].([]any)); got != 0 {
		t.Fatalf("project members = %d after leaving the team, want 0", got)
	}
}

func TestDeleteProjectDeletesItsBanks(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}
	// The project's own bank and a sibling under the same name. The unrelated
	// bank must survive.
	fixture.banks.put("acme", "acme::Web", "acme::Web.docs", "acme::Mobile")

	// Destructive deletes demand an explicit confirmation.
	for _, body := range []string{
		``,
		`{}`,
		`{"confirm":"web","delete_banks":false}`,
		`{"confirm":"wrong","delete_banks":true}`,
	} {
		if recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/projects/web", body); recorder.Code != http.StatusBadRequest {
			t.Fatalf("delete project with %q = %d, want 400: %s", body, recorder.Code, recorder.Body.String())
		}
	}

	recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/projects/web", `{"confirm":"web","delete_banks":true}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete project = %d: %s", recorder.Code, recorder.Body.String())
	}
	deleted, _ := decodeMap(t, recorder)["deleted_banks"].([]any)
	if len(deleted) != 2 {
		t.Fatalf("deleted banks = %v, want the project's two", deleted)
	}
	for _, bankID := range []string{"acme::Web", "acme::Web.docs"} {
		if !containsBank(fixture.banks.gone, bankID) {
			t.Fatalf("%s was not deleted", bankID)
		}
	}
	if containsBank(fixture.banks.gone, "acme::Mobile") {
		t.Fatal("an unrelated bank was deleted")
	}
	if recorder := fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("project still present = %d", recorder.Code)
	}
}

// TestDeleteProjectRefusesWhenTheRegistryAndSchemaDisagree pins the safety gate:
// a project row must not delete banks when the bank it names is not among the
// ones the prefix found, because that disagreement is exactly what would make a
// prefix delete take someone else's data.
func TestDeleteProjectRefusesWhenTheRegistryAndSchemaDisagree(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	fixture.banks.put("acme", "acme::Web.suffix")
	recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/projects/web", `{"confirm":"web","delete_banks":true}`)
	if recorder.Code != http.StatusConflict {
		t.Fatalf("delete = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if len(fixture.banks.gone) != 0 {
		t.Fatalf("banks were deleted anyway: %v", fixture.banks.gone)
	}
	if recorder := fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web", ""); recorder.Code != http.StatusOK {
		t.Fatalf("project row was removed despite the refusal: %d", recorder.Code)
	}
}

func TestPurgeContributionReportsNothingRebuilt(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.seed(t, "alice", tenant.KindPersonal)
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{}`); recorder.Code != http.StatusOK {
		t.Fatalf("add member = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``); recorder.Code != http.StatusOK {
		t.Fatalf("add project member = %d", recorder.Code)
	}
	recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects/web/purge-contribution", `{"tenant_id":"alice"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("purge = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeMap(t, recorder)
	if body["rebuild"] != false {
		t.Fatalf("rebuild = %v, want false until the merge job exists", body["rebuild"])
	}
	members := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/members", ""))
	if got := len(members["members"].([]any)); got != 0 {
		t.Fatalf("project members = %d after purge, want 0", got)
	}
}

func TestTeamRoutesRefusePersonalTenants(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "alice", tenant.KindPersonal)
	for _, route := range []struct{ method, path, body string }{
		{http.MethodGet, "/admin/v1/teams/alice", ""},
		{http.MethodGet, "/admin/v1/teams/alice/members", ""},
		{http.MethodGet, "/admin/v1/teams/alice/projects", ""},
		{http.MethodPatch, "/admin/v1/teams/alice", `{"display_name":"Nope"}`},
		{http.MethodDelete, "/admin/v1/teams/alice", ""},
	} {
		recorder := fixture.do(t, route.method, route.path, route.body)
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404: %s", route.method, route.path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestTeamSoftDeleteKeepsRows(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("delete team = %d: %s", recorder.Code, recorder.Body.String())
	}
	if decodeMap(t, recorder)["status"] != "deleted" {
		t.Fatalf("status = %s", recorder.Body.String())
	}
	projects := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects", ""))
	if got := len(projects["projects"].([]any)); got != 1 {
		t.Fatalf("projects after a soft delete = %d, want the row kept", got)
	}
}

// TestTeamKeysAreTenantKeys pins that a team's credentials go through the same
// handlers as a tenant's: a team is a tenant, so minting and revoking are not
// reimplemented, only reached under the team's path.
func TestTeamKeysAreTenantKeys(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.seed(t, "alice", tenant.KindPersonal)

	recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/keys", `{"label":"ci"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create team key = %d: %s", recorder.Code, recorder.Body.String())
	}
	plaintext, _ := decodeMap(t, recorder)["plaintext"].(string)
	if !strings.HasPrefix(plaintext, "wyk_") {
		t.Fatalf("plaintext = %q", plaintext)
	}
	keys, err := fixture.tenants.ListKeys(context.Background(), "acme")
	if err != nil || len(keys) != 1 {
		t.Fatalf("keys = %v, %v", keys, err)
	}
	list := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/keys", ""))
	if len(list["keys"].([]any)) != 1 {
		t.Fatalf("key list = %s", recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/keys/"+keys[0].ID, ""); recorder.Code != http.StatusOK {
		t.Fatalf("revoke team key = %d: %s", recorder.Code, recorder.Body.String())
	}

	// A personal tenant's keys are not reachable through the team path.
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/admin/v1/teams/alice/keys"},
		{http.MethodPost, "/admin/v1/teams/alice/keys"},
		{http.MethodDelete, "/admin/v1/teams/alice/keys/" + keys[0].ID},
	} {
		if recorder := fixture.do(t, route.method, route.path, `{}`); recorder.Code != http.StatusNotFound {
			t.Fatalf("%s %s = %d, want 404", route.method, route.path, recorder.Code)
		}
	}
}
