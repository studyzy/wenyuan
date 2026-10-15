package cli

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfig(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantURL string
		wantKey string
	}{
		{"bare", "api_url = http://a\n", "http://a", ""},
		{"double quoted", "api_url = \"http://a\"\n", "http://a", ""},
		{"single quoted", "api_url = 'http://a'\n", "http://a", ""},
		{"repeated quotes", `api_url = ""http://a""` + "\n", "http://a", ""},
		// Quote stripping is one kind at a time: the inner double quotes
		// survive after the outer single quotes are trimmed.
		{"mixed quote kinds", `api_url = '"http://a"'` + "\n", "\"http://a\"", ""},
		{"base64 padding survives", "api_key = dGhpcw==\n", "", "dGhpcw=="},
		{"equals inside value", "api_key = a=b=c\n", "", "a=b=c"},
		{"no spaces", "api_url=http://a\napi_key=k\n", "http://a", "k"},
		{"extra whitespace", "  api_url   =   http://a  \n", "http://a", ""},
		{"empty value is absent", "api_url = \"\"\n", "", ""},
		{"comment line unmatched", "# api_url = http://a\n", "", ""},
		{"no equals unmatched", "garbage\n", "", ""},
		{"unknown key ignored", "other = x\n", "", ""},
		{"duplicate last wins", "api_url = http://a\napi_url = http://b\n", "http://b", ""},
		{"empty line between", "\n\napi_url = http://a\n\n", "http://a", ""},
		{"both keys", "api_url = http://a\napi_key = secret\n", "http://a", "secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotURL, gotKey := parseConfig(tc.data)
			if gotURL != tc.wantURL || gotKey != tc.wantKey {
				t.Fatalf("parseConfig(%q) = (%q, %q), want (%q, %q)", tc.data, gotURL, gotKey, tc.wantURL, tc.wantKey)
			}
		})
	}
}

func staticLookup(env map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		v, ok := env[key]
		return v, ok
	}
}

func writeProfileFixture(t *testing.T, home, name, content string) {
	t.Helper()
	if err := os.MkdirAll(ProfileDirPath(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ProfilePath(home, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeConfigFixture(t *testing.T, home, content string) {
	t.Helper()
	if err := os.MkdirAll(ConfigDir(home), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigPath(home), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLoadPriorityChain(t *testing.T) {
	profileBody := "api_url = \"http://profile:1\"\napi_key = \"pk\"\n"
	fileBody := "api_url = \"http://file:1\"\napi_key = \"fk\"\n"

	cases := []struct {
		name       string
		env        map[string]string
		profile    string
		files      func(t *testing.T, home string)
		wantURL    string
		wantKey    string
		wantSource string
	}{
		{
			name:    "env url wins with env key",
			env:     map[string]string{EnvAPIURL: "http://env:1", EnvAPIKey: "ek"},
			wantURL: "http://env:1", wantKey: "ek", wantSource: "environment variable",
		},
		{
			name: "env url wins over profile and file",
			env:  map[string]string{EnvAPIURL: "http://env:1"},
			files: func(t *testing.T, home string) {
				writeProfileFixture(t, home, "dev", profileBody)
				writeConfigFixture(t, home, fileBody)
			},
			wantURL: "http://env:1", wantSource: "environment variable",
		},
		{
			name:    "env url invalid errors even though set",
			env:     map[string]string{EnvAPIURL: "ftp://env:1"},
			wantURL: "", wantSource: "",
		},
		{
			name:    "env url set but empty errors",
			env:     map[string]string{EnvAPIURL: ""},
			wantURL: "", wantSource: "",
		},
		{
			name:    "flag profile beats env profile",
			env:     map[string]string{EnvProfile: "other"},
			profile: "dev",
			files: func(t *testing.T, home string) {
				writeProfileFixture(t, home, "dev", "api_url = \"http://dev:1\"\n")
				writeProfileFixture(t, home, "other", "api_url = \"http://other:1\"\n")
			},
			wantURL: "http://dev:1", wantSource: "profile 'dev'",
		},
		{
			name:    "env profile selects when no flag",
			env:     map[string]string{EnvProfile: "dev"},
			files:   func(t *testing.T, home string) { writeProfileFixture(t, home, "dev", profileBody) },
			wantURL: "http://profile:1", wantKey: "pk", wantSource: "profile 'dev'",
		},
		{
			name:    "empty env profile ignored",
			env:     map[string]string{EnvProfile: ""},
			files:   func(t *testing.T, home string) { writeConfigFixture(t, home, fileBody) },
			wantURL: "http://file:1", wantKey: "fk", wantSource: "config file",
		},
		{
			name:    "env key overrides profile key",
			env:     map[string]string{EnvProfile: "dev", EnvAPIKey: "ek"},
			files:   func(t *testing.T, home string) { writeProfileFixture(t, home, "dev", profileBody) },
			wantURL: "http://profile:1", wantKey: "ek", wantSource: "profile 'dev'",
		},
		{
			name:    "env key overrides file key",
			env:     map[string]string{EnvAPIKey: "ek"},
			files:   func(t *testing.T, home string) { writeConfigFixture(t, home, fileBody) },
			wantURL: "http://file:1", wantKey: "ek", wantSource: "config file",
		},
		{
			name:    "file with only api_key is discarded",
			files:   func(t *testing.T, home string) { writeConfigFixture(t, home, "api_key = \"fk\"\n") },
			wantURL: DefaultAPIURL, wantKey: "", wantSource: "default",
		},
		{
			name: "file with only api_key, env key is additive at default level",
			env:  map[string]string{EnvAPIKey: "ek"},
			files: func(t *testing.T, home string) {
				writeConfigFixture(t, home, "api_key = \"fk\"\n")
			},
			wantURL: DefaultAPIURL, wantKey: "ek", wantSource: "default",
		},
		{
			name:    "nothing set falls to default",
			wantURL: DefaultAPIURL, wantKey: "", wantSource: "default",
		},
		{
			name:    "file url invalid errors",
			files:   func(t *testing.T, home string) { writeConfigFixture(t, home, "api_url = \"nope\"\n") },
			wantURL: "", wantSource: "",
		},
		{
			name: "profile url invalid errors",
			env:  map[string]string{EnvProfile: "dev"},
			files: func(t *testing.T, home string) {
				writeProfileFixture(t, home, "dev", "api_url = \"nope\"\n")
			},
			wantURL: "", wantSource: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			if tc.files != nil {
				tc.files(t, home)
			}
			cfg, err := Load(LoadOptions{
				Home:      home,
				Profile:   tc.profile,
				LookupEnv: staticLookup(tc.env),
			})
			if tc.wantSource == "" {
				if err == nil {
					t.Fatalf("Load() = %+v, want an error", cfg)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error: %v", err)
			}
			if cfg.APIURL != tc.wantURL || cfg.APIKey != tc.wantKey || cfg.Source.String() != tc.wantSource {
				t.Fatalf("Load() = (%q, %q, %s), want (%q, %q, %s)",
					cfg.APIURL, cfg.APIKey, cfg.Source, tc.wantURL, tc.wantKey, tc.wantSource)
			}
		})
	}
}

func TestLoadErrors(t *testing.T) {
	t.Run("profile not found", func(t *testing.T) {
		home := t.TempDir()
		_, err := Load(LoadOptions{Home: home, Profile: "nope", LookupEnv: staticLookup(nil)})
		want := "profile 'nope' not found at " + ProfilePath(home, "nope") +
			"; create with: wenyuan-admin profile create nope --api-url <url>"
		if err == nil || err.Error() != want {
			t.Fatalf("Load() error = %v, want %q", err, want)
		}
	})
	t.Run("profile missing api_url", func(t *testing.T) {
		home := t.TempDir()
		writeProfileFixture(t, home, "dev", "api_key = \"k\"\n")
		_, err := Load(LoadOptions{Home: home, Profile: "dev", LookupEnv: staticLookup(nil)})
		want := "profile 'dev' at " + ProfilePath(home, "dev") + " is missing required 'api_url' field"
		if err == nil || err.Error() != want {
			t.Fatalf("Load() error = %v, want %q", err, want)
		}
	})
	t.Run("config read error", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores file permissions")
		}
		home := t.TempDir()
		writeConfigFixture(t, home, "api_url = \"http://a\"\n")
		path := ConfigPath(home)
		if err := os.Chmod(path, 0o000); err != nil {
			t.Fatal(err)
		}
		_, err := Load(LoadOptions{Home: home, LookupEnv: staticLookup(nil)})
		want := "Failed to read config file: " + path
		if err == nil || err.Error() != want {
			t.Fatalf("Load() error = %v, want %q", err, want)
		}
	})
	t.Run("home undeterminable", func(t *testing.T) {
		t.Setenv("HOME", "")
		_, err := Load(LoadOptions{LookupEnv: staticLookup(nil)})
		if err == nil || err.Error() != "Could not determine home directory" {
			t.Fatalf("Load() error = %v, want the home-directory failure", err)
		}
	})
	t.Run("absent file is absent", func(t *testing.T) {
		cfg, err := Load(LoadOptions{Home: t.TempDir(), LookupEnv: staticLookup(nil)})
		if err != nil {
			t.Fatalf("Load() error: %v", err)
		}
		if cfg.Source.Kind != SourceDefault {
			t.Fatalf("source = %s, want default", cfg.Source)
		}
	})
}

func TestSourceString(t *testing.T) {
	cases := []struct {
		source Source
		want   string
	}{
		{Source{Kind: SourceEnv}, "environment variable"},
		{Source{Kind: SourceProfile, Profile: "dev"}, "profile 'dev'"},
		{Source{Kind: SourceFile}, "config file"},
		{Source{Kind: SourceDefault}, "default"},
	}
	for _, tc := range cases {
		if got := tc.source.String(); got != tc.want {
			t.Errorf("Source(%+v).String() = %q, want %q", tc.source, got, tc.want)
		}
	}
}

func TestValidateAPIURL(t *testing.T) {
	for _, ok := range []string{"http://x", "https://x/y", "http://"} {
		if err := ValidateAPIURL(ok); err != nil {
			t.Errorf("ValidateAPIURL(%q) = %v, want nil", ok, err)
		}
	}
	for _, bad := range []string{"", "ftp://x", "localhost:8888", " http://x"} {
		err := ValidateAPIURL(bad)
		want := "Invalid API URL: " + bad + ". Must start with http:// or https://"
		if err == nil || err.Error() != want {
			t.Errorf("ValidateAPIURL(%q) = %v, want %q", bad, err, want)
		}
	}
}

func TestSaveConfigRoundTrip(t *testing.T) {
	home := t.TempDir()
	path, err := SaveConfig(home, "http://x:1", "key")
	if err != nil {
		t.Fatalf("SaveConfig() error: %v", err)
	}
	if path != ConfigPath(home) {
		t.Fatalf("path = %q, want %q", path, ConfigPath(home))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "api_url = \"http://x:1\"\napi_key = \"key\"\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}
	cfg, err := Load(LoadOptions{Home: home, LookupEnv: staticLookup(nil)})
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}
	if cfg.APIURL != "http://x:1" || cfg.APIKey != "key" || cfg.Source.Kind != SourceFile {
		t.Fatalf("Load() = %+v, want the saved file values", cfg)
	}

	// A key-less save writes only the URL line.
	if _, err := SaveConfig(home, "http://x:2", ""); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "api_url = \"http://x:2\"\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}

	if _, err := SaveConfig(home, "nope", ""); err == nil {
		t.Fatal("SaveConfig with an invalid URL should fail")
	}
}

func TestConfigDirLayout(t *testing.T) {
	home := "/home/u"
	if got, want := ConfigPath(home), filepath.Join(home, ".wenyuan", "config"); got != want {
		t.Errorf("ConfigPath = %q, want %q", got, want)
	}
	if got, want := ProfileDirPath(home), filepath.Join(home, ".wenyuan", "cli-profiles"); got != want {
		t.Errorf("ProfileDirPath = %q, want %q", got, want)
	}
	if got, want := ProfilePath(home, "dev"), filepath.Join(home, ".wenyuan", "cli-profiles", "dev.toml"); got != want {
		t.Errorf("ProfilePath = %q, want %q", got, want)
	}
}
