package storage

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// AdminColumn is one column of a persistent table as the admin backup and
// restore commands see it: the name and the PostgreSQL type name. The type is
// recorded so a restore can refuse a backup whose shape no longer matches the
// target before it truncates anything.
type AdminColumn struct {
	Name string `json:"name"`
	Type string `json:"type_name"`
}

// AdminTableStore is the optional store capability behind the admin backup and
// restore commands. It exists so those commands do not have to reach into a
// concrete backend, and so the in-memory backend can decline them: it holds no
// schema, so it cannot enumerate a table set or reproduce PostgreSQL's types.
//
// PostgreSQL implements it by reading its catalogue and moving whole tables
// through JSON, which the same binary can read back without a new dependency.
type AdminTableStore interface {
	// TableColumns returns the table's insertable columns in declaration order.
	TableColumns(ctx context.Context, table string) ([]AdminColumn, error)
	// DumpTable returns every row of the table as a JSON array of objects.
	DumpTable(ctx context.Context, table string) ([]byte, error)
	// LoadTable inserts the rows of a JSON array into the table. It advances
	// any identity sequence past the restored ids so a later insert cannot
	// collide with a row the backup carried.
	LoadTable(ctx context.Context, table string, data []byte) error
	// TruncateTables removes every row of the named tables in one statement.
	//
	// The set travels together because PostgreSQL refuses to truncate a table
	// that another table still references unless that table is part of the same
	// command: truncating them one at a time fails as soon as any two of them
	// are joined by a foreign key, however well ordered the calls are. A single
	// statement also needs no CASCADE, so a table outside the set is never
	// emptied by surprise.
	TruncateTables(ctx context.Context, tables []string) error
}

// The migration series is the authority for the persistent table set. These
// patterns read it rather than a second hand-maintained list that could drift
// the moment a migration adds a table.
var (
	createTablePattern = regexp.MustCompile(`(?i)\bCREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)`)
	foreignKeyPattern  = regexp.MustCompile(`(?i)\bALTER\s+TABLE\s+("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s+ADD\s+CONSTRAINT\s+("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)\s+FOREIGN\s+KEY\s*\([^)]*\)\s*REFERENCES\s+("[^"]+"|[A-Za-z_][A-Za-z0-9_]*)`)
)

// PersistentTables returns every persistent table the migration series creates,
// in foreign-key dependency order: a table appears after every table it
// references. A backup writes in this order and a restore loads in it, so every
// row's references exist by the time it is inserted. Clearing the target is one
// statement over the whole set, which is what PostgreSQL requires — see
// AdminTableStore.TruncateTables.
//
// The set is derived from the migrations themselves. Any table the memory
// domain declares but no migration creates is appended afterwards, so the two
// can never silently disagree.
func PersistentTables() ([]string, error) {
	series, err := MigrationSeries()
	if err != nil {
		return nil, err
	}
	var created []string
	seen := map[string]bool{}
	// references maps a child table to the tables it points at.
	references := map[string][]string{}
	for _, migration := range series {
		for _, match := range createTablePattern.FindAllStringSubmatch(migration.SQL, -1) {
			name := unquoteIdentifier(match[1])
			if !seen[name] {
				seen[name] = true
				created = append(created, name)
			}
		}
		for _, match := range foreignKeyPattern.FindAllStringSubmatch(migration.SQL, -1) {
			child := unquoteIdentifier(match[1])
			parent := unquoteIdentifier(match[3])
			references[child] = append(references[child], parent)
		}
	}
	for _, name := range MemoryDomainTables {
		if !seen[name] {
			seen[name] = true
			created = append(created, name)
		}
	}
	if len(created) == 0 {
		return nil, fmt.Errorf("no persistent tables found in the migration series")
	}
	return dependencyOrder(created, references)
}

// dependencyOrder topologically sorts tables so each follows everything it
// references. Ties are broken by the original order, so the result is stable
// from run to run (a backup and its restore must agree on the order).
func dependencyOrder(tables []string, references map[string][]string) ([]string, error) {
	index := make(map[string]int, len(tables))
	for i, name := range tables {
		index[name] = i
	}
	children := map[string][]string{}
	inDegree := map[string]int{}
	for _, name := range tables {
		inDegree[name] = 0
	}
	for _, child := range tables {
		distinct := map[string]bool{}
		for _, parent := range references[child] {
			// A self-reference (knowledge_pages points at itself) says nothing
			// about ordering, and a reference to a table outside the set is not
			// ours to order.
			if parent == child || distinct[parent] {
				continue
			}
			if _, ok := index[parent]; !ok {
				continue
			}
			distinct[parent] = true
			children[parent] = append(children[parent], child)
			inDegree[child]++
		}
	}

	ready := make([]string, 0, len(tables))
	for _, name := range tables {
		if inDegree[name] == 0 {
			ready = append(ready, name)
		}
	}
	sortByTableIndex(ready, index)

	ordered := make([]string, 0, len(tables))
	for len(ready) > 0 {
		name := ready[0]
		ready = ready[1:]
		ordered = append(ordered, name)
		for _, child := range children[name] {
			inDegree[child]--
			if inDegree[child] == 0 {
				ready = append(ready, child)
			}
		}
		sortByTableIndex(ready, index)
	}
	if len(ordered) != len(tables) {
		return nil, fmt.Errorf("persistent table foreign-key graph contains a cycle")
	}
	return ordered, nil
}

// sortByTableIndex orders a ready set by the table's original position.
func sortByTableIndex(names []string, index map[string]int) {
	sort.SliceStable(names, func(i, j int) bool { return index[names[i]] < index[names[j]] })
}

// unquoteIdentifier strips the surrounding double quotes a generated migration
// may put around an identifier and unescapes any doubled quote inside it.
func unquoteIdentifier(raw string) string {
	if len(raw) >= 2 && strings.HasPrefix(raw, `"`) && strings.HasSuffix(raw, `"`) {
		return strings.ReplaceAll(raw[1:len(raw)-1], `""`, `"`)
	}
	return raw
}
