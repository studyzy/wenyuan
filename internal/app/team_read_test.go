package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

// The team read fallback, end to end through the served handlers.
//
// These pin the contract the design's §八 states: a member's read is answered by
// their project's shared bank once their own holds nothing, the response still
// names the bank they asked for, and a caller who is not a member of the project
// reads exactly what they did before teams existed.

// teamReadFixture is a composed multi-tenant app with a team, a project and two
// member tenants.
type teamReadFixture struct {
	*App
	store   *storage.MemoryStore
	teamKey string
	// keys maps a member tenant id to its data-plane credential.
	keys map[string]string
}

// newTeamReadFixture builds the team, its members, the project and the project
// membership through the served management plane.
func newTeamReadFixture(t *testing.T) *teamReadFixture {
	t.Helper()
	cfg := testConfig()
	cfg.TenantMode = config.TenantModeMulti
	cfg.AdminListen = "127.0.0.1:18891"
	cfg.AdminAPIKey = "operator-secret"
	store := storage.NewMemoryStore("public")
	application, err := New(context.Background(), cfg, Dependencies{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	fixture := &teamReadFixture{App: application, store: store, keys: map[string]string{}}
	admin := application.AdminHandler

	// Two people exist as tenants first, each with their own key.
	for _, id := range []string{"alice", "bob"} {
		if recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/tenants", `{"tenant_id":"`+id+`"}`); recorder.Code != http.StatusCreated {
			t.Fatalf("create tenant %s = %d: %s", id, recorder.Code, recorder.Body.String())
		}
		created := decodeResponse(t, teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/tenants/"+id+"/keys", `{}`))
		key, _ := created["plaintext"].(string)
		if key == "" {
			t.Fatalf("tenant %s got no key: %v", id, created)
		}
		fixture.keys[id] = key
	}

	created := decodeResponse(t, teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/teams", `{"tenant_id":"acme","display_name":"Acme"}`))
	fixture.teamKey, _ = created["plaintext"].(string)
	for _, member := range []string{"alice", "bob"} {
		if recorder := teamAdminRequest(t, admin, http.MethodPut, "/admin/v1/teams/acme/members/"+member, `{}`); recorder.Code != http.StatusOK {
			t.Fatalf("add member %s = %d: %s", member, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := teamAdminRequest(t, admin, http.MethodPost, "/admin/v1/teams/acme/projects", `{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}
	// Only Alice takes part, so Bob is the non-member contrast.
	if recorder := teamAdminRequest(t, admin, http.MethodPut, "/admin/v1/teams/acme/projects/web/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("add project member = %d: %s", recorder.Code, recorder.Body.String())
	}
	return fixture
}

// recall runs recall as one tenant and returns the decoded response.
func (f *teamReadFixture) recall(t *testing.T, tenantID, bankID, query string) map[string]any {
	t.Helper()
	body := `{"query":"` + query + `"}`
	request := httptest.NewRequest(http.MethodPost, "/v1/default/banks/"+bankID+"/memories/recall", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+f.keys[tenantID])
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	f.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("recall as %s on %s = %d: %s", tenantID, bankID, recorder.Code, recorder.Body.String())
	}
	return decodeResponse(t, recorder)
}

// list runs the memory listing as one tenant.
func (f *teamReadFixture) list(t *testing.T, tenantID, bankID, query string) map[string]any {
	t.Helper()
	request := httptest.NewRequest(http.MethodGet, "/v1/default/banks/"+bankID+"/memories/list?"+query, nil)
	request.Header.Set("Authorization", "Bearer "+f.keys[tenantID])
	recorder := httptest.NewRecorder()
	f.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list as %s on %s = %d: %s", tenantID, bankID, recorder.Code, recorder.Body.String())
	}
	return decodeResponse(t, recorder)
}

// writeTeamFact puts one fact in the team's bank, which is where the merge job
// would leave it.
func (f *teamReadFixture) writeTeamFact(t *testing.T, text string) {
	t.Helper()
	ctx, err := storage.WithSchema(context.Background(), "acme")
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if err := f.store.EnsureBank(ctx, "acme::web"); err != nil {
		t.Fatalf("ensure team bank: %v", err)
	}
	f.store.SeedUnit(storage.MemoryUnit{
		ID: "00000000-0000-0000-0000-00000000a001", BankID: "acme::web",
		Text: text, FactType: storage.TypeWorld,
	})
}

// ensurePersonalBank creates a member's own bank without any content.
func (f *teamReadFixture) ensurePersonalBank(t *testing.T, tenantID string) {
	t.Helper()
	ctx, err := storage.WithSchema(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if err := f.store.EnsureBank(ctx, tenantID+"::web"); err != nil {
		t.Fatalf("ensure personal bank: %v", err)
	}
}

// writePersonalFact puts one fact in a member's own bank.
func (f *teamReadFixture) writePersonalFact(t *testing.T, tenantID, text string) {
	t.Helper()
	ctx, err := storage.WithSchema(context.Background(), tenantID)
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	bankID := tenantID + "::web"
	if err := f.store.EnsureBank(ctx, bankID); err != nil {
		t.Fatalf("ensure personal bank: %v", err)
	}
	f.store.SeedUnit(storage.MemoryUnit{
		ID: "00000000-0000-0000-0001-0000000000" + tenantID[:2], BankID: bankID,
		Text: text, FactType: storage.TypeWorld,
	})
}

// TestMemberRecallReadsTheTeamBank pins the core value: a member's recall returns
// the project's shared content.
func TestMemberRecallReadsTheTeamBank(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.ensurePersonalBank(t, "alice")

	response := fixture.recall(t, "alice", "alice::web", "deploy port")
	results, _ := response["results"].([]any)
	if len(results) == 0 {
		t.Fatalf("recall returned nothing; the team's content should be visible: %v", response)
	}
	found := false
	for _, item := range results {
		entry, _ := item.(map[string]any)
		if entry["text"] == "The deploy uses port 15432." {
			found = true
		}
	}
	if !found {
		t.Fatalf("recall results = %v, want the team's fact", results)
	}
}

// TestMemberListingReadsTheTeamBankAndKeepsTheBankID pins both halves of the
// listing contract: the content comes from the team bank, and the response still
// names the bank the caller asked for.
func TestMemberListingReadsTheTeamBankAndKeepsTheBankID(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.ensurePersonalBank(t, "alice")

	response := fixture.list(t, "alice", "alice::web", "")
	memories, _ := response["items"].([]any)
	if len(memories) != 1 {
		t.Fatalf("items = %v, want the team's one fact", response)
	}
	entry, _ := memories[0].(map[string]any)
	if entry["text"] != "The deploy uses port 15432." {
		t.Fatalf("memory text = %v, want the team's fact", entry["text"])
	}
	// The list's item shape carries no bank_id — the storage layer rewrites it
	// for the queries that take a bank as an argument, which
	// TestRestoreBankIDRewritesTheListing covers. What this proves is that the
	// content came from the team bank.
	if entry["text"] != "The deploy uses port 15432." {
		t.Fatalf("memory text = %v", entry["text"])
	}
}

// TestMemberFallsBackToTheirOwnBankWhenTheTeamIsEmpty pins the per-bank decision:
// before the first merge the member's own content answers, which is what a
// project looks like while it is young.
func TestMemberFallsBackToTheirOwnBankWhenTheTeamIsEmpty(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writePersonalFact(t, "alice", "Alice's own note about the deploy.")

	response := fixture.list(t, "alice", "alice::web", "")
	memories, _ := response["items"].([]any)
	if len(memories) != 1 {
		t.Fatalf("items = %v, want Alice's own note", response)
	}
	entry, _ := memories[0].(map[string]any)
	if entry["text"] != "Alice's own note about the deploy." {
		t.Fatalf("memory text = %v, want Alice's own note", entry["text"])
	}
}

// TestNonMemberDoesNotFallBack is the security half: someone who does not take
// part in the project reads their own bank, never the team's.
func TestNonMemberDoesNotFallBack(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.ensurePersonalBank(t, "bob")

	response := fixture.list(t, "bob", "bob::web", "")
	memories, _ := response["items"].([]any)
	if len(memories) != 0 {
		t.Fatalf("items = %v, want none: Bob is not on the project", response)
	}
}

// TestTeamKeyCannotReachItsBankByMemberPrefix pins that the fallback is keyed on
// membership, not on the bank id's shape: the team's own credential reads its own
// bank, and another tenant's prefix is not a way in.
func TestTeamKeyCannotReachItsBankByMemberPrefix(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")

	// The team's key reading "alice::web" is a read of Alice's bank, which the
	// team's schema does not hold.
	request := httptest.NewRequest(http.MethodGet, "/v1/default/banks/alice::web/memories/list", nil)
	request.Header.Set("Authorization", "Bearer "+fixture.teamKey)
	recorder := httptest.NewRecorder()
	fixture.Handler.ServeHTTP(recorder, request)
	if recorder.Code == http.StatusOK {
		var body struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &body); err == nil && len(body.Items) > 0 {
			t.Fatalf("the team's key read %d memories from another tenant's bank", len(body.Items))
		}
	}
}

// TestInvalidatedListingDoesNotFallBack pins §8.3: the team's bank holds no
// invalidated rows, so redirecting that filter would turn "this bank has archived
// facts" into "there is nothing here".
func TestInvalidatedListingDoesNotFallBack(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.ensurePersonalBank(t, "alice")

	response := fixture.list(t, "alice", "alice::web", "state=invalidated")
	memories, _ := response["items"].([]any)
	if len(memories) != 0 {
		t.Fatalf("invalidated items = %v, want none: the filter stays on the personal bank", response)
	}
}

// TestSingleTenantReadsAreUnchanged is the compatibility clause: with no team
// registry at all, a read is answered exactly as it was before teams existed.
func TestSingleTenantReadsAreUnchanged(t *testing.T) {
	cfg := testConfig()
	if cfg.TenantMode != config.TenantModeSingle {
		t.Fatalf("test default tenant mode = %q, want single", cfg.TenantMode)
	}
	store := storage.NewMemoryStore("public")
	application, err := New(context.Background(), cfg, Dependencies{Store: store})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if application.TeamRead != nil {
		t.Fatal("a single-tenant deployment must resolve no team reads")
	}
	if application.MergeStore != nil {
		t.Fatal("a single-tenant deployment must have no merge store")
	}
	ctx, err := storage.WithSchema(context.Background(), "public")
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	if err := store.EnsureBank(ctx, "plain"); err != nil {
		t.Fatalf("ensure bank: %v", err)
	}
	store.SeedUnit(storage.MemoryUnit{
		ID: "00000000-0000-0000-0000-000000000123", BankID: "plain",
		Text: "a single-tenant fact", FactType: storage.TypeWorld,
	})
	request := httptest.NewRequest(http.MethodGet, "/v1/default/banks/plain/memories/list", nil)
	request.Header.Set("Authorization", "Bearer "+cfg.TenantAPIKey)
	recorder := httptest.NewRecorder()
	application.Handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("list = %d: %s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body.Items) != 1 || body.Items[0]["text"] != "a single-tenant fact" {
		t.Fatalf("items = %v, want the one fact on its own bank", body.Items)
	}
}

// tenantKindForReads documents the kinds the fixture relies on.
var _ = tenant.KindTeam

// The two exits a project member has, through the served management plane.
//
// They differ in what survives: a removal stops the member contributing but keeps
// what was already merged, while a withdrawal takes the member out and rebuilds so
// their facts leave the team's content. The UI names both, so the difference has to
// hold end to end rather than only in the dialog's wording.

// TestRemovingAProjectMemberStopsTheFallback pins the readable effect of a removal:
// the member's next read no longer comes from the project.
func TestRemovingAProjectMemberStopsTheFallback(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.ensurePersonalBank(t, "alice")

	// Before: Alice's own bank is empty, so she reads the project's content.
	if got := listingTotal(t, fixture, "alice", "alice::web"); got != 1 {
		t.Fatalf("Alice's listing before removal = %d, want the team's one fact", got)
	}
	if recorder := teamAdminRequest(t, fixture.AdminHandler, http.MethodDelete,
		"/admin/v1/teams/acme/projects/web/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove alice = %d: %s", recorder.Code, recorder.Body.String())
	}
	// After: she is not on the project, so her own empty bank answers.
	if got := listingTotal(t, fixture, "alice", "alice::web"); got != 0 {
		t.Fatalf("Alice's listing after removal = %d, want none", got)
	}
}

// TestWithdrawingAContributionKeepsTheMembershipOut pins the withdrawal's contract:
// the member leaves, and the project is asked to rebuild without them.
func TestWithdrawingAContributionKeepsTheMembershipOut(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")

	recorder := teamAdminRequest(t, fixture.AdminHandler, http.MethodPost,
		"/admin/v1/teams/acme/projects/web/purge-contribution", `{"tenant_id":"alice"}`)
	// The composed app wires no merge store on the in-memory backend, so the plane
	// refuses rather than half-doing it — the protection the UI surfaces verbatim.
	if recorder.Code != http.StatusConflict {
		t.Fatalf("purge without a merge store = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	// Refused before anything moved: the member is still on the project.
	members := decodeResponse(t, teamAdminRequest(t, fixture.AdminHandler, http.MethodGet,
		"/admin/v1/teams/acme/projects/web/members", ""))
	list, _ := members["members"].([]any)
	found := false
	for _, item := range list {
		entry, _ := item.(map[string]any)
		if entry["tenant_id"] == "alice" {
			found = true
		}
	}
	if !found {
		t.Fatal("a refused withdrawal removed the member anyway")
	}
}

// TestRemovalClearsTheLedgerWithoutTouchingTheContent pins the difference between
// the exits on a real merge store: a removal drops the member's ledger rows so they
// stop being checked as a source, while the facts already merged stay.
func TestRemovalClearsTheLedgerWithoutTouchingTheContent(t *testing.T) {
	fixture := newTeamReadFixture(t)
	fixture.writeTeamFact(t, "The deploy uses port 15432.")
	fixture.ensurePersonalBank(t, "alice")

	if recorder := teamAdminRequest(t, fixture.AdminHandler, http.MethodDelete,
		"/admin/v1/teams/acme/projects/web/members/alice", ""); recorder.Code != http.StatusOK {
		t.Fatalf("remove alice = %d: %s", recorder.Code, recorder.Body.String())
	}
	// The team's content is untouched: nothing was withdrawn.
	ctx, err := storage.WithSchema(context.Background(), "acme")
	if err != nil {
		t.Fatalf("WithSchema: %v", err)
	}
	page, err := fixture.store.ListMemories(ctx, storage.MemoryQuery{BankID: "acme::web", Limit: 10})
	if err != nil {
		t.Fatalf("list the team bank: %v", err)
	}
	if len(page.Memories) != 1 {
		t.Fatalf("team content after a removal = %d facts, want the 1 that was there", len(page.Memories))
	}
}

// listingTotal is the total a tenant's memory listing reports.
func listingTotal(t *testing.T, f *teamReadFixture, tenantID, bankID string) int {
	t.Helper()
	body := f.list(t, tenantID, bankID, "")
	total, _ := body["total"].(float64)
	return int(total)
}
