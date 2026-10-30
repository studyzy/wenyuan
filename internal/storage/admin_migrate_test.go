package storage

import (
	"slices"
	"strings"
	"testing"
)

func TestAdminMigrationSeries(t *testing.T) {
	series, err := AdminMigrationSeries()
	if err != nil {
		t.Fatalf("AdminMigrationSeries: %v", err)
	}
	if len(series) != 4 {
		t.Fatalf("series length = %d, want 4", len(series))
	}
	wantVersions := []struct {
		version int
		name    string
	}{
		{1, "tenant_registry"},
		{2, "team_registry"},
		{3, "merge_state"},
		{4, "project_name_is_a_label"},
	}
	for i, want := range wantVersions {
		if series[i].Version != want.version || series[i].Name != want.name {
			t.Fatalf("migration %d = %04d_%s, want %04d_%s",
				i, series[i].Version, series[i].Name, want.version, want.name)
		}
	}
	body := strings.Join(series[0].Statements(), "\n")
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS tenant",
		"CREATE TABLE IF NOT EXISTS tenant_key",
		"CREATE TABLE IF NOT EXISTS admin_audit",
		"REFERENCES tenant",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("migration body does not contain %q:\n%s", want, body)
		}
	}
}

// TestTeamRegistryMigrationKeepsTheMergeLedgerOut pins the split the plan
// states: the team registry is 0002 and the merge ledger is a separate 0003, so
// a deployment that already ran 0002 records exactly one new version rather than
// a changed one.
func TestTeamRegistryMigrationKeepsTheMergeLedgerOut(t *testing.T) {
	series, err := AdminMigrationSeries()
	if err != nil {
		t.Fatalf("AdminMigrationSeries: %v", err)
	}
	registry := strings.Join(series[1].Statements(), "\n")
	for _, want := range []string{
		"ADD COLUMN IF NOT EXISTS kind text NOT NULL DEFAULT 'personal'",
		"CREATE TABLE IF NOT EXISTS team_member",
		"CREATE TABLE IF NOT EXISTS team_project",
		"CREATE TABLE IF NOT EXISTS team_project_member",
		// A name carrying a colon would make its own bank ids unresolvable.
		"CHECK (name !~ ':')",
		// A member's project rows follow them out of the team.
		"FOREIGN KEY (team_tenant_id, tenant_id) REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE",
	} {
		if !strings.Contains(strings.Join(strings.Fields(registry), " "), want) {
			t.Fatalf("0002 does not contain %q:\n%s", want, registry)
		}
	}
	for _, unwanted := range []string{"merge_state", "merge_source_unit"} {
		if strings.Contains(registry, unwanted) {
			t.Fatalf("0002 must not create %q; it belongs to 0003:\n%s", unwanted, registry)
		}
	}
}

// TestMergeStateMigration pins the merge ledger's shape, including the two
// decisions that are load-bearing rather than cosmetic: a ledger row records the
// merge_key its source supports (so a folded-away source's deletion cannot force
// a rebuild), and merging_since carries the claim across the model call.
func TestMergeStateMigration(t *testing.T) {
	series, err := AdminMigrationSeries()
	if err != nil {
		t.Fatalf("AdminMigrationSeries: %v", err)
	}
	if len(series) < 3 {
		t.Fatalf("series length = %d, want at least 3", len(series))
	}
	ledger := strings.Join(strings.Fields(series[2].SQL), " ")
	for _, want := range []string{
		"CREATE TABLE IF NOT EXISTS merge_state",
		"CREATE TABLE IF NOT EXISTS merge_source_unit",
		"cursor_updated_at timestamptz NOT NULL DEFAULT '-infinity'",
		"cursor_unit_id uuid NOT NULL",
		"merging_since timestamptz",
		"fail_count integer NOT NULL DEFAULT 0",
		"rebuild_requested boolean NOT NULL DEFAULT false",
		"source_unit_id uuid NOT NULL",
		"merge_key text NOT NULL",
		"PRIMARY KEY (team_tenant_id, project_id, tenant_id, source_unit_id)",
		"REFERENCES team_project(team_tenant_id, project_id) ON DELETE CASCADE",
		"REFERENCES team_member(team_tenant_id, tenant_id) ON DELETE CASCADE",
	} {
		if !strings.Contains(ledger, want) {
			t.Fatalf("0003 does not contain %q:\n%s", want, ledger)
		}
	}
	// The delete-detection index excludes the folded-away rows, which is what
	// keeps that scan over the supporting sources rather than the whole ledger.
	if !strings.Contains(ledger, "WHERE merge_key <> '__dropped__'") {
		t.Fatalf("0003 has no partial index excluding dropped sources:\n%s", ledger)
	}
}

// TestAdminSeriesSkipsTeamTablesForSingleTenant pins the split a single-tenant
// deployment gets: the tenant registry only. Creating the team tables there would
// put team vocabulary in the schema of a deployment that never has a team.
func TestAdminSeriesSkipsTeamTablesForSingleTenant(t *testing.T) {
	full, err := AdminMigrationSeriesFor(true)
	if err != nil {
		t.Fatalf("AdminMigrationSeriesFor(multi): %v", err)
	}
	single, err := AdminMigrationSeriesFor(false)
	if err != nil {
		t.Fatalf("AdminMigrationSeriesFor(single): %v", err)
	}
	if len(full) != 4 {
		t.Fatalf("multi-tenant series length = %d, want 4", len(full))
	}
	if len(single) != 1 {
		t.Fatalf("single-tenant series length = %d, want 1", len(single))
	}
	if single[0].Version != 1 {
		t.Fatalf("single-tenant series starts at %d, want 1", single[0].Version)
	}
	var body strings.Builder
	for _, migration := range single {
		body.WriteString(migration.SQL)
	}
	for _, unwanted := range []string{"team_member", "team_project", "merge_state", "merge_source_unit"} {
		if strings.Contains(body.String(), unwanted) {
			t.Fatalf("single-tenant series must not mention %q:\n%s", unwanted, body.String())
		}
	}
	// The versions a multi-tenant deployment applies on top are exactly the team
	// ones, so an existing multi-tenant deployment upgrading sees only 0003.
	var versions []int
	for _, migration := range full {
		if !slices.ContainsFunc(single, func(m Migration) bool { return m.Version == migration.Version }) {
			versions = append(versions, migration.Version)
		}
	}
	if !slices.Equal(versions, []int{2, 3, 4}) {
		t.Fatalf("multi-tenant-only versions = %v, want [2 3 4]", versions)
	}
}
