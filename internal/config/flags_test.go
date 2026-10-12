package config

import (
	"os"
	"path/filepath"
	"testing"
)

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
func boolPtr(b bool) *bool    { return &b }

// ts-0031: listen address follows flag-over-environment-over-default.
func TestFlagOverridesEnvironment(t *testing.T) {
	base := FromEnv(envMap(map[string]string{EnvHost: "10.0.0.1", EnvPort: "7000"}))
	cfg := ApplyFlags(base, FlagValues{Host: strPtr("192.168.0.5"), Port: intPtr(7100)})
	if cfg.Host != "192.168.0.5" || cfg.Port != 7100 {
		t.Fatalf("flag must win over environment: %s:%d", cfg.Host, cfg.Port)
	}
}

func TestEnvironmentOverridesDefault(t *testing.T) {
	base := FromEnv(envMap(map[string]string{EnvHost: "10.0.0.1", EnvPort: "7000"}))
	cfg := ApplyFlags(base, FlagValues{})
	if cfg.Host != "10.0.0.1" || cfg.Port != 7000 {
		t.Fatalf("environment must win over default: %s:%d", cfg.Host, cfg.Port)
	}
}

func TestAbsentFlagsFallBackToDefault(t *testing.T) {
	cfg := ApplyFlags(FromEnv(envMap(nil)), FlagValues{})
	if cfg.Host != DefaultHost || cfg.Port != DefaultPort {
		t.Fatalf("defaults not applied: %s:%d", cfg.Host, cfg.Port)
	}
}

// sec-0001: daemon mode defaults the bind address to loopback.
func TestDaemonDefaultsToLoopback(t *testing.T) {
	cfg := ApplyFlags(FromEnv(envMap(nil)), FlagValues{Daemon: boolPtr(true)})
	if cfg.Host != DefaultHost {
		t.Fatalf("daemon host = %q, want loopback default", cfg.Host)
	}
}

func TestDaemonHonorsExplicitHost(t *testing.T) {
	cfg := ApplyFlags(FromEnv(envMap(nil)), FlagValues{Daemon: boolPtr(true), Host: strPtr("0.0.0.0")})
	if cfg.Host != "0.0.0.0" {
		t.Fatalf("explicit daemon host ignored: %q", cfg.Host)
	}
}

func TestServerFlagsSurface(t *testing.T) {
	cfg := ApplyFlags(FromEnv(envMap(nil)), FlagValues{
		LogLevel:     strPtr("debug"),
		Workers:      intPtr(4),
		AccessLog:    boolPtr(true),
		ProxyHeaders: boolPtr(true),
		SSLKeyFile:   strPtr("/tmp/key.pem"),
		SSLCertFile:  strPtr("/tmp/cert.pem"),
	})
	if cfg.LogLevel != "debug" || cfg.Workers != 4 || !cfg.AccessLog || !cfg.ProxyHeaders {
		t.Fatalf("server flags not applied: %+v", cfg)
	}
	if cfg.SSLKeyFile != "/tmp/key.pem" || cfg.SSLCertFile != "/tmp/cert.pem" {
		t.Fatalf("ssl files not applied: %+v", cfg)
	}
}

// in-0003: a dotenv file participates in configuration resolution.
func TestDotenvParticipatesInResolution(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\n" + EnvPort + "=8123\n" + EnvHost + "=\"127.0.0.9\"\nexport " + EnvMCPEnabled + "=true\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	key := EnvPort
	old, had := os.LookupEnv(key)
	if err := os.Unsetenv(key); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
		_ = os.Unsetenv(EnvHost)
		_ = os.Unsetenv(EnvMCPEnabled)
	})

	if err := LoadDotenvFile(path); err != nil {
		t.Fatal(err)
	}
	cfg := FromEnvironment()
	if cfg.Port != 8123 {
		t.Fatalf("dotenv port not applied: %d", cfg.Port)
	}
	if cfg.Host != "127.0.0.9" {
		t.Fatalf("quoted dotenv value not unquoted: %q", cfg.Host)
	}
	if !cfg.MCPEnabled {
		t.Fatalf("export-prefixed dotenv boolean not applied")
	}
}

func TestLoadDotenvMissingFileIsNotAnError(t *testing.T) {
	if err := LoadDotenvFile(filepath.Join(t.TempDir(), "does-not-exist")); err != nil {
		t.Fatalf("missing dotenv file must not error: %v", err)
	}
}

func TestLoadDotenvDoesNotOverwriteExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	if err := os.WriteFile(path, []byte(EnvPort+"=4444\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key := EnvPort
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, "5555"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})
	if err := LoadDotenvFile(path); err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv(key); got != "5555" {
		t.Fatalf("existing env must not be overwritten, got %q", got)
	}
}
