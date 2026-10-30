package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// fakeBanks is an in-memory BankAdmin: it holds one team's bank ids, records
// the ones it created and the ones it deleted.
type fakeBanks struct {
	mu      sync.Mutex
	banks   map[string][]string // schema -> bank ids
	created []string
	gone    []string
	// createErr, when set, is returned by CreateBank instead of creating.
	createErr error
}

func newFakeBanks() *fakeBanks { return &fakeBanks{banks: map[string][]string{}} }

func (f *fakeBanks) put(schema string, bankIDs ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.banks[schema] = append(f.banks[schema], bankIDs...)
}

func (f *fakeBanks) CreateBank(_ context.Context, schema, bankID, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.createErr != nil {
		return f.createErr
	}
	for _, id := range f.banks[schema] {
		if id == bankID {
			return storage.ErrBankExists
		}
	}
	f.banks[schema] = append(f.banks[schema], bankID)
	f.created = append(f.created, bankID)
	return nil
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
	// merges is set before any request when a test wants the merge plane
	// wired; nil leaves the routes answering as a deployment that cannot merge.
	merges MergeAdmin
	// invalidated records the tenants whose read cache the plane dropped. It is a
	// pointer because the handler holds a closure over it, and the fixture is
	// copied by value on return.
	invalidated *[]string
}

// fakeMergeAdmin records what the plane told the merge job.
type fakeMergeAdmin struct {
	cleared    []string
	rebuilds   []string
	resets     []string
	triggers   []string
	statusErr  error
	clearErr   error
	rebuildErr error
}

func (f *fakeMergeAdmin) ClearMemberProject(_ context.Context, teamID, projectID, tenantID string) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.cleared = append(f.cleared, teamID+"/"+projectID+"/"+tenantID)
	return nil
}

func (f *fakeMergeAdmin) RequestRebuild(_ context.Context, teamID, projectID string) error {
	if f.rebuildErr != nil {
		return f.rebuildErr
	}
	f.rebuilds = append(f.rebuilds, teamID+"/"+projectID)
	return nil
}

func (f *fakeMergeAdmin) TriggerMerge(_ context.Context, teamID, projectID string) {
	f.triggers = append(f.triggers, teamID+"/"+projectID)
}

func (f *fakeMergeAdmin) MergeStatus(_ context.Context, teamID, projectID string) (storage.MergeStatus, error) {
	if f.statusErr != nil {
		return storage.MergeStatus{}, f.statusErr
	}
	return storage.MergeStatus{ProjectID: projectID, BankID: teamID + "::" + projectID, Merged: true, TeamFacts: 3}, nil
}

func (f *fakeMergeAdmin) ResetMergeState(_ context.Context, teamID, projectID string) error {
	f.resets = append(f.resets, teamID+"/"+projectID)
	return nil
}

func newTeamFixture(t *testing.T) teamFixture {
	t.Helper()
	tenants := storage.NewMemoryTenantRegistry()
	teams := storage.NewMemoryTeamRegistry(tenants)
	banks := newFakeBanks()
	invalidated := &[]string{}
	fixture := teamFixture{tenants: tenants, teams: teams, banks: banks, invalidated: invalidated}
	handler, err := New(Config{
		Registry:    tenants,
		OperatorKey: testOperatorKey,
		TeamConfig: TeamConfig{
			Teams: teams, Banks: banks,
			MembershipChanged: func(tenantID string) {
				*invalidated = append(*invalidated, tenantID)
			},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fixture.handler = handler
	return fixture
}

// withMerges returns the fixture with a merge plane wired, rebuilding the handler
// so the routes see it. Requests must go through the returned fixture.
func (f teamFixture) withMerges(t *testing.T, merges MergeAdmin) teamFixture {
	t.Helper()
	handler, err := New(Config{
		Registry:    f.tenants,
		OperatorKey: testOperatorKey,
		TeamConfig: TeamConfig{
			Teams: f.teams, Banks: f.banks, Merges: merges,
			MembershipChanged: func(tenantID string) { *f.invalidated = append(*f.invalidated, tenantID) },
		},
	})
	if err != nil {
		t.Fatalf("New with merges: %v", err)
	}
	f.handler = handler
	f.merges = merges
	return f
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

	// Projects: created with a computed bank id, and that bank created with it.
	recorder = fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}
	project := decodeMap(t, recorder)
	if project["bank_id"] != "acme::web" || project["project_id"] != "web" {
		t.Fatalf("project = %v", project)
	}
	if !containsBank(fixture.banks.created, "acme::web") {
		t.Fatalf("project creation created no bank; created = %v", fixture.banks.created)
	}
	listed, err := fixture.banks.ListBanks(context.Background(), "acme", "acme::web")
	if err != nil || !containsBank(listed, "acme::web") {
		t.Fatalf("the project's bank is not in the team's schema: %v, %v", listed, err)
	}
	// A name is presentation only, so nothing it contains is refused: the colon
	// case below proves that, while the malformed body is still a bad request.
	for _, body := range []string{
		`{"project_id":"web2","name":"Web"}`,     // same name in the same team
		`{"project_id":"Web","name":"Other"}`,    // a capitalised id
		`{"project_id":"web2","name":"Other"} }`, // two objects: malformed
	} {
		if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", body); recorder.Code != http.StatusBadRequest && recorder.Code != http.StatusConflict {
			t.Fatalf("create project %s = %d, want 400 or 409: %s", body, recorder.Code, recorder.Body.String())
		}
	}

	// A bank already under the project's id is a conflict unless the caller adopts
	// it, and adopting hands that bank over instead of creating it again.
	//
	// The reachable case is a bank whose project row is gone: a project of the
	// team already owns the name, so the registry refuses any second project
	// with it — see the conflict just above.
	other := newTeamFixture(t)
	other.banks.put("acme", "acme::web")
	other.seed(t, "acme", tenant.KindTeam)
	if recorder := other.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("create over an existing bank = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := other.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web","adopt_bank":true}`); recorder.Code != http.StatusCreated {
		t.Fatalf("adopt existing bank = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	naming := 0
	for _, id := range other.banks.banks["acme"] {
		if id == "acme::web" {
			naming++
		}
	}
	if naming != 1 {
		t.Fatalf("adopting a bank created it again: %v", other.banks.banks["acme"])
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

// TestProjectNameIsFreeForm pins the name's role: it is a label a person reads, so
// any text is accepted — Chinese and colons included — while the bank id stays the
// project's own slug.
func TestProjectNameIsFreeForm(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	for _, tc := range []struct{ id, name string }{
		{"named", "a:b"},
		{"cn", "产品:Web 后端"},
		{"ja", "プロジェクト"},
		{"emoji", "🚀 rocket"},
	} {
		body := `{"project_id":"` + tc.id + `","name":"` + tc.name + `"}`
		if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", body); recorder.Code != http.StatusCreated {
			t.Fatalf("create project %s = %d, want 201: %s", body, recorder.Code, recorder.Body.String())
		}
		created := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/"+tc.id, ""))
		if created["name"] != tc.name {
			t.Fatalf("name = %v, want %q kept verbatim", created["name"], tc.name)
		}
		if created["bank_id"] != "acme::"+tc.id {
			t.Fatalf("bank_id = %v, want acme::%s from the id", created["bank_id"], tc.id)
		}
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
	// The project's bank exists already, since creating the project made it. A
	// sibling under the same name is added beside it; the unrelated bank must
	// survive.
	fixture.banks.put("acme", "acme::web.docs", "acme::Mobile")

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
	for _, bankID := range []string{"acme::web", "acme::web.docs"} {
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
	// The project's own bank goes missing while a bank under its name stays,
	// which is the state that must not be deleted through.
	if _, err := fixture.banks.DeleteBank(context.Background(), "acme", "acme::web"); err != nil {
		t.Fatalf("remove the project's bank: %v", err)
	}
	fixture.banks.gone = nil
	fixture.banks.put("acme", "acme::web.suffix")
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

// TestCreateProjectCreatesItsBank pins the default path: a project's bank
// "<team>::<name>" exists as soon as the project does, in the team's schema, so
// the team can write to it without a first-write side effect.
func TestCreateProjectCreatesItsBank(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}
	// The bank is in the team's schema, and only there.
	list, err := fixture.banks.ListBanks(context.Background(), "acme", "acme::")
	if err != nil {
		t.Fatalf("ListBanks: %v", err)
	}
	if len(list) != 1 || list[0] != "acme::web" {
		t.Fatalf("team schema banks = %v, want [acme::web]", list)
	}
	if foreign := fixture.banks.banks["other"]; len(foreign) != 0 {
		t.Fatalf("the bank landed outside the team's schema: %v", foreign)
	}
	// The project's status read agrees the bank exists.
	status := decodeMap(t, fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects/web/adopt-bank", `{}`))
	if status["exists"] != true || status["bank_id"] != "acme::web" {
		t.Fatalf("bank status = %v, want the created bank", status)
	}
}

// TestCreateProjectWithoutTheBankLeavesNoProject pins the failure path: a bank
// that could not be created is reported and no project row is recorded, so the
// registry never names a bank that is not there.
func TestCreateProjectWithoutTheBankLeavesNoProject(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.banks.createErr = errors.New("boom")
	recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`)
	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("create project over a failing bank = %d, want 500: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("project row survived a failed bank creation: %d", recorder.Code)
	}

	// The bank is refused when it appeared between the check and the insert:
	// that is the same conflict an existing bank gets, not a server fault.
	fixture.banks.createErr = storage.ErrBankExists
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("create project onto a racing bank = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if recorder := fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("project row survived an occupied bank: %d", recorder.Code)
	}
}

// TestCreateProjectRollsBackItsBankWhenTheRowIsRefused pins the compensation: a
// bank created for a project whose registry row is then refused is deleted
// again, because the row is the only thing that would name it.
//
// The refusal is the registry's own: a project id that is still being written
// collides with the row an earlier request left, and the bank that was created
// for it must not outlive the project that never was.
func TestCreateProjectRollsBackItsBankWhenTheRowIsRefused(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	// A project holds "web". A request for "web" again is refused by the
	// registry after its bank was created, because the bank check ran first and
	// found no bank under that id.
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create first project = %d: %s", recorder.Code, recorder.Body.String())
	}
	fixture.banks.mu.Lock()
	fixture.banks.created = nil
	fixture.banks.gone = nil
	fixture.banks.mu.Unlock()
	// A second project reusing the id would collide on the bank check first, so
	// the compensation is exercised the way it is reachable: the registry refuses
	// the row after the bank exists. That is arranged by making the registry
	// refuse, rather than by a name, because a name no longer contributes to an
	// id and so cannot create a distinct bank for a taken one.
	duplicate := tenant.TeamProject{TeamID: "acme", ProjectID: "rolling", Name: "Rolling"}
	if err := fixture.teams.CreateTeamProject(context.Background(), duplicate); err != nil {
		t.Fatalf("seed the taken project id: %v", err)
	}
	fixture.banks.mu.Lock()
	fixture.banks.created = nil
	fixture.banks.gone = nil
	fixture.banks.mu.Unlock()
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"rolling","name":"Whatever"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("create project with a taken id = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	fixture.banks.mu.Lock()
	created, gone := append([]string(nil), fixture.banks.created...), append([]string(nil), fixture.banks.gone...)
	fixture.banks.mu.Unlock()
	if !containsBank(gone, "acme::rolling") {
		t.Fatalf("the refused project's bank was left behind; created = %v gone = %v", created, gone)
	}
	if containsBank(gone, "acme::web") {
		t.Fatalf("the first project's bank was deleted: %v", gone)
	}
}

// TestTwoTeamsOwnASameNamedProject pins that a project's identity is per team:
// "Web" in acme and "Web" in beta are two projects with two banks, because the
// bank id carries the team prefix and every uniqueness is scoped to the team.
func TestTwoTeamsOwnASameNamedProject(t *testing.T) {
	fixture := newTeamFixture(t)
	for _, id := range []string{"acme", "beta"} {
		fixture.seed(t, id, tenant.KindTeam)
		if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/"+id+"/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
			t.Fatalf("create %s/web = %d: %s", id, recorder.Code, recorder.Body.String())
		}
	}
	// Each team's project names its own bank, and each bank was created once.
	for team, want := range map[string]string{"acme": "acme::web", "beta": "beta::web"} {
		project := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/"+team+"/projects/web", ""))
		if project["bank_id"] != want {
			t.Fatalf("%s bank_id = %v, want %s", team, project["bank_id"], want)
		}
	}
	if len(fixture.banks.created) != 2 {
		t.Fatalf("created banks = %v, want one per team", fixture.banks.created)
	}
}

// TestPurgeContributionIsRefusedWithoutMerging pins that a withdrawal the
// deployment cannot complete is refused rather than half-done: taking the member
// out and leaving their content in is the opposite of what the caller asked for.
func TestPurgeContributionIsRefusedWithoutMerging(t *testing.T) {
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
	if recorder.Code != http.StatusConflict {
		t.Fatalf("purge without merging = %d, want 409", recorder.Code)
	}
	// Refused before anything moved: the member is still on the project.
	members := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/members", ""))
	if got := len(members["members"].([]any)); got != 1 {
		t.Fatalf("project members = %d after a refused purge, want the member still there", got)
	}
}

// TestPurgeContributionRemovesAndRebuilds pins the withdrawable path: the member
// leaves, their ledger is cleared and a rebuild is requested, which is what makes
// their facts leave the team's content.
func TestPurgeContributionRemovesAndRebuilds(t *testing.T) {
	fixture := newTeamFixture(t).withMerges(t, &fakeMergeAdmin{})
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
	if body["rebuild"] != true {
		t.Fatalf("rebuild = %v, want true", body["rebuild"])
	}
	members := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/members", ""))
	if got := len(members["members"].([]any)); got != 0 {
		t.Fatalf("project members = %d after purge, want 0", got)
	}
	merges := fixture.merges.(*fakeMergeAdmin)
	if len(merges.cleared) != 1 || merges.cleared[0] != "acme/web/alice" {
		t.Fatalf("cleared = %v, want the member's ledger cleared", merges.cleared)
	}
	if len(merges.rebuilds) != 1 {
		t.Fatalf("rebuilds = %v, want one", merges.rebuilds)
	}
}

// TestRemovingAProjectMemberClearsTheLedger pins R6: the ledger's foreign keys do
// not reach team_project_member, so a removed member's rows must be cleared
// explicitly or the project would rebuild on every round.
func TestRemovingAProjectMemberClearsTheLedger(t *testing.T) {
	fixture := newTeamFixture(t).withMerges(t, &fakeMergeAdmin{})
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
	if recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/projects/web/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove = %d: %s", recorder.Code, recorder.Body.String())
	}
	merges := fixture.merges.(*fakeMergeAdmin)
	if len(merges.cleared) != 1 {
		t.Fatalf("cleared = %v, want the removed member's ledger cleared", merges.cleared)
	}
	if len(merges.rebuilds) != 0 {
		t.Fatalf("rebuilds = %v, want none: a plain removal keeps the merged content", merges.rebuilds)
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

// TestMergeStatusReportsTheProject pins the console's read: the route reports the
// merge state without changing anything.
func TestMergeStatusReportsTheProject(t *testing.T) {
	fixture := newTeamFixture(t).withMerges(t, &fakeMergeAdmin{})
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	recorder := fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/merge-status", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("merge-status = %d: %s", recorder.Code, recorder.Body.String())
	}
	body := decodeMap(t, recorder)
	if body["project_id"] != "web" {
		t.Fatalf("project_id = %v, want web", body["project_id"])
	}
	if body["merged"] != true {
		t.Fatalf("merged = %v, want true", body["merged"])
	}
	if body["team_facts"] != float64(3) {
		t.Fatalf("team_facts = %v, want 3", body["team_facts"])
	}
}

// TestMergeStatusIsRefusedWithoutMerging pins that a deployment that cannot merge
// says so rather than reporting an empty state that looks like a healthy project.
func TestMergeStatusIsRefusedWithoutMerging(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	recorder := fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/merge-status", "")
	if recorder.Code != http.StatusConflict {
		t.Fatalf("merge-status without merging = %d, want 409", recorder.Code)
	}
}

// TestMergeResetClearsTheBreaker pins the operator's way out of a halted project.
func TestMergeResetClearsTheBreaker(t *testing.T) {
	merges := &fakeMergeAdmin{}
	fixture := newTeamFixture(t).withMerges(t, merges)
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects/web/merge-reset", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("merge-reset = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(merges.resets) != 1 || merges.resets[0] != "acme/web" {
		t.Fatalf("resets = %v, want acme/web", merges.resets)
	}
}

// TestAddingAProjectMemberTriggersAMerge pins the on-demand trigger: a member
// added to a project asks for that project's round immediately, because the
// member's own bank is now an input and they expect their existing knowledge to
// appear in the project's shared content.
func TestAddingAProjectMemberTriggersAMerge(t *testing.T) {
	merges := &fakeMergeAdmin{}
	fixture := newTeamFixture(t).withMerges(t, merges)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.seed(t, "alice", tenant.KindPersonal)
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{}`); recorder.Code != http.StatusOK {
		t.Fatalf("add member = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	if len(merges.triggers) != 0 {
		t.Fatalf("merges triggered before any project member was added: %v", merges.triggers)
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``); recorder.Code != http.StatusOK {
		t.Fatalf("add project member = %d", recorder.Code)
	}
	if len(merges.triggers) != 1 || merges.triggers[0] != "acme/web" {
		t.Fatalf("triggers = %v, want acme/web", merges.triggers)
	}
}

// TestOnlyAddingAProjectMemberTriggersAMerge pins the rest of the lifecycle:
// removing a member and purging a contribution use their own paths (the ledger
// clear and the rebuild request), so they do not also fire the trigger.
func TestOnlyAddingAProjectMemberTriggersAMerge(t *testing.T) {
	merges := &fakeMergeAdmin{}
	fixture := newTeamFixture(t).withMerges(t, merges)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.seed(t, "alice", tenant.KindPersonal)
	fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{}`)
	fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`)
	fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``)
	if len(merges.triggers) != 1 {
		t.Fatalf("triggers = %v after adding, want 1", merges.triggers)
	}
	fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/projects/web/members/alice", "")
	fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``)
	if len(merges.triggers) != 2 {
		t.Fatalf("triggers = %v, want the removal to add none and the re-add one", merges.triggers)
	}
	fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects/web/purge-contribution", `{"tenant_id":"alice"}`)
	if len(merges.triggers) != 2 {
		t.Fatalf("triggers = %v after a purge; the purge asks for a rebuild instead", merges.triggers)
	}
	if len(merges.rebuilds) != 1 {
		t.Fatalf("rebuilds = %v, want the purge's one", merges.rebuilds)
	}
}

// TestAddingAProjectMemberWithoutMergingStillSucceeds pins that the trigger is a
// convenience: a deployment that cannot merge still records the membership.
func TestAddingAProjectMemberWithoutMergingStillSucceeds(t *testing.T) {
	fixture := newTeamFixture(t)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.seed(t, "alice", tenant.KindPersonal)
	fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{}`)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d", recorder.Code)
	}
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``); recorder.Code != http.StatusOK {
		t.Fatalf("add project member without merging = %d, want 200", recorder.Code)
	}
	members := decodeMap(t, fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects/web/members", ""))
	if got := len(members["members"].([]any)); got != 1 {
		t.Fatalf("project members = %d, want 1", got)
	}
}

// TestMembershipChangesInvalidateTheReadCache pins that every route which changes
// what a tenant may read tells the read path at once. Without it a member added to
// a project keeps reading their own bank until the cache's TTL expires, which looks
// like the feature simply not working.
func TestMembershipChangesInvalidateTheReadCache(t *testing.T) {
	merges := &fakeMergeAdmin{}
	fixture := newTeamFixture(t).withMerges(t, merges)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.seed(t, "alice", tenant.KindPersonal)
	fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/members/alice", `{}`)
	fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`)

	// Added to the project.
	if recorder := fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``); recorder.Code != http.StatusOK {
		t.Fatalf("add project member = %d", recorder.Code)
	}
	if len(*fixture.invalidated) != 1 || (*fixture.invalidated)[0] != "alice" {
		t.Fatalf("invalidated = %v after adding, want alice", *fixture.invalidated)
	}
	// Removed from the project.
	if recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/projects/web/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove project member = %d", recorder.Code)
	}
	if len(*fixture.invalidated) != 2 {
		t.Fatalf("invalidated = %v after removing, want a second entry", *fixture.invalidated)
	}
	// Removed from the team, which takes every project membership with it.
	fixture.do(t, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ``)
	if recorder := fixture.do(t, http.MethodDelete, "/admin/v1/teams/acme/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove team member = %d", recorder.Code)
	}
	if len(*fixture.invalidated) != 4 {
		t.Fatalf("invalidated = %v after leaving the team, want a fourth entry", *fixture.invalidated)
	}
}
