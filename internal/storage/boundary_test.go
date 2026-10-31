package storage

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The memory-domain table names are only allowed to appear inside the storage
// area. Any other package that names them directly violates the boundary rule.
func TestNoMemoryDomainTableReferencesOutsideStorage(t *testing.T) {
	root := moduleRoot(t)
	violations := make([]string, 0)

	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			// A nested checkout is another tree's source, not this module's: a git
			// worktree under .agents/ carries its own internal/storage, and holding
			// this tree to that one's contents would report violations nobody here
			// wrote.
			if info.Name() == ".git" || info.Name() == "vendor" || info.Name() == "worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		relative, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		if strings.HasPrefix(filepath.ToSlash(relative), "internal/storage/") {
			return nil
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, table := range MemoryDomainTables {
			if mentionsTable(string(content), table) {
				violations = append(violations, filepath.ToSlash(relative)+" references "+table)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(violations) > 0 {
		t.Fatalf("memory-domain tables referenced outside the storage area:\n%s", strings.Join(violations, "\n"))
	}
}

// mentionsTable looks for an SQL-shaped reference so that a JSON field name
// that happens to equal a table name does not trip the boundary check.
func mentionsTable(content, table string) bool {
	candidates := []string{
		"FROM " + table,
		"INTO " + table,
		"UPDATE " + table,
		"JOIN " + table,
		"TABLE " + table,
		"TABLE IF NOT EXISTS " + table,
	}
	for _, candidate := range candidates {
		if strings.Contains(content, candidate) {
			return true
		}
	}
	return false
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate the module root")
		}
		dir = parent
	}
}
