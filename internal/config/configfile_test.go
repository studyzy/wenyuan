package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeConfigFile writes a configuration file and points the environment at it,
// which is how a deployment names a file that is not the conventional one.
func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "wenyuan.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	t.Setenv(EnvConfigFile, path)
	return path
}

// unloadConfigFile drops the package source a previous test loaded, so a file
// one test read can never answer a key another test resolves.
func unloadConfigFile(t *testing.T) {
	t.Helper()
	unload := func() {
		configSourceMu.Lock()
		configSource, configPath = nil, ""
		configSourceMu.Unlock()
	}
	unload()
	t.Cleanup(unload)
}

// The file supplies every key the resolver reads, whatever the YAML scalar
// type, and reports the path it came from.
func TestConfigFileSuppliesKeys(t *testing.T) {
	unloadConfigFile(t)
	path := writeConfigFile(t, "# a comment the loader ignores\n"+
		"host: 127.0.0.1\n"+
		"port: 18888\n"+
		"log_level: debug\n"+
		"access_log: true\n"+
		"recall_max_tokens: 4096\n")

	if err := LoadConfigFile(); err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if got := LoadedConfigPath(); got != path {
		t.Errorf("LoadedConfigPath = %q, want %q", got, path)
	}

	cfg := FromEnvironment()
	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host = %q, want 127.0.0.1", cfg.Host)
	}
	if cfg.Port != 18888 {
		t.Errorf("Port = %d, want 18888", cfg.Port)
	}
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel = %q, want debug", cfg.LogLevel)
	}
	if !cfg.AccessLog {
		t.Error("AccessLog = false, want true")
	}
	if cfg.ProcessDefaults.RecallMaxTokens != 4096 {
		t.Errorf("RecallMaxTokens = %d, want 4096", cfg.ProcessDefaults.RecallMaxTokens)
	}
}

// Resolution order, highest first: flag, process environment, file, default.
func TestConfigFilePrecedence(t *testing.T) {
	cases := []struct {
		name     string
		file     string
		env      string
		flag     *int
		wantPort int
	}{
		{name: "the default alone when nothing is set", wantPort: DefaultPort},
		{name: "the file over the default", file: "port: 18888\n", wantPort: 18888},
		{name: "the environment over the file", file: "port: 18888\n", env: "19999", wantPort: 19999},
		{name: "the environment over the default", env: "19999", wantPort: 19999},
		{name: "the flag over the environment and the file", file: "port: 18888\n", env: "19999", flag: intPtr(17777), wantPort: 17777},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unloadConfigFile(t)
			if tc.file != "" {
				writeConfigFile(t, tc.file)
			} else {
				// No file at all: the conventional path, in a directory that has none.
				t.Chdir(t.TempDir())
				t.Setenv(EnvConfigFile, "")
			}
			if tc.env != "" {
				t.Setenv(EnvPort, tc.env)
			}
			if err := LoadConfigFile(); err != nil {
				t.Fatalf("LoadConfigFile: %v", err)
			}

			cfg := FromEnvironment()
			if tc.flag != nil {
				cfg = ApplyFlags(cfg, FlagValues{Port: tc.flag})
			}
			if cfg.Port != tc.wantPort {
				t.Errorf("Port = %d, want %d", cfg.Port, tc.wantPort)
			}
		})
	}
}

// The conventional path needs no environment variable, and an absent one leaves
// the environment as the only source rather than failing startup.
func TestConfigFileDefaultPath(t *testing.T) {
	t.Run("read when present", func(t *testing.T) {
		unloadConfigFile(t)
		t.Chdir(t.TempDir())
		t.Setenv(EnvConfigFile, "")
		if err := os.MkdirAll(DefaultConfigDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(DefaultConfigPath(), []byte("port: 18888\n"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}

		if err := LoadConfigFile(); err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if got := FromEnvironment().Port; got != 18888 {
			t.Errorf("Port = %d, want 18888", got)
		}
	})

	t.Run("absent is not an error", func(t *testing.T) {
		unloadConfigFile(t)
		t.Chdir(t.TempDir())
		t.Setenv(EnvConfigFile, "")

		if err := LoadConfigFile(); err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if got := LoadedConfigPath(); got != "" {
			t.Errorf("LoadedConfigPath = %q, want empty", got)
		}
		if got := FromEnvironment().Port; got != DefaultPort {
			t.Errorf("Port = %d, want the default %d", got, DefaultPort)
		}
	})
}

// A file the operator named is read or startup fails: a named file that is
// missing, unparseable, or shaped so nothing could ever read it is an error,
// never a silent no-op.
func TestConfigFileRefusals(t *testing.T) {
	cases := []struct {
		name    string
		body    *string
		wantErr string
	}{
		{name: "a named file that does not exist", wantErr: "configuration file"},
		{name: "a malformed file", body: strPtr("port: [unclosed\n"), wantErr: "configuration file"},
		{name: "a list value", body: strPtr("mcp_enabled_tools:\n  - one\n"), wantErr: "cannot express"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			unloadConfigFile(t)
			if tc.body == nil {
				t.Setenv(EnvConfigFile, filepath.Join(t.TempDir(), "absent.yaml"))
			} else {
				writeConfigFile(t, *tc.body)
			}

			err := LoadConfigFile()
			if err == nil {
				t.Fatal("LoadConfigFile = nil, want an error")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to name %q", err, tc.wantErr)
			}
		})
	}
}

// An unknown key and a blank value are no-ops, exactly as they are in the
// environment: neither invents a value nor fails the load.
func TestConfigFileIgnoresUnknownKeysAndBlankValues(t *testing.T) {
	unloadConfigFile(t)
	writeConfigFile(t, "not_a_setting: 1\nport: \"\"\n")
	if err := LoadConfigFile(); err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}
	if got := FromEnvironment().Port; got != DefaultPort {
		t.Errorf("Port = %d, want the default %d", got, DefaultPort)
	}
}

// A key this build refuses is refused whether the environment or the file
// supplied it, so a file cannot smuggle in a setting that would be ignored.
// database_backend is value-conditional — postgresql is honoured — so the
// probe is a dialect the build genuinely refuses.
func TestConfigFileRefusedKeyFailsValidation(t *testing.T) {
	unloadConfigFile(t)
	writeConfigFile(t, "database_backend: oracle\n")
	if err := LoadConfigFile(); err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}

	err := FromEnvironment().ValidateUnsupported()
	if err == nil {
		t.Fatal("ValidateUnsupported = nil, want the key refused")
	}
	if !strings.Contains(err.Error(), EnvDatabaseBackend) {
		t.Errorf("error = %q, want it to name %s", err, EnvDatabaseBackend)
	}
}

// A nested key spelling is accepted and flattened to the flat key the resolver
// asks for, at any depth: llm: {provider: openai} answers WENYUAN_API_LLM_PROVIDER.
func TestConfigFileAcceptsNestedKeys(t *testing.T) {
	unloadConfigFile(t)
	writeConfigFile(t,
		"database:\n"+
			"  url: postgresql://user:pw@localhost:5432/nested\n"+
			"worker:\n"+
			"  max_retries: 7\n")
	if err := LoadConfigFile(); err != nil {
		t.Fatalf("LoadConfigFile: %v", err)
	}

	cfg := FromEnvironment()
	if cfg.DatabaseURL != "postgresql://user:pw@localhost:5432/nested" {
		t.Errorf("nested database.url not flattened: %q", cfg.DatabaseURL)
	}
	if cfg.Worker.MaxRetries != 7 {
		t.Errorf("nested worker.max_retries not flattened: %d", cfg.Worker.MaxRetries)
	}
}

// Both spellings of one key may appear, but they must agree: a disagreement is
// refused naming the key, and agreement resolves to the one value.
func TestConfigFileNestedFlatSpellingsMustAgree(t *testing.T) {
	t.Run("a disagreement is refused", func(t *testing.T) {
		unloadConfigFile(t)
		writeConfigFile(t, "worker:\n  max_retries: 7\nworker_max_retries: 9\n")
		err := LoadConfigFile()
		if err == nil {
			t.Fatal("LoadConfigFile = nil, want the conflicting spellings refused")
		}
		if !strings.Contains(err.Error(), "worker_max_retries") {
			t.Errorf("error = %q, want it to name the key", err)
		}
	})

	t.Run("agreement is accepted", func(t *testing.T) {
		unloadConfigFile(t)
		writeConfigFile(t, "worker:\n  max_retries: 7\nworker_max_retries: 7\n")
		if err := LoadConfigFile(); err != nil {
			t.Fatalf("LoadConfigFile: %v", err)
		}
		if got := FromEnvironment().Worker.MaxRetries; got != 7 {
			t.Errorf("Worker.MaxRetries = %d, want 7", got)
		}
	})
}

// The shipped template is a configuration file this build accepts: it exists,
// parses, and configures nothing while every line stays commented out. The
// template is checked in as etc/example.wenyuan.yaml; the real etc/wenyuan.yaml
// a deployment writes is not checked in.
func TestShippedConfigTemplateLoads(t *testing.T) {
	unloadConfigFile(t)
	shipped := filepath.Join("..", "..", "etc", "example.wenyuan.yaml")
	t.Setenv(EnvConfigFile, shipped)
	if err := LoadConfigFile(); err != nil {
		t.Fatalf("shipped %s: %v", shipped, err)
	}
	if got := LoadedConfigPath(); got != shipped {
		t.Errorf("LoadedConfigPath = %q, want the shipped template", got)
	}
}
