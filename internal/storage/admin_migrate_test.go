package storage

import (
	"strings"
	"testing"
)

func TestAdminMigrationSeries(t *testing.T) {
	series, err := AdminMigrationSeries()
	if err != nil {
		t.Fatalf("AdminMigrationSeries: %v", err)
	}
	if len(series) != 2 {
		t.Fatalf("series length = %d, want 2", len(series))
	}
	if series[0].Version != 1 || series[0].Name != "tenant_registry" {
		t.Fatalf("first migration = %04d_%s, want 0001_tenant_registry", series[0].Version, series[0].Name)
	}
	if series[1].Version != 2 || series[1].Name != "team_registry" {
		t.Fatalf("second migration = %04d_%s, want 0002_team_registry", series[1].Version, series[1].Name)
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
// states: the team tables exist, the merge ledger does not yet — a missing
// merge_state row means "never merged", so nothing in this series depends on
// it, and adding it later is a new migration rather than an edit to this one.
func TestTeamRegistryMigrationKeepsTheMergeLedgerOut(t *testing.T) {
	series, err := AdminMigrationSeries()
	if err != nil {
		t.Fatalf("AdminMigrationSeries: %v", err)
	}
	var body strings.Builder
	for _, migration := range series {
		body.WriteString(strings.Join(migration.Statements(), "\n"))
		body.WriteString("\n")
	}
	all := strings.Join(strings.Fields(body.String()), " ")
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
		if !strings.Contains(all, want) {
			t.Fatalf("admin series does not contain %q:\n%s", want, all)
		}
	}
	for _, unwanted := range []string{"merge_state", "merge_source_unit"} {
		if strings.Contains(all, unwanted) {
			t.Fatalf("admin series must not create %q yet:\n%s", unwanted, all)
		}
	}
}
