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
	if len(series) != 1 {
		t.Fatalf("series length = %d, want 1", len(series))
	}
	if series[0].Version != 1 || series[0].Name != "tenant_registry" {
		t.Fatalf("first migration = %04d_%s, want 0001_tenant_registry", series[0].Version, series[0].Name)
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
