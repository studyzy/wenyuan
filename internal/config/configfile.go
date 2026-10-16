package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/spf13/viper"
)

// EnvConfigFile names the environment variable that points at the YAML
// configuration file. Unset, the conventional path is read instead.
const EnvConfigFile = "HINDSIGHT_API_CONFIG_FILE"

// The conventional configuration file: etc/wenyuan.yaml, relative to the
// process working directory — the same convention the .env file follows.
const (
	DefaultConfigDir  = "etc"
	DefaultConfigName = "wenyuan"
	DefaultConfigExt  = "yaml"
)

// envPrefix is the prefix every configuration key carries. The environment
// spells it HINDSIGHT_API_LLM_PROVIDER; the configuration file spells the same
// key llm_provider — the environment name, lowercased, without the prefix.
const envPrefix = "HINDSIGHT_API"

// configSource is the configuration file as it was read, with the path it was
// read from, or nil when no file was loaded. It is package state because the
// resolver reads every key through lookupEnv, which has no place to thread a
// source through.
var (
	configSourceMu sync.RWMutex
	configSource   *viper.Viper
	configPath     string
)

// DefaultConfigPath is the file read when EnvConfigFile names no other one.
func DefaultConfigPath() string {
	return filepath.Join(DefaultConfigDir, DefaultConfigName+"."+DefaultConfigExt)
}

// LoadConfigFile makes the YAML configuration file the source behind the
// process environment for every key the configuration resolves.
//
// Precedence, highest first: command-line flags, the .env file, the rest of
// the process environment, this file, the built-in defaults. A discovered
// .env is authoritative over the ambient environment, which is the rule the
// reference's own entrypoints keep; nothing here loads .env, and nothing here
// writes the environment, so a deployment that never wrote a configuration
// file resolves exactly as before.
//
// A path named by EnvConfigFile must exist and parse: an operator who named a
// file expects it to be read. The conventional path may be absent, which leaves
// the process environment as the only source.
func LoadConfigFile() error {
	path := strings.TrimSpace(os.Getenv(EnvConfigFile))
	required := path != ""
	if !required {
		path = DefaultConfigPath()
	}

	source := viper.New()
	source.SetEnvPrefix(envPrefix)
	source.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	source.AutomaticEnv()
	source.SetConfigFile(path)

	if err := source.ReadInConfig(); err != nil {
		if !required && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("configuration file %s: %w", path, err)
	}

	flat, err := flattenConfigKeys(source, path)
	if err != nil {
		return err
	}
	configSourceMu.Lock()
	configSource, configPath = flat, path
	configSourceMu.Unlock()
	return nil
}

// LoadedConfigPath is the path of the configuration file in effect, or empty
// when none was read. A process reports it so an operator can tell a file that
// was read from one that was only expected.
func LoadedConfigPath() string {
	configSourceMu.RLock()
	defer configSourceMu.RUnlock()
	return configPath
}

// lookupConfigFile answers one configuration key from the loaded file,
// lowercased and without the prefix, which is how the file spells it. It
// reports false when no file was loaded, the key is absent, or the value is
// blank — and never for a key outside the environment namespace, which no file
// can configure.
func lookupConfigFile(key string) (string, bool) {
	configSourceMu.RLock()
	source := configSource
	configSourceMu.RUnlock()
	if source == nil {
		return "", false
	}
	suffix, ok := strings.CutPrefix(key, envPrefix+"_")
	if !ok {
		return "", false
	}
	value := strings.TrimSpace(source.GetString(strings.ToLower(suffix)))
	if value == "" {
		return "", false
	}
	return value, true
}

// flattenConfigKeys normalizes the parsed document into the flat key spelling
// the resolver asks for, returned as a fresh viper source: nested mappings are
// flattened to underscore-joined keys (llm: {provider: openai} answers
// llm_provider), and every key is lowercased. Both spellings of a key may
// appear; they must agree, and a disagreement is refused naming the key rather
// than resolved by an arbitrary winner. A list value is refused — an
// environment value cannot carry one — while a mapping value becomes deeper
// keys, so a setting whose value is itself a mapping must be spelled as a
// quoted JSON string.
//
// The flattened entries are installed as defaults, which rank below the
// environment in viper's lookup order: the file answers behind the process
// environment, exactly as before.
func flattenConfigKeys(source *viper.Viper, path string) (*viper.Viper, error) {
	settings := map[string]string{}
	if err := flattenMap(source.AllSettings(), "", path, settings); err != nil {
		return nil, err
	}
	flat := viper.New()
	flat.SetEnvPrefix(envPrefix)
	flat.SetEnvKeyReplacer(strings.NewReplacer(".", "_", "-", "_"))
	flat.AutomaticEnv()
	keys := make([]string, 0, len(settings))
	for key := range settings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		flat.SetDefault(key, settings[key])
	}
	return flat, nil
}

// flattenMap walks one nesting level, joining keys with underscores. Empty
// mappings contribute nothing; anything else must be a scalar or a list, and a
// list is refused.
func flattenMap(node map[string]any, prefix string, path string, out map[string]string) error {
	keys := make([]string, 0, len(node))
	for key := range node {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		flatKey := prefix + strings.ToLower(strings.TrimSpace(key))
		if flatKey == "" {
			return fmt.Errorf("configuration file %s: a key must not be blank", path)
		}
		switch value := node[key].(type) {
		case map[string]any:
			if err := flattenMap(value, flatKey+"_", path, out); err != nil {
				return err
			}
		case map[any]any:
			converted := make(map[string]any, len(value))
			for k, v := range value {
				converted[fmt.Sprint(k)] = v
			}
			if err := flattenMap(converted, flatKey+"_", path, out); err != nil {
				return err
			}
		case []any:
			return fmt.Errorf(
				"configuration file %s: key %q holds a list, which an environment value cannot express", path, flatKey)
		default:
			spelled := fmt.Sprint(value)
			if prev, ok := out[flatKey]; ok && prev != spelled {
				return fmt.Errorf(
					"configuration file %s: key %q is spelled two ways with different values (%q and %q)",
					path, flatKey, prev, spelled)
			}
			out[flatKey] = spelled
		}
	}
	return nil
}
