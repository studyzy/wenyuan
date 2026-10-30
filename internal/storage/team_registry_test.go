package storage

import (
	"context"
	"errors"
	"testing"

	"github.com/studyzy/wenyuan/internal/tenant"
)

// teamRegistryFixture is what one backend has to provide so the shared suite can
// run against it: the team registry, and the tenant registry it shares its rows
// with.
type teamRegistryFixture struct {
	teams   TeamRegistry
	tenants TenantRegistry
}

// newMemoryTeamFixture builds the in-memory pair, which shares one tenant map.
func newMemoryTeamFixture() teamRegistryFixture {
	tenants := NewMemoryTenantRegistry()
	return teamRegistryFixture{teams: NewMemoryTeamRegistry(tenants), tenants: tenants}
}

// seedTenant creates an active tenant so team operations have something to
// point at.
func seedTenant(t *testing.T, fixture teamRegistryFixture, id string, kind tenant.Kind) tenant.Record {
	t.Helper()
	ctx := context.Background()
	record := tenant.Record{ID: id, DisplayName: id, Status: tenant.StatusActive, Kind: kind}
	if err := fixture.tenants.CreateTenant(ctx, record); err != nil {
		t.Fatalf("CreateTenant(%s): %v", id, err)
	}
	return record
}

// runTeamRegistrySuite exercises the whole TeamRegistry contract. Each backend
// runs it against its own storage, so a divergence between the in-memory and
// PostgreSQL implementations fails here rather than in production.
func runTeamRegistrySuite(t *testing.T, fixture teamRegistryFixture) {
	ctx := context.Background()
	seedTenant(t, fixture, "acme", tenant.KindTeam)
	seedTenant(t, fixture, "alice", tenant.KindPersonal)
	seedTenant(t, fixture, "bob", tenant.KindPersonal)

	t.Run("list only teams", func(t *testing.T) {
		teams, err := fixture.teams.ListTeams(ctx, TenantFilter{})
		if err != nil {
			t.Fatalf("ListTeams: %v", err)
		}
		if len(teams) != 1 || teams[0].ID != "acme" {
			t.Fatalf("teams = %+v, want only acme", teams)
		}
		if !teams[0].IsTeam() {
			t.Fatalf("acme kind = %q, want team", teams[0].Kind)
		}
	})

	t.Run("a personal tenant is not a team", func(t *testing.T) {
		if _, err := fixture.teams.GetTeam(ctx, "alice"); !errors.Is(err, tenant.ErrKindMismatch) {
			t.Fatalf("GetTeam(alice) = %v, want ErrKindMismatch", err)
		}
		if _, err := fixture.teams.GetTeam(ctx, "ghost"); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("GetTeam(ghost) = %v, want ErrNotFound", err)
		}
	})

	t.Run("members", func(t *testing.T) {
		if err := fixture.teams.AddTeamMember(ctx, "acme", "alice", tenant.RoleOwner); err != nil {
			t.Fatalf("AddTeamMember(alice): %v", err)
		}
		// Idempotent: a second add updates the role rather than failing.
		if err := fixture.teams.AddTeamMember(ctx, "acme", "alice", tenant.RoleMember); err != nil {
			t.Fatalf("AddTeamMember(alice) again: %v", err)
		}
		if err := fixture.teams.AddTeamMember(ctx, "acme", "bob", tenant.RoleMember); err != nil {
			t.Fatalf("AddTeamMember(bob): %v", err)
		}
		members, err := fixture.teams.ListTeamMembers(ctx, "acme")
		if err != nil {
			t.Fatalf("ListTeamMembers: %v", err)
		}
		if len(members) != 2 {
			t.Fatalf("members = %+v, want alice and bob", members)
		}
		if members[0].TenantID != "alice" || members[0].Role != tenant.RoleMember {
			t.Fatalf("alice row = %+v, want member role", members[0])
		}
		if members[0].DisplayName == "" {
			t.Fatal("member display name was not filled")
		}

		if err := fixture.teams.AddTeamMember(ctx, "acme", "ghost", tenant.RoleMember); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("AddTeamMember(ghost) = %v, want ErrNotFound", err)
		}
		if err := fixture.teams.AddTeamMember(ctx, "nobody", "alice", tenant.RoleMember); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("AddTeamMember(unknown team) = %v, want ErrNotFound", err)
		}
		if err := fixture.teams.AddTeamMember(ctx, "alice", "bob", tenant.RoleMember); !errors.Is(err, tenant.ErrKindMismatch) {
			t.Fatalf("AddTeamMember(personal team) = %v, want ErrKindMismatch", err)
		}
	})

	t.Run("a suspended member is refused", func(t *testing.T) {
		seedTenant(t, fixture, "carol", tenant.KindPersonal)
		suspended := tenant.StatusSuspended
		if _, err := fixture.tenants.UpdateTenant(ctx, "carol", TenantUpdate{Status: &suspended}); err != nil {
			t.Fatalf("suspend carol: %v", err)
		}
		if err := fixture.teams.AddTeamMember(ctx, "acme", "carol", tenant.RoleMember); !errors.Is(err, tenant.ErrInvalidTransition) {
			t.Fatalf("AddTeamMember(carol) = %v, want ErrInvalidTransition", err)
		}
	})

	t.Run("projects", func(t *testing.T) {
		project := tenant.TeamProject{TeamID: "acme", ProjectID: "web", Name: "Web"}
		if err := fixture.teams.CreateTeamProject(ctx, project); err != nil {
			t.Fatalf("CreateTeamProject: %v", err)
		}
		created, err := fixture.teams.GetTeamProject(ctx, "acme", "web")
		if err != nil {
			t.Fatalf("GetTeamProject: %v", err)
		}
		if created.BankID != "acme::Web" {
			t.Fatalf("bank_id = %q, want acme::Web", created.BankID)
		}
		// The same project id or name cannot be reused inside one team.
		if err := fixture.teams.CreateTeamProject(ctx, tenant.TeamProject{TeamID: "acme", ProjectID: "web2", Name: "Web"}); !errors.Is(err, tenant.ErrExists) {
			t.Fatalf("duplicate name = %v, want ErrExists", err)
		}
		if err := fixture.teams.CreateTeamProject(ctx, tenant.TeamProject{TeamID: "acme", ProjectID: "web", Name: "Web2"}); !errors.Is(err, tenant.ErrExists) {
			t.Fatalf("duplicate id = %v, want ErrExists", err)
		}
		// A bad name and a bad id are refused before anything is written.
		if err := fixture.teams.CreateTeamProject(ctx, tenant.TeamProject{TeamID: "acme", ProjectID: "bad", Name: "a:b"}); !errors.Is(err, tenant.ErrInvalidProject) {
			t.Fatalf("colon name = %v, want ErrInvalidProject", err)
		}
		// A personal tenant is not a project owner.
		if err := fixture.teams.CreateTeamProject(ctx, tenant.TeamProject{TeamID: "alice", ProjectID: "web", Name: "Web"}); !errors.Is(err, tenant.ErrKindMismatch) {
			t.Fatalf("personal owner = %v, want ErrKindMismatch", err)
		}
	})

	t.Run("two teams own a same-named project", func(t *testing.T) {
		seedTenant(t, fixture, "beta", tenant.KindTeam)
		if err := fixture.teams.CreateTeamProject(ctx, tenant.TeamProject{TeamID: "beta", ProjectID: "web", Name: "Web"}); err != nil {
			t.Fatalf("CreateTeamProject(beta/web): %v", err)
		}
		betaProject, err := fixture.teams.GetTeamProject(ctx, "beta", "web")
		if err != nil {
			t.Fatalf("GetTeamProject(beta/web): %v", err)
		}
		if betaProject.BankID != "beta::Web" || betaProject.BankID == "acme::Web" {
			t.Fatalf("beta bank_id = %q, want its own bank", betaProject.BankID)
		}
	})

	t.Run("project members", func(t *testing.T) {
		if err := fixture.teams.AddProjectMember(ctx, "acme", "web", "alice"); err != nil {
			t.Fatalf("AddProjectMember(alice): %v", err)
		}
		// Repeat adds are idempotent.
		if err := fixture.teams.AddProjectMember(ctx, "acme", "web", "alice"); err != nil {
			t.Fatalf("AddProjectMember(alice) again: %v", err)
		}
		members, err := fixture.teams.ListProjectMembers(ctx, "acme", "web")
		if err != nil {
			t.Fatalf("ListProjectMembers: %v", err)
		}
		if len(members) != 1 || members[0].TenantID != "alice" {
			t.Fatalf("project members = %+v, want alice", members)
		}
		// bob is on the team but not yet on the project.
		if err := fixture.teams.AddProjectMember(ctx, "acme", "web", "bob"); err != nil {
			t.Fatalf("AddProjectMember(bob): %v", err)
		}
		// carol is not on the team, so she cannot take part in its project.
		if err := fixture.teams.AddProjectMember(ctx, "acme", "web", "carol"); !errors.Is(err, tenant.ErrInvalidTransition) {
			t.Fatalf("AddProjectMember(outsider) = %v, want ErrInvalidTransition", err)
		}
		if err := fixture.teams.AddProjectMember(ctx, "acme", "missing", "alice"); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("AddProjectMember(missing project) = %v, want ErrNotFound", err)
		}
		project, err := fixture.teams.GetTeamProject(ctx, "acme", "web")
		if err != nil {
			t.Fatalf("GetTeamProject: %v", err)
		}
		if project.MemberCount != 2 {
			t.Fatalf("member count = %d, want 2", project.MemberCount)
		}

		if err := fixture.teams.RemoveProjectMember(ctx, "acme", "web", "bob"); err != nil {
			t.Fatalf("RemoveProjectMember(bob): %v", err)
		}
		if err := fixture.teams.RemoveProjectMember(ctx, "acme", "web", "bob"); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("RemoveProjectMember(bob) again = %v, want ErrNotFound", err)
		}
	})

	t.Run("leaving the team removes project membership", func(t *testing.T) {
		if err := fixture.teams.RemoveTeamMember(ctx, "acme", "alice"); err != nil {
			t.Fatalf("RemoveTeamMember(alice): %v", err)
		}
		members, err := fixture.teams.ListProjectMembers(ctx, "acme", "web")
		if err != nil {
			t.Fatalf("ListProjectMembers: %v", err)
		}
		for _, member := range members {
			if member.TenantID == "alice" {
				t.Fatal("alice is still on the project after leaving the team")
			}
		}
		if err := fixture.teams.RemoveTeamMember(ctx, "acme", "alice"); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("RemoveTeamMember(alice) again = %v, want ErrNotFound", err)
		}
		// Re-joining does not resurrect the project membership.
		if err := fixture.teams.AddTeamMember(ctx, "acme", "alice", tenant.RoleMember); err != nil {
			t.Fatalf("re-join alice: %v", err)
		}
		members, err = fixture.teams.ListProjectMembers(ctx, "acme", "web")
		if err != nil {
			t.Fatalf("ListProjectMembers: %v", err)
		}
		for _, member := range members {
			if member.TenantID == "alice" {
				t.Fatal("alice's project membership came back on re-join")
			}
		}
	})

	t.Run("rename and counts", func(t *testing.T) {
		team, err := fixture.teams.UpdateTeamName(ctx, "acme", "Acme Team")
		if err != nil {
			t.Fatalf("UpdateTeamName: %v", err)
		}
		if team.DisplayName != "Acme Team" {
			t.Fatalf("display name = %q", team.DisplayName)
		}
		if team.MemberCount != 2 {
			t.Fatalf("member count = %d, want 2 (alice, bob)", team.MemberCount)
		}
		if team.ProjectCount != 1 {
			t.Fatalf("project count = %d, want 1", team.ProjectCount)
		}
		if _, err := fixture.teams.UpdateTeamName(ctx, "alice", "Nope"); !errors.Is(err, tenant.ErrKindMismatch) {
			t.Fatalf("UpdateTeamName(personal) = %v, want ErrKindMismatch", err)
		}
	})

	t.Run("soft delete keeps the rows", func(t *testing.T) {
		team, err := fixture.teams.SoftDeleteTeam(ctx, "beta")
		if err != nil {
			t.Fatalf("SoftDeleteTeam: %v", err)
		}
		if team.Status != tenant.StatusDeleted {
			t.Fatalf("status = %q, want deleted", team.Status)
		}
		// Repeating the delete is a no-op, not an error.
		again, err := fixture.teams.SoftDeleteTeam(ctx, "beta")
		if err != nil {
			t.Fatalf("SoftDeleteTeam again: %v", err)
		}
		if again.Status != tenant.StatusDeleted {
			t.Fatalf("status = %q, want deleted", again.Status)
		}
		projects, err := fixture.teams.ListTeamProjects(ctx, "beta")
		if err != nil {
			t.Fatalf("ListTeamProjects(beta): %v", err)
		}
		if len(projects) != 1 {
			t.Fatalf("projects = %+v, want the row kept", projects)
		}
	})

	t.Run("deleting a project drops its membership", func(t *testing.T) {
		if err := fixture.teams.DeleteTeamProject(ctx, "acme", "web"); err != nil {
			t.Fatalf("DeleteTeamProject: %v", err)
		}
		if _, err := fixture.teams.GetTeamProject(ctx, "acme", "web"); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("GetTeamProject after delete = %v, want ErrNotFound", err)
		}
		if err := fixture.teams.DeleteTeamProject(ctx, "acme", "web"); !errors.Is(err, tenant.ErrNotFound) {
			t.Fatalf("DeleteTeamProject again = %v, want ErrNotFound", err)
		}
	})
}

func TestMemoryTeamRegistry(t *testing.T) {
	runTeamRegistrySuite(t, newMemoryTeamFixture())
}

// TestMemoryTeamRegistrySharesTenants proves the team registry is not a second
// tenant store: a tenant created through the tenant registry is the same row
// the team registry reasons about.
func TestMemoryTeamRegistrySharesTenants(t *testing.T) {
	ctx := context.Background()
	fixture := newMemoryTeamFixture()
	seedTenant(t, fixture, "acme", tenant.KindTeam)

	records, err := fixture.tenants.ListTenants(ctx, TenantFilter{Kind: tenant.KindTeam})
	if err != nil {
		t.Fatalf("ListTenants: %v", err)
	}
	if len(records) != 1 || records[0].ID != "acme" {
		t.Fatalf("team tenants = %+v, want acme", records)
	}
	if team, err := fixture.teams.GetTeam(ctx, "acme"); err != nil || team.Kind != tenant.KindTeam {
		t.Fatalf("GetTeam(acme) = %+v, %v", team, err)
	}
}

// TestMemoryTenantKindIsImmutable pins that a tenant's kind is fixed at
// creation: an existing personal tenant is never promoted to a team.
func TestMemoryTenantKindIsImmutable(t *testing.T) {
	ctx := context.Background()
	registry := NewMemoryTenantRegistry()
	if err := registry.CreateTenant(ctx, tenant.Record{ID: "alice", DisplayName: "Alice", Status: tenant.StatusActive}); err != nil {
		t.Fatalf("CreateTenant: %v", err)
	}
	if got, _ := registry.GetTenant(ctx, "alice"); got.Kind != tenant.KindPersonal {
		t.Fatalf("kind = %q, want personal", got.Kind)
	}
	promoted := tenant.KindTeam
	if _, err := registry.UpdateTenant(ctx, "alice", TenantUpdate{Kind: &promoted}); !errors.Is(err, tenant.ErrInvalidTransition) {
		t.Fatalf("promote to team = %v, want ErrInvalidTransition", err)
	}
	// Restating the current kind is accepted, so a read-modify-write works.
	same := tenant.KindPersonal
	if _, err := registry.UpdateTenant(ctx, "alice", TenantUpdate{Kind: &same}); err != nil {
		t.Fatalf("restating kind = %v", err)
	}
}
