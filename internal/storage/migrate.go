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
//
// The split is a small scanner rather than a split on ";" because a semicolon
// is only a statement boundary when it is outside every quoted region. Three
// regions would otherwise break the split:
//
//   - a dollar-quoted block ($$ ... $$ or $tag$ ... $tag$), which is how a
//     PL/pgSQL function body is written — its inner semicolons separate
//     statements *of the function*, not of the migration;
//   - a single-quoted string, where a semicolon is just a character;
//   - a line comment, where everything to end of line is ignored.
//
// A migration that only holds plain DDL parses exactly as before, so the
// existing series is unaffected.
func (m Migration) Statements() []string {
	var (
		out     []string
		current strings.Builder
		// delimiter is the open dollar-quote tag, empty when outside one.
		delimiter string
		single    bool
	)
	body := m.SQL
	for i := 0; i < len(body); i++ {
		c := body[i]

		// A line comment runs to end of line, unless it is inside a quoted
		// region where "--" is literal text.
		if delimiter == "" && !single && c == '-' && i+1 < len(body) && body[i+1] == '-' {
			for i < len(body) && body[i] != '\n' {
				i++
			}
			// Keep the newline so statement text stays line-separated.
			current.WriteByte('\n')
			continue
		}

		if delimiter == "" && c == '\'' {
			// A doubled quote inside a string is an escaped quote, not the end.
			if single && i+1 < len(body) && body[i+1] == '\'' {
				current.WriteString("''")
				i++
				continue
			}
			single = !single
			current.WriteByte(c)
			continue
		}

		if !single && c == '$' {
			if tag, width := scanDollarTag(body[i:]); width > 0 {
				if delimiter == "" {
					delimiter = tag
					current.WriteString(tag)
				} else if tag == delimiter {
					delimiter = ""
					current.WriteString(tag)
				} else {
					current.WriteString(tag)
				}
				i += width - 1
				continue
			}
		}

		if c == ';' && delimiter == "" && !single {
			if statement := strings.TrimSpace(current.String()); statement != "" {
				out = append(out, statement)
			}
			current.Reset()
			continue
		}

		current.WriteByte(c)
	}
	if statement := strings.TrimSpace(current.String()); statement != "" {
		out = append(out, statement)
	}
	return out
}

// scanDollarTag reads a dollar-quote tag at the start of s and reports it with
// its width in bytes, or a zero width when s does not open one.
//
// A tag is "$" optionally followed by an identifier, then "$". The bare "$$" is
// the common form; a named tag exists so a body may contain "$$" as data.
func scanDollarTag(s string) (string, int) {
	if len(s) == 0 || s[0] != '$' {
		return "", 0
	}
	for i := 1; i < len(s); i++ {
		c := s[i]
		if c == '$' {
			return s[:i+1], i + 1
		}
		// An identifier character keeps the tag open; anything else means this
		// was not a dollar quote (a positional parameter, say).
		if !isTagChar(c) {
			return "", 0
		}
	}
	return "", 0
}

// isTagChar reports whether c may appear in a dollar-quote tag. The rule is the
// identifier rule minus the leading character restriction, which the caller
// already applied by requiring the opening "$".
func isTagChar(c byte) bool {
	return c == '_' || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
}

// LatestVersion returns the highest migration version in the series.
func LatestVersion() (int, error) {
	series, err := MigrationSeries()
	if err != nil {
		return 0, err
	}
	return series[len(series)-1].Version, nil
}
