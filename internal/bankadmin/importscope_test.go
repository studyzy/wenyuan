package bankadmin

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/tenant"
)

func TestResolveImportSchema(t *testing.T) {
	active := []tenant.Tenant{{ID: "studyzy", Schema: "studyzy"}}
	cases := []struct {
		name    string
		scope   ImportScope
		want    string
		wantErr string
	}{
		{
			name:  "single tenant uses the configured schema",
			scope: ImportScope{ConfiguredSchema: "public"},
			want:  "public",
		},
		{
			name:  "single tenant accepts its own name",
			scope: ImportScope{TenantID: "public", ConfiguredSchema: "public"},
			want:  "public",
		},
		{
			name:    "single tenant refuses another tenant",
			scope:   ImportScope{TenantID: "studyzy", ConfiguredSchema: "public"},
			wantErr: "not the schema this deployment serves",
		},
		{
			name:    "multi tenant requires the selection",
			scope:   ImportScope{MultiTenant: true, ActiveTenants: active},
			wantErr: "--tenant is required",
		},
		{
			name:  "multi tenant resolves the tenant schema",
			scope: ImportScope{MultiTenant: true, TenantID: "studyzy", ActiveTenants: active},
			want:  "studyzy",
		},
		{
			name:    "multi tenant refuses an unknown tenant",
			scope:   ImportScope{MultiTenant: true, TenantID: "nobody", ActiveTenants: active},
			wantErr: "not an active tenant",
		},
		{
			name:    "multi tenant refuses a conflicting schema override",
			scope:   ImportScope{MultiTenant: true, TenantID: "studyzy", Schema: "public", ActiveTenants: active},
			wantErr: "does not match tenant",
		},
		{
			name:  "multi tenant accepts a matching schema override",
			scope: ImportScope{MultiTenant: true, TenantID: "studyzy", Schema: "studyzy", ActiveTenants: active},
			want:  "studyzy",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := tc.scope.ResolveSchema()
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ResolveSchema() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ResolveSchema() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ResolveSchema() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestImportTargetBankID(t *testing.T) {
	cases := []struct {
		name     string
		source   string
		tenant   string
		override string
		want     string
		wantErr  string
	}{
		{
			name:   "re-homes the reference harness prefix under the tenant",
			source: "coding-agent::wenyuan",
			tenant: "studyzy",
			want:   "studyzy::wenyuan",
		},
		{
			name:     "an explicit target in the tenant is kept",
			source:   "coding-agent::wenyuan",
			tenant:   "studyzy",
			override: "studyzy::wenyuan",
			want:     "studyzy::wenyuan",
		},
		{
			name:     "an explicit target outside the tenant is refused",
			source:   "coding-agent::wenyuan",
			tenant:   "studyzy",
			override: "coding-agent::wenyuan",
			wantErr:  "must be scoped to tenant",
		},
		{
			name:   "a source without a prefix gets one",
			source: "wenyuan",
			tenant: "studyzy",
			want:   "studyzy::wenyuan",
		},
		{
			name:   "no tenant keeps the source id",
			source: "coding-agent::wenyuan",
			want:   "coding-agent::wenyuan",
		},
		{
			name:     "no tenant keeps an explicit target",
			source:   "coding-agent::wenyuan",
			override: "written",
			want:     "written",
		},
		{
			name:     "an invalid target is refused",
			source:   "coding-agent::wenyuan",
			tenant:   "studyzy",
			override: "studyzy::bad\x01name",
			wantErr:  "invalid bank id",
		},
		{
			name:    "an empty source cannot be re-homed",
			tenant:  "studyzy",
			wantErr: "cannot derive a target bank id",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ImportTargetBankID(tc.source, tc.tenant, tc.override)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("ImportTargetBankID() error = %v, want %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ImportTargetBankID() error = %v", err)
			}
			if got != tc.want {
				t.Fatalf("ImportTargetBankID() = %q, want %q", got, tc.want)
			}
		})
	}
}

// A tenant import renames the reference bank: the archive names
// coding-agent::wenyuan, and the copy lands under studyzy::wenyuan in the
// tenant's store.
func TestImportBankReHomesUnderTenant(t *testing.T) {
	ctx := context.Background()
	source := storage.NewMemoryStore("public")
	if _, err := source.CreateBank(ctx, storage.Bank{BankID: "coding-agent::wenyuan", Name: "coding-agent::wenyuan"}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.PutDocument(ctx, "coding-agent::wenyuan", "d1", storage.Document{ID: "d1", OriginalText: "Alice moved to Berlin."}); err != nil {
		t.Fatal(err)
	}
	if _, err := source.UpsertUnit(ctx, storage.MemoryUnit{
		ID: "u1", BankID: "coding-agent::wenyuan", Text: "Alice moved to Berlin", FactType: storage.TypeWorld,
		DocumentID: "d1", Metadata: map[string]any{},
	}); err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "bank.zip")
	if _, err := RunExportBank(ctx, source, ExportBankFlags{BankID: "coding-agent::wenyuan", Output: path}, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}

	target := storage.NewMemoryStore("studyzy")
	var out bytes.Buffer
	report, err := RunImportBank(ctx, target, nil, ImportBankFlags{Archive: path, Tenant: "studyzy"}, &out)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if report.TargetBankID != "studyzy::wenyuan" {
		t.Fatalf("target bank = %q, want studyzy::wenyuan", report.TargetBankID)
	}
	if _, err := target.GetBank(ctx, "studyzy::wenyuan"); err != nil {
		t.Fatalf("the re-homed bank must exist: %v", err)
	}
	if !strings.Contains(out.String(), `Imported bank "studyzy::wenyuan" into tenant "studyzy"`) {
		t.Fatalf("import out = %q", out.String())
	}
}
