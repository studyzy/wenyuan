package storage

import (
	"embed"
	"fmt"
	"io/fs"
	"sort"
	"strings"
)

// migrationFS embeds the ordered SQL migration series so the binary can apply
// it without shipping loose files. The migrations directory is a sibling of the
// module root, so it is embedded from the repository root via a generated
// listing rather than a package-relative glob.
//
//go:embed all:embedded
var migrationFS embed.FS

// Migration is one ordered schema step.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// MigrationSeries returns the embedded migration series in ascending version
// order. It errors if the series is empty or contains a gap.
func MigrationSeries() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFS, "embedded")
	if err != nil {
		return nil, err
	}
	var migrations []Migration
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version, name, err := parseMigrationName(entry.Name())
		if err != nil {
			return nil, err
		}
		content, err := fs.ReadFile(migrationFS, "embedded/"+entry.Name())
		if err != nil {
			return nil, err
		}
		migrations = append(migrations, Migration{Version: version, Name: name, SQL: string(content)})
	}
	sort.Slice(migrations, func(i, j int) bool { return migrations[i].Version < migrations[j].Version })
	if len(migrations) == 0 {
		return nil, fmt.Errorf("no migrations found")
	}
	for i, m := range migrations {
		if m.Version != i+1 {
			return nil, fmt.Errorf("migration series has a gap at version %d", m.Version)
		}
	}
	return migrations, nil
}

func parseMigrationName(fileName string) (int, string, error) {
	trimmed := strings.TrimSuffix(fileName, ".sql")
	parts := strings.SplitN(trimmed, "_", 2)
	if len(parts) < 1 || parts[0] == "" {
		return 0, "", fmt.Errorf("invalid migration file name %q", fileName)
	}
	var version int
	if _, err := fmt.Sscanf(parts[0], "%04d", &version); err != nil {
		return 0, "", fmt.Errorf("invalid migration version in %q", fileName)
	}
	if version == 0 {
		return 0, "", fmt.Errorf("invalid migration version in %q", fileName)
	}
	name := trimmed
	if len(parts) == 2 {
		name = parts[1]
	}
	return version, name, nil
}

// Statements splits a migration body into individual statements for ordered
// execution by a backend.
func (m Migration) Statements() []string {
	raw := strings.Split(m.SQL, ";")
	out := make([]string, 0, len(raw))
	for _, statement := range raw {
		trimmed := strings.TrimSpace(stripSQLComments(statement))
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func stripSQLComments(statement string) string {
	var b strings.Builder
	for _, line := range strings.Split(statement, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "--") {
			continue
		}
		b.WriteString(line)
		b.WriteString("\n")
	}
	return b.String()
}

// LatestVersion returns the highest migration version in the series.
func LatestVersion() (int, error) {
	series, err := MigrationSeries()
	if err != nil {
		return 0, err
	}
	return series[len(series)-1].Version, nil
}
