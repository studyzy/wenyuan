package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateProfileName(t *testing.T) {
	cases := []struct {
		name string
		want string // "" means valid
	}{
		{"", "profile name cannot be empty"},
		{".hidden", "invalid profile name '.hidden': must not contain path separators, whitespace, or start with '.'"},
		{"a/b", "invalid profile name 'a/b': must not contain path separators, whitespace, or start with '.'"},
		{`a\b`, "invalid profile name 'a\\b': must not contain path separators, whitespace, or start with '.'"},
		{"a..b", "invalid profile name 'a..b': must not contain path separators, whitespace, or start with '.'"},
		{"a b", "invalid profile name 'a b': must not contain path separators, whitespace, or start with '.'"},
		{"a\tb", "invalid profile name 'a\tb': must not contain path separators, whitespace, or start with '.'"},
		{"a.b", ""},
		{"dev-1", ""},
		{"DEV", ""},
	}
	for _, tc := range cases {
		err := ValidateProfileName(tc.name)
		if tc.want == "" {
			if err != nil {
				t.Errorf("ValidateProfileName(%q) = %v, want nil", tc.name, err)
			}
			continue
		}
		if err == nil || err.Error() != tc.want {
			t.Errorf("ValidateProfileName(%q) = %v, want %q", tc.name, err, tc.want)
		}
	}
}

func TestSaveProfileModeAndContent(t *testing.T) {
	home := t.TempDir()
	path, err := SaveProfile(home, "dev", "http://p:1", "key")
	if err != nil {
		t.Fatalf("SaveProfile() error: %v", err)
	}
	if path != ProfilePath(home, "dev") {
		t.Fatalf("path = %q, want %q", path, ProfilePath(home, "dev"))
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode = %o, want 600", got)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(data), "api_url = \"http://p:1\"\napi_key = \"key\"\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}

	// Re-saving over a looser file still ends at 0600, and a key-less save
	// writes only the URL line.
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SaveProfile(home, "dev", "http://p:2", ""); err != nil {
		t.Fatal(err)
	}
	info, err = os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode after re-save = %o, want 600", got)
	}
	data, _ = os.ReadFile(path)
	if got, want := string(data), "api_url = \"http://p:2\"\n"; got != want {
		t.Fatalf("file = %q, want %q", got, want)
	}

	p, err := LoadProfile(home, "dev")
	if err != nil {
		t.Fatalf("LoadProfile() error: %v", err)
	}
	if p.APIURL != "http://p:2" || p.APIKey != "" || p.Name != "dev" || p.Path != path {
		t.Fatalf("LoadProfile() = %+v", p)
	}
}

func TestSaveProfileValidation(t *testing.T) {
	home := t.TempDir()
	if _, err := SaveProfile(home, "", "http://p:1", ""); err == nil || err.Error() != "profile name cannot be empty" {
		t.Errorf("empty name error = %v", err)
	}
	if _, err := SaveProfile(home, "bad name", "http://p:1", ""); err == nil {
		t.Error("whitespace name should fail")
	}
	want := ValidateAPIURL("nope")
	if _, err := SaveProfile(home, "dev", "nope", ""); err == nil || err.Error() != want.Error() {
		t.Errorf("invalid url error = %v, want %v", err, want)
	}
}

func TestLoadProfileErrors(t *testing.T) {
	home := t.TempDir()
	_, err := LoadProfile(home, "nope")
	want := "profile 'nope' not found at " + ProfilePath(home, "nope") +
		"; create with: wenyuan-admin profile create nope --api-url <url>"
	if err == nil || err.Error() != want {
		t.Fatalf("missing profile error = %v, want %q", err, want)
	}

	writeProfileFixture(t, home, "empty", "api_key = \"k\"\n")
	_, err = LoadProfile(home, "empty")
	want = "profile 'empty' at " + ProfilePath(home, "empty") + " is missing required 'api_url' field"
	if err == nil || err.Error() != want {
		t.Fatalf("missing api_url error = %v, want %q", err, want)
	}

	if os.Geteuid() != 0 {
		writeProfileFixture(t, home, "locked", "api_url = \"http://p:1\"\n")
		if err := os.Chmod(ProfilePath(home, "locked"), 0o000); err != nil {
			t.Fatal(err)
		}
		_, err = LoadProfile(home, "locked")
		want = "Failed to read profile file: " + ProfilePath(home, "locked")
		if err == nil || err.Error() != want {
			t.Fatalf("read error = %v, want %q", err, want)
		}
	}
}

func TestListProfiles(t *testing.T) {
	home := t.TempDir()
	names, err := ListProfiles(home)
	if err != nil || names != nil {
		t.Fatalf("ListProfiles without a dir = (%v, %v), want (nil, nil)", names, err)
	}

	writeProfileFixture(t, home, "b", "api_url = \"http://b\"\n")
	writeProfileFixture(t, home, "a", "api_url = \"http://a\"\n")
	if err := os.WriteFile(filepath.Join(ProfileDirPath(home), "notes.txt"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(ProfileDirPath(home), "z.toml"), 0o700); err != nil {
		t.Fatal(err)
	}
	names, err = ListProfiles(home)
	if err != nil {
		t.Fatal(err)
	}
	if len(names) != 2 || names[0] != "a" || names[1] != "b" {
		t.Fatalf("names = %v, want [a b]", names)
	}
}

func TestDeleteProfile(t *testing.T) {
	home := t.TempDir()
	err := DeleteProfile(home, "nope")
	want := "profile 'nope' not found at " + ProfilePath(home, "nope")
	if err == nil || err.Error() != want {
		t.Fatalf("missing delete error = %v, want %q", err, want)
	}
	if _, err := SaveProfile(home, "dev", "http://p:1", ""); err != nil {
		t.Fatal(err)
	}
	if err := DeleteProfile(home, "dev"); err != nil {
		t.Fatalf("DeleteProfile() error: %v", err)
	}
	if _, err := os.Stat(ProfilePath(home, "dev")); !os.IsNotExist(err) {
		t.Fatalf("profile file still present: %v", err)
	}
}

func TestMaskKey(t *testing.T) {
	cases := []struct{ key, want string }{
		{"", "****"},
		{"short", "****"},
		{"12345678", "****"},
		{"123456789", "1234...6789"},
		{"sk-abcdefghijklmnop", "sk-a...mnop"},
	}
	for _, tc := range cases {
		if got := MaskKey(tc.key); got != tc.want {
			t.Errorf("MaskKey(%q) = %q, want %q", tc.key, got, tc.want)
		}
	}
}

func testUI() (*UI, *strings.Builder, *strings.Builder) {
	var out, errB strings.Builder
	return NewUI(&out, &errB, ColorEnv{}), &out, &errB
}

func TestProfileCreateOutput(t *testing.T) {
	home := t.TempDir()
	ui, out, _ := testUI()
	if err := ProfileCreate(ui, FormatPretty, home, "dev", "http://p:1", "sk-abcdefghijklmnop"); err != nil {
		t.Fatal(err)
	}
	want := "Profile 'dev' saved to " + ProfilePath(home, "dev") + "\n" +
		"\n" +
		"  API URL: http://p:1\n" +
		"  API Key: sk-a...mnop\n" +
		"\n" +
		"Use with: wenyuan -p dev <command>  (or export " + EnvProfile + "=dev)\n"
	if got := out.String(); got != want {
		t.Fatalf("pretty output =\n%q\nwant\n%q", got, want)
	}

	// Without a key the API Key line is omitted.
	ui2, out2, _ := testUI()
	if err := ProfileCreate(ui2, FormatPretty, home, "bare", "http://p:2", ""); err != nil {
		t.Fatal(err)
	}
	if got := out2.String(); strings.Contains(got, "API Key") {
		t.Fatalf("key-less create printed a key line: %q", got)
	}
}

func TestProfileCreateJSON(t *testing.T) {
	home := t.TempDir()
	ui, out, _ := testUI()
	if err := ProfileCreate(ui, FormatJSON, home, "dev", "http://p:1", "k"); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	// Keys serialize in declaration order: name, api_url, api_key_set, path.
	order := []string{`"name"`, `"api_url"`, `"api_key_set"`, `"path"`}
	at := -1
	for _, key := range order {
		next := strings.Index(got, key)
		if next <= at {
			t.Fatalf("key %s out of order in %s", key, got)
		}
		at = next
	}
	var parsed struct {
		Name      string `json:"name"`
		APIURL    string `json:"api_url"`
		APIKeySet bool   `json:"api_key_set"`
		Path      string `json:"path"`
	}
	if err := json.Unmarshal([]byte(got), &parsed); err != nil {
		t.Fatalf("json: %v", err)
	}
	if parsed.Name != "dev" || parsed.APIURL != "http://p:1" || !parsed.APIKeySet || parsed.Path != ProfilePath(home, "dev") {
		t.Fatalf("parsed = %+v", parsed)
	}
}

func TestProfileListOutput(t *testing.T) {
	home := t.TempDir()
	ui, out, _ := testUI()
	if err := ProfileList(ui, FormatPretty, home); err != nil {
		t.Fatal(err)
	}
	want := "No profiles found.\n\nCreate one with: wenyuan-admin profile create <name> --api-url <url>\n"
	if got := out.String(); got != want {
		t.Fatalf("empty pretty = %q, want %q", got, want)
	}

	writeProfileFixture(t, home, "b", "api_url = \"http://b\"\n")
	writeProfileFixture(t, home, "a", "api_url = \"http://a\"\n")
	ui2, out2, _ := testUI()
	if err := ProfileList(ui2, FormatPretty, home); err != nil {
		t.Fatal(err)
	}
	want = "Profiles:\n  • a\n  • b\n"
	if got := out2.String(); got != want {
		t.Fatalf("pretty = %q, want %q", got, want)
	}

	ui3, out3, _ := testUI()
	if err := ProfileList(ui3, FormatJSON, home); err != nil {
		t.Fatal(err)
	}
	if got, wantJSON := out3.String(), "{\n  \"profiles\": [\n    \"a\",\n    \"b\"\n  ]\n}\n"; got != wantJSON {
		t.Fatalf("json = %q, want %q", got, wantJSON)
	}
}

func TestProfileShowOutput(t *testing.T) {
	home := t.TempDir()
	writeProfileFixture(t, home, "dev", "api_url = \"http://p:1\"\napi_key = \"sk-abcdefghijklmnop\"\n")

	ui, out, _ := testUI()
	if err := ProfileShow(ui, FormatPretty, home, "dev"); err != nil {
		t.Fatal(err)
	}
	want := "Profile 'dev'\n\n" +
		"  Path:    " + ProfilePath(home, "dev") + "\n" +
		"  API URL: http://p:1\n" +
		"  API Key: sk-a...mnop\n"
	if got := out.String(); got != want {
		t.Fatalf("pretty = %q, want %q", got, want)
	}

	ui2, out2, _ := testUI()
	if err := ProfileShow(ui2, FormatYAML, home, "dev"); err != nil {
		t.Fatal(err)
	}
	got := out2.String()
	// show orders keys name, path, api_url, api_key_set — different from
	// create's order, which is why the payload types are separate.
	if strings.Index(got, "name:") > strings.Index(got, "path:") ||
		strings.Index(got, "path:") > strings.Index(got, "api_url:") ||
		strings.Index(got, "api_url:") > strings.Index(got, "api_key_set:") {
		t.Fatalf("yaml key order wrong: %s", got)
	}
	if !strings.Contains(got, "api_key_set: true\n") {
		t.Fatalf("yaml missing api_key_set: %s", got)
	}
}

func TestProfileDeleteOutput(t *testing.T) {
	newProfile := func(t *testing.T) string {
		t.Helper()
		home := t.TempDir()
		if _, err := SaveProfile(home, "dev", "http://p:1", ""); err != nil {
			t.Fatal(err)
		}
		return home
	}

	t.Run("pretty declines with n", func(t *testing.T) {
		home := newProfile(t)
		ui, out, _ := testUI()
		if err := ProfileDelete(ui, FormatPretty, home, strings.NewReader("n\n"), "dev", false); err != nil {
			t.Fatalf("abort should exit 0, got %v", err)
		}
		if got := out.String(); !strings.Contains(got, "Aborted.") {
			t.Fatalf("output missing Aborted.: %q", got)
		}
		if _, err := os.Stat(ProfilePath(home, "dev")); err != nil {
			t.Fatalf("file should survive an abort: %v", err)
		}
	})
	t.Run("pretty accepts y and yes case-insensitively", func(t *testing.T) {
		for _, answer := range []string{"y\n", "  YES \n", "Yes\n"} {
			home := newProfile(t)
			ui, out, _ := testUI()
			if err := ProfileDelete(ui, FormatPretty, home, strings.NewReader(answer), "dev", false); err != nil {
				t.Fatalf("answer %q: %v", answer, err)
			}
			if got := out.String(); !strings.Contains(got, "Deleted profile 'dev' (") {
				t.Fatalf("answer %q output: %q", answer, got)
			}
			if _, err := os.Stat(ProfilePath(home, "dev")); !os.IsNotExist(err) {
				t.Fatalf("answer %q: file still present", answer)
			}
		}
	})
	t.Run("eof declines", func(t *testing.T) {
		home := newProfile(t)
		ui, _, _ := testUI()
		if err := ProfileDelete(ui, FormatPretty, home, strings.NewReader(""), "dev", false); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(ProfilePath(home, "dev")); err != nil {
			t.Fatalf("file should survive EOF: %v", err)
		}
	})
	t.Run("-y skips the prompt", func(t *testing.T) {
		home := newProfile(t)
		ui, out, _ := testUI()
		if err := ProfileDelete(ui, FormatPretty, home, strings.NewReader(""), "dev", true); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "[y/N]") {
			t.Fatalf("-y still prompted: %q", out.String())
		}
	})
	t.Run("json skips the confirmation entirely", func(t *testing.T) {
		home := newProfile(t)
		ui, out, _ := testUI()
		if err := ProfileDelete(ui, FormatJSON, home, strings.NewReader(""), "dev", false); err != nil {
			t.Fatal(err)
		}
		var parsed struct {
			Name    string `json:"name"`
			Path    string `json:"path"`
			Deleted bool   `json:"deleted"`
		}
		if err := json.Unmarshal([]byte(out.String()), &parsed); err != nil {
			t.Fatalf("json: %v", err)
		}
		if parsed.Name != "dev" || parsed.Path != ProfilePath(home, "dev") || !parsed.Deleted {
			t.Fatalf("parsed = %+v", parsed)
		}
		if _, err := os.Stat(ProfilePath(home, "dev")); !os.IsNotExist(err) {
			t.Fatal("json delete did not delete")
		}
	})
	t.Run("missing profile errors", func(t *testing.T) {
		home := t.TempDir()
		ui, _, _ := testUI()
		err := ProfileDelete(ui, FormatPretty, home, strings.NewReader(""), "nope", true)
		want := "profile 'nope' not found at " + ProfilePath(home, "nope")
		if err == nil || err.Error() != want {
			t.Fatalf("error = %v, want %q", err, want)
		}
	})
}
