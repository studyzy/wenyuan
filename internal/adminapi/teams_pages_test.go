package adminapi

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// fakePageSeeder records what the plane asked it to seed.
type fakePageSeeder struct {
	mu      sync.Mutex
	schemas []string
	seeded  []string // "<bankID>/<project>"
	err     error
	calls   int
	onCalls func()
}

func (f *fakePageSeeder) SeedKnowledgePages(_ context.Context, schema, bankID, project string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	f.schemas = append(f.schemas, schema)
	f.seeded = append(f.seeded, bankID+"/"+project)
	if f.onCalls != nil {
		f.onCalls()
	}
	return f.err
}

// withPages returns the fixture with a page seeder wired, rebuilding the
// handler so the routes see it.
func (f teamFixture) withPages(t *testing.T, pages PageSeeder) teamFixture {
	t.Helper()
	handler, err := New(Config{
		Registry:    f.tenants,
		OperatorKey: testOperatorKey,
		TeamConfig: TeamConfig{
			Teams: f.teams, Banks: f.banks, Pages: pages,
			MembershipChanged: func(tenantID string) { *f.invalidated = append(*f.invalidated, tenantID) },
		},
	})
	if err != nil {
		t.Fatalf("New with pages: %v", err)
	}
	f.handler = handler
	return f
}

func TestCreateProjectSeedsTheNewBanksKnowledgePages(t *testing.T) {
	pages := &fakePageSeeder{}
	fixture := newTeamFixture(t).withPages(t, pages)
	fixture.seed(t, "acme", tenant.KindTeam)

	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects",
		`{"project_id":"web","name":"Web App"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("create project = %d: %s", recorder.Code, recorder.Body.String())
	}
	if len(pages.seeded) != 1 {
		t.Fatalf("seed calls = %v, want one", pages.seeded)
	}
	// The project's display name, not its id: the seeded questions name the
	// subject the pages are about, and the name is what a reader recognises.
	if pages.seeded[0] != "acme::web/Web App" {
		t.Errorf("seeded %q, want the project's bank and name", pages.seeded[0])
	}
	// The team's schema, the same one every other bank operation names: the
	// management plane's context carries no schema of its own.
	if len(pages.schemas) != 1 || pages.schemas[0] != "acme" {
		t.Errorf("schemas = %v, want the team's schema", pages.schemas)
	}
}

func TestCreateProjectDoesNotSeedAnAdoptedBank(t *testing.T) {
	pages := &fakePageSeeder{}
	fixture := newTeamFixture(t).withPages(t, pages)
	fixture.seed(t, "acme", tenant.KindTeam)
	fixture.banks.put("acme", "acme::web")

	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects",
		`{"project_id":"web","name":"Web","adopt_bank":true}`); recorder.Code != http.StatusCreated {
		t.Fatalf("adopt existing bank = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	if len(pages.seeded) != 0 {
		t.Errorf("an adopted bank was seeded: %v", pages.seeded)
	}
}

func TestCreateProjectSucceedsWhenSeedingFails(t *testing.T) {
	pages := &fakePageSeeder{err: errors.New("no model configured")}
	fixture := newTeamFixture(t).withPages(t, pages)
	fixture.seed(t, "acme", tenant.KindTeam)

	recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects",
		`{"project_id":"web","name":"Web"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("create project with a failing seeder = %d, want 201: %s", recorder.Code, recorder.Body.String())
	}
	// The project is real: it is listed and its bank exists, so the failure
	// cost only the pages.
	if got := fixture.do(t, http.MethodGet, "/admin/v1/teams/acme/projects", ""); got.Code != http.StatusOK {
		t.Fatalf("list projects = %d: %s", got.Code, got.Body.String())
	}
	if !containsBank(fixture.banks.created, "acme::web") {
		t.Errorf("the project's bank was rolled back")
	}
}

func TestCreateProjectSeedsOnlyAfterTheRegistryAcceptsIt(t *testing.T) {
	// A registry refusal drops the bank it just made, so seeding must not run
	// at all: pages written into a bank that is about to be deleted would be
	// work thrown away, and a bank left behind by a failed delete would carry
	// pages for a project that never was.
	pages := &fakePageSeeder{}
	fixture := newTeamFixture(t).withPages(t, pages)
	fixture.seed(t, "acme", tenant.KindTeam)
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects",
		`{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusCreated {
		t.Fatalf("first project = %d", recorder.Code)
	}
	pages.mu.Lock()
	pages.seeded = nil
	pages.mu.Unlock()
	// Same project id twice is refused by the registry.
	if recorder := fixture.do(t, http.MethodPost, "/admin/v1/teams/acme/projects",
		`{"project_id":"web","name":"Web"}`); recorder.Code != http.StatusConflict {
		t.Fatalf("duplicate project = %d, want 409: %s", recorder.Code, recorder.Body.String())
	}
	if len(pages.seeded) != 0 {
		t.Errorf("a refused project seeded pages: %v", pages.seeded)
	}
}
