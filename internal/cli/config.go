package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Environment variable names of the client-facing connection contract. The
// HINDSIGHT_* spellings are the repository's established convention for these
// keys and are the only sanctioned use of the token.
const (
	EnvAPIURL  = "HINDSIGHT_API_URL"
	EnvAPIKey  = "HINDSIGHT_API_KEY"
	EnvProfile = "HINDSIGHT_PROFILE"
)

// DefaultAPIURL is applied when no level of the priority chain resolves one.
const DefaultAPIURL = "http://localhost:18888"

// Paths under the home directory.
const (
	configDirName  = ".wenyuan"
	configFileName = "config"
	profilesDir    = "cli-profiles"
)

// SourceKind enumerates the four levels of the priority chain.
type SourceKind int

const (
	SourceDefault SourceKind = iota
	SourceFile
	SourceProfile
	SourceEnv
)

// Source records where the resolved configuration came from.
type Source struct {
	Kind    SourceKind
	Profile string // profile name when Kind is SourceProfile
}

// String renders the exact display text of the source.
func (s Source) String() string {
	switch s.Kind {
	case SourceEnv:
		return "environment variable"
	case SourceProfile:
		return fmt.Sprintf("profile '%s'", s.Profile)
	case SourceFile:
		return "config file"
	default:
		return "default"
	}
}

// Config is the resolved connection configuration.
type Config struct {
	APIURL string
	APIKey string
	Source Source
}

// LoadOptions tunes Load for injection and for commands without profile
// support.
type LoadOptions struct {
	// Home overrides the OS home directory; empty uses the OS resolution.
	Home string
	// Profile is the -p flag value; it beats the profile environment variable.
	Profile string
	// IgnoreProfileEnv skips the profile environment variable entirely, which
	// the configure command does.
	IgnoreProfileEnv bool
	// LookupEnv resolves environment variables; nil uses os.LookupEnv.
	LookupEnv func(string) (string, bool)
}

// ConfigDir returns <home>/.wenyuan.
func ConfigDir(home string) string { return filepath.Join(home, configDirName) }

// ConfigPath returns <home>/.wenyuan/config.
func ConfigPath(home string) string { return filepath.Join(ConfigDir(home), configFileName) }

// ProfileDirPath returns <home>/.wenyuan/cli-profiles.
func ProfileDirPath(home string) string { return filepath.Join(ConfigDir(home), profilesDir) }

// ProfilePath returns the file of one named profile.
func ProfilePath(home, name string) string {
	return filepath.Join(ProfileDirPath(home), name+".toml")
}

// UserHome resolves the OS home directory.
func UserHome() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("Could not determine home directory") //nolint:staticcheck // exact copy text of the CLI contract
	}
	return home, nil
}

// Load resolves the configuration along the priority chain: the API
// environment variables, then the named profile, then the config file, then
// the default. The key environment variable is consulted at every level; the
// URL environment variable only wins by being set.
func Load(opts LoadOptions) (Config, error) {
	lookup := opts.LookupEnv
	if lookup == nil {
		lookup = os.LookupEnv
	}

	if raw, ok := lookup(EnvAPIURL); ok {
		if err := ValidateAPIURL(raw); err != nil {
			return Config{}, err
		}
		return Config{APIURL: raw, APIKey: lookupKey(lookup), Source: Source{Kind: SourceEnv}}, nil
	}

	name := opts.Profile
	if name == "" && !opts.IgnoreProfileEnv {
		if v, ok := lookup(EnvProfile); ok && v != "" {
			name = v
		}
	}
	if name != "" {
		home, err := resolveHome(opts.Home)
		if err != nil {
			return Config{}, err
		}
		p, err := loadProfileFile(home, name)
		if err != nil {
			return Config{}, err
		}
		if err := ValidateAPIURL(p.APIURL); err != nil {
			return Config{}, err
		}
		key := p.APIKey
		if envKey, ok := lookup(EnvAPIKey); ok {
			key = envKey
		}
		return Config{APIURL: p.APIURL, APIKey: key, Source: Source{Kind: SourceProfile, Profile: name}}, nil
	}

	home, err := resolveHome(opts.Home)
	if err != nil {
		return Config{}, err
	}
	path := ConfigPath(home)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		url, key := parseConfig(string(data))
		if url != "" {
			if err := ValidateAPIURL(url); err != nil {
				return Config{}, err
			}
			if envKey, ok := lookup(EnvAPIKey); ok {
				key = envKey
			}
			return Config{APIURL: url, APIKey: key, Source: Source{Kind: SourceFile}}, nil
		}
		// A file without api_url counts as absent; its key alone is
		// discarded rather than adopted at the default level.
	case errors.Is(err, fs.ErrNotExist):
		// Absent.
	default:
		return Config{}, fmt.Errorf("Failed to read config file: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}

	return Config{APIURL: DefaultAPIURL, APIKey: lookupKey(lookup), Source: Source{Kind: SourceDefault}}, nil
}

func resolveHome(override string) (string, error) {
	if override != "" {
		return override, nil
	}
	return UserHome()
}

func lookupKey(lookup func(string) (string, bool)) string {
	key, _ := lookup(EnvAPIKey)
	return key
}

// parseConfig reads the line-oriented TOML subset: one key = string value
// per line, split at the first '=', quote characters stripped by repeated
// trimming, empty values treated as absent, unknown keys ignored, and the
// last occurrence of a key winning. There is no comment syntax; unmatched
// lines are simply skipped.
func parseConfig(data string) (apiURL, apiKey string) {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		eq := strings.Index(line, "=")
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		value = strings.Trim(value, `"`)
		value = strings.Trim(value, `'`)
		if value == "" {
			continue
		}
		switch key {
		case "api_url":
			apiURL = value
		case "api_key":
			apiKey = value
		}
	}
	return apiURL, apiKey
}

// ValidateAPIURL checks the http:// or https:// prefix and nothing else; no
// URL parsing, so the prefix followed by anything is accepted.
func ValidateAPIURL(url string) error {
	if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
		return nil
	}
	return fmt.Errorf("Invalid API URL: %s. Must start with http:// or https://", url) //nolint:staticcheck // exact copy text of the CLI contract
}

// SaveConfig writes the config file, creating the directory when missing.
// The file inherits the process defaults (0644 & umask), unlike profiles.
// It returns the written path.
func SaveConfig(home, apiURL, apiKey string) (string, error) {
	if err := ValidateAPIURL(apiURL); err != nil {
		return "", err
	}
	dir := ConfigDir(home)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("Failed to create config directory: %s", dir) //nolint:staticcheck // exact copy text of the CLI contract
	}
	path := ConfigPath(home)
	content := fmt.Sprintf("api_url = \"%s\"\n", apiURL)
	if apiKey != "" {
		content += fmt.Sprintf("api_key = \"%s\"\n", apiKey)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("Failed to write config file: %s", path) //nolint:staticcheck // exact copy text of the CLI contract
	}
	return path, nil
}
