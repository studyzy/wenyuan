package config

import (
	"bufio"
	"os"
	"strings"
)

// LoadDotenvFile reads a dotenv file and applies its key/value pairs to the
// process environment without overwriting variables that are already set. A
// missing file is not an error so callers can always attempt to load the
// conventional default path.
func LoadDotenvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		key, value, ok := parseDotenvLine(scanner.Text())
		if !ok {
			continue
		}
		if _, present := os.LookupEnv(key); present {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return err
		}
	}
	return scanner.Err()
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
