package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotenvFile reads a dotenv file and applies its key/value pairs to the
// process environment. A discovered .env is authoritative over values the
// caller already exported, which is the reference's own entrypoint rule, so a
// later export cannot quietly undo the file a deployment shipped. A missing
// file is not an error so callers can always attempt to load the conventional
// default path.
func LoadDotenvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer func() { _ = f.Close() }()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := parseDotenvLine(scanner.Text())
		if !ok {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
}

// LoadDotenvForEntrypoint assembles the environment behind a wenyuan
// entrypoint: the conventional .env file is applied over the process
// environment, then the YAML configuration file is read behind both. Every
// entrypoint loads both files through this one call, so a deployment
// configures the server, the worker and bankctl identically.
func LoadDotenvForEntrypoint() error {
	if err := LoadDotenvFile(".env"); err != nil {
		return err
	}
	return LoadConfigFile()
}

// parseDotenvLine turns one dotenv line into a key/value pair. Blank lines and
// comment lines are skipped.
func parseDotenvLine(line string) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if trimmed == "" || strings.HasPrefix(trimmed, "#") {
		return "", "", false
	}
	trimmed = strings.TrimPrefix(trimmed, "export ")
	eq := strings.IndexByte(trimmed, '=')
	if eq <= 0 {
		return "", "", false
	}
	key := strings.TrimSpace(trimmed[:eq])
	value := strings.TrimSpace(trimmed[eq+1:])
	value = unquoteDotenvValue(value)
	if key == "" {
		return "", "", false
	}
	return key, value, true
}

func unquoteDotenvValue(value string) string {
	if len(value) >= 2 {
		if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
			return value[1 : len(value)-1]
		}
	}
	return value
}
