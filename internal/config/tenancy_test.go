package config

import "testing"

func TestTenancyConfigDefaultsAndReads(t *testing.T) {
	cfg := FromEnv(envMap(nil))
	if cfg.TenantMode != TenantModeSingle {
		t.Fatalf("default tenant mode = %q, want %q", cfg.TenantMode, TenantModeSingle)
	}
	if cfg.AdminListen != "" || cfg.AdminAPIKey != "" {
		t.Fatalf("admin plane should be off by default: %+v", cfg)
	}

	cfg = FromEnv(envMap(map[string]string{
		EnvTenantMode:  "MULTI",
		EnvAdminListen: "127.0.0.1:18890",
		EnvAdminAPIKey: "operator-secret",
	}))
	if cfg.TenantMode != TenantModeMulti {
		t.Fatalf("tenant mode = %q, want %q", cfg.TenantMode, TenantModeMulti)
	}
	if cfg.AdminListen != "127.0.0.1:18890" || cfg.AdminAPIKey != "operator-secret" {
		t.Fatalf("admin config = %+v", cfg)
	}
}

func TestValidateTenancy(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
	}{
		{name: "defaults", cfg: Config{TenantMode: TenantModeSingle}},
		{name: "multi", cfg: Config{TenantMode: TenantModeMulti}},
		{name: "case insensitive", cfg: Config{TenantMode: "MULTI"}},
		{name: "unknown mode", cfg: Config{TenantMode: "shared"}, wantErr: true},
		{name: "listener without key", cfg: Config{TenantMode: TenantModeMulti, AdminListen: "127.0.0.1:18890"}, wantErr: true},
		{
			name: "listener with key",
			cfg:  Config{TenantMode: TenantModeMulti, AdminListen: "127.0.0.1:18890", AdminAPIKey: "secret"},
		},
		{
			name:    "listener with multiple workers",
			cfg:     Config{TenantMode: TenantModeMulti, AdminListen: "127.0.0.1:18890", AdminAPIKey: "secret", Workers: 2},
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.cfg.ValidateTenancy()
			if tc.wantErr {
				if err == nil {
					t.Fatal("ValidateTenancy = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("ValidateTenancy = %v, want nil", err)
			}
		})
	}
}
