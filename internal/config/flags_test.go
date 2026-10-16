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
	if cfg.Host != DefaultDaemonHost {
		t.Fatalf("daemon host = %q, want loopback default", cfg.Host)
	}
	if cfg.Port != DefaultDaemonPort {
		t.Fatalf("daemon port = %d, want %d", cfg.Port, DefaultDaemonPort)
	}
}

// A port exported for foreground runs does not move the daemon: the reference
// resolves the daemon's port from its own default unless --port was passed.
func TestDaemonIgnoresConfiguredPort(t *testing.T) {
	base := FromEnv(envMap(map[string]string{EnvPort: "9999"}))
	cfg := ApplyFlags(base, FlagValues{Daemon: boolPtr(true)})
	if cfg.Port != DefaultDaemonPort {
		t.Fatalf("daemon port = %d, want %d despite %s", cfg.Port, DefaultDaemonPort, EnvPort)
	}
	// A host configured in the environment is explicit, so it survives.
	if cfg.Host != DefaultDaemonHost {
		t.Fatalf("daemon host = %q, want %q", cfg.Host, DefaultDaemonHost)
	}
	explicit := ApplyFlags(base, FlagValues{Daemon: boolPtr(true), Host: strPtr("0.0.0.0")})
	if explicit.Host != "0.0.0.0" {
		t.Fatalf("explicit daemon host ignored: %q", explicit.Host)
	}
	explicitPort := ApplyFlags(base, FlagValues{Daemon: boolPtr(true), Port: intPtr(9999)})
	if explicitPort.Port != 9999 {
		t.Fatalf("explicit daemon port ignored: %d", explicitPort.Port)
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

// The reference's own entrypoints treat a discovered .env as authoritative over
// the ambient process environment, so a later export cannot quietly undo the
// file a deployment shipped.
func TestLoadDotenvOverwritesExisting(t *testing.T) {
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
	if got := os.Getenv(key); got != "4444" {
		t.Fatalf("the dotenv value must win over the exported one, got %q", got)
	}
}

// Every entrypoint assembles its environment through LoadDotenvForEntrypoint:
// the conventional .env applies over the exported environment, and the YAML
// configuration file sits behind both.
func TestLoadDotenvForEntrypoint(t *testing.T) {
	unloadConfigFile(t)
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.MkdirAll(DefaultConfigDir, 0o755); err != nil {
		t.Fatal(err)
	}
	dotenv := "# only the worker knob\n" + EnvWorkerMaxRetries + "=9\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(dotenv), 0o600); err != nil {
		t.Fatal(err)
	}
	file := "port: 18901\n"
	if err := os.WriteFile(DefaultConfigPath(), []byte(file), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvConfigFile, "")

	key := EnvWorkerMaxRetries
	old, had := os.LookupEnv(key)
	if err := os.Setenv(key, "3"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if had {
			_ = os.Setenv(key, old)
		} else {
			_ = os.Unsetenv(key)
		}
	})

	if err := LoadDotenvForEntrypoint(); err != nil {
		t.Fatal(err)
	}
	cfg := FromEnvironment()
	if cfg.Worker.MaxRetries != 9 {
		t.Fatalf("the .env value must win over the exported %s, got %d", key, cfg.Worker.MaxRetries)
	}
	if cfg.Port != 18901 {
		t.Fatalf("the configuration file must answer behind the environment: %d", cfg.Port)
	}
	if got := LoadedConfigPath(); got != DefaultConfigPath() {
		t.Fatalf("LoadedConfigPath = %q, want %q", got, DefaultConfigPath())
	}
}
