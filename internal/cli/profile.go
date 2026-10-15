package cli

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
	"strings"
	"unicode"
)

// ValidateProfileName enforces the profile name rules: non-empty, no path
// separators, no whitespace, not starting with a dot, and no ".." inside.
// Dots inside a name are otherwise allowed.
func ValidateProfileName(name string) error {
	if name == "" {
		return errors.New("profile name cannot be empty")
	}
	if strings.HasPrefix(name, ".") || strings.Contains(name, "..") ||
		strings.ContainsAny(name, `/\`) || strings.ContainsFunc(name, unicode.IsSpace) {
		return fmt.Errorf("invalid profile name '%s': must not contain path separators, whitespace, or start with '.'", name)
	}
	return nil
}

// Profile is one named connection profile.
type Profile struct {
	Name   string
	APIURL string
	APIKey string
	Path   string
}

// LoadProfile reads a named profile file.
func LoadProfile(home, name string) (Profile, error) {
	if err := ValidateProfileName(name); err != nil {
		return Profile{}, err
	}
	return loadProfileFile(home, name)
}

func loadProfileFile(home, name string) (Profile, error) {
	path := ProfilePath(home, name)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Profile{}, fmt.Errorf("profile '%s' not found at %s; create with: wenyuan-admin profile create %s --api-url <url>", name, path, name)
		}
		return Profile{}, fmt.Errorf("Failed to read profile file: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	apiURL, apiKey := parseConfig(string(data))
	if apiURL == "" {
		return Profile{}, fmt.Errorf("profile '%s' at %s is missing required 'api_url' field", name, path)
	}
	return Profile{Name: name, APIURL: apiURL, APIKey: apiKey, Path: path}, nil
}

// SaveProfile writes a profile file and restricts it to the owner. The write
// and the explicit chmod are kept separate so the documented error contexts
// stay distinguishable. It returns the written path.
func SaveProfile(home, name, apiURL, apiKey string) (string, error) {
	if err := ValidateProfileName(name); err != nil {
		return "", err
	}
	if err := ValidateAPIURL(apiURL); err != nil {
		return "", err
	}
	dir := ProfileDirPath(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("Failed to create profile directory: %s", dir) //nolint:staticcheck // exact copy text of the CLI contract
	}
	path := ProfilePath(home, name)
	content := fmt.Sprintf("api_url = \"%s\"\n", apiURL)
	if apiKey != "" {
		content += fmt.Sprintf("api_key = \"%s\"\n", apiKey)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return "", fmt.Errorf("Failed to write profile file: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", fmt.Errorf("Failed to set permissions on profile file: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	return path, nil
}

// ListProfiles returns the profile names, sorted alphabetically. A missing
// directory is an empty list, not an error, and non-.toml entries are
// ignored.
func ListProfiles(home string) ([]string, error) {
	entries, err := os.ReadDir(ProfileDirPath(home))
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		names = append(names, strings.TrimSuffix(e.Name(), ".toml"))
	}
	sort.Strings(names)
	return names, nil
}

// DeleteProfile removes a profile file.
func DeleteProfile(home, name string) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	path := ProfilePath(home, name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("profile '%s' not found at %s", name, path)
		}
		return err
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("Failed to delete profile file: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	return nil
}

// MaskKey masks an API key for display: keys longer than eight characters
// keep their first and last four, everything else collapses to four stars.
// A full key is never printed.
func MaskKey(key string) string {
	if len(key) > 8 {
		return key[:4] + "..." + key[len(key)-4:]
	}
	return "****"
}

type profileCreateOut struct {
	Name      string `json:"name" yaml:"name"`
	APIURL    string `json:"api_url" yaml:"api_url"`
	APIKeySet bool   `json:"api_key_set" yaml:"api_key_set"`
	Path      string `json:"path" yaml:"path"`
}

// ProfileCreate implements `wenyuan-admin profile create`.
func ProfileCreate(ui *UI, format Format, home, name, apiURL, apiKey string) error {
	path, err := SaveProfile(home, name, apiURL, apiKey)
	if err != nil {
		return err
	}
	if !format.IsMachine() {
		ui.Infof("Profile '%s' saved to %s", name, path)
		ui.Plain("")
		ui.Plain("  API URL: %s", apiURL)
		if apiKey != "" {
			ui.Plain("  API Key: %s", MaskKey(apiKey))
		}
		ui.Plain("")
		ui.Plain("Use with: wenyuan -p %s <command>  (or export %s=%s)", name, EnvProfile, name)
		return nil
	}
	return format.Render(ui.Stdout, profileCreateOut{
		Name: name, APIURL: apiURL, APIKeySet: apiKey != "", Path: path,
	})
}

type profileListOut struct {
	Profiles []string `json:"profiles" yaml:"profiles"`
}

// ProfileList implements `wenyuan-admin profile list`.
func ProfileList(ui *UI, format Format, home string) error {
	names, err := ListProfiles(home)
	if err != nil {
		return err
	}
	if !format.IsMachine() {
		if len(names) == 0 {
			ui.Infof("No profiles found.")
			ui.Plain("")
			ui.Plain("Create one with: wenyuan-admin profile create <name> --api-url <url>")
			return nil
		}
		ui.Infof("Profiles:")
		for _, name := range names {
			ui.Plain("  • %s", name)
		}
		return nil
	}
	if names == nil {
		names = []string{}
	}
	return format.Render(ui.Stdout, profileListOut{Profiles: names})
}

type profileShowOut struct {
	Name      string `json:"name" yaml:"name"`
	Path      string `json:"path" yaml:"path"`
	APIURL    string `json:"api_url" yaml:"api_url"`
	APIKeySet bool   `json:"api_key_set" yaml:"api_key_set"`
}

// ProfileShow implements `wenyuan-admin profile show`.
func ProfileShow(ui *UI, format Format, home, name string) error {
	p, err := LoadProfile(home, name)
	if err != nil {
		return err
	}
	if !format.IsMachine() {
		ui.Infof("Profile '%s'", name)
		ui.Plain("")
		ui.Plain("  %-8s %s", "Path:", p.Path)
		ui.Plain("  %-8s %s", "API URL:", p.APIURL)
		if p.APIKey != "" {
			ui.Plain("  %-8s %s", "API Key:", MaskKey(p.APIKey))
		}
		return nil
	}
	return format.Render(ui.Stdout, profileShowOut{
		Name: p.Name, Path: p.Path, APIURL: p.APIURL, APIKeySet: p.APIKey != "",
	})
}

type profileDeleteOut struct {
	Name    string `json:"name" yaml:"name"`
	Path    string `json:"path" yaml:"path"`
	Deleted bool   `json:"deleted" yaml:"deleted"`
}

// ProfileDelete implements `wenyuan-admin profile delete`. The confirmation prompt
// only appears in pretty mode without -y; machine formats skip it entirely
// and delete unconditionally, which is the documented upstream behavior, not
// an oversight.
func ProfileDelete(ui *UI, format Format, home string, stdin io.Reader, name string, yes bool) error {
	if err := ValidateProfileName(name); err != nil {
		return err
	}
	// Existence is checked first, with the delete-specific message that
	// carries no create hint.
	path := ProfilePath(home, name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("profile '%s' not found at %s", name, path)
		}
		return err
	}
	if !format.IsMachine() && !yes {
		_, _ = fmt.Fprintf(ui.Stdout, "Delete profile '%s' at %s? [y/N]: ", name, path)
		answer := strings.ToLower(readLine(stdin))
		if answer != "y" && answer != "yes" {
			ui.Infof("Aborted.")
			return nil
		}
	}
	if err := DeleteProfile(home, name); err != nil {
		return err
	}
	if !format.IsMachine() {
		ui.Infof("Deleted profile '%s' (%s)", name, path)
		return nil
	}
	return format.Render(ui.Stdout, profileDeleteOut{Name: name, Path: path, Deleted: true})
}
