package storage

import (
	"fmt"
	"strings"
	"sync"
)

// ObjectKind names the catalogue class an expected object belongs to. The
// classes are the ones the series creates and the ones most different in how
// they are recognised: a table, the columns of a table, a named constraint, a
// named index and a function.
type ObjectKind string

// The object classes the migration series creates.
const (
	ObjectTable      ObjectKind = "table"
	ObjectColumn     ObjectKind = "column"
	ObjectConstraint ObjectKind = "constraint"
	ObjectIndex      ObjectKind = "index"
	ObjectFunction   ObjectKind = "function"
)

// ExpectedObject is one catalogue object the migration series creates.
//
// The inventory of these is what lets a target database be recognised as
// already carrying the schema: the series is the source of truth for what the
// schema is, so its objects are read back out of the series rather than
// declared a second time beside it, where the two could drift apart.
type ExpectedObject struct {
	Kind ObjectKind
	// Table is the owning table for a column or a constraint, empty otherwise.
	Table string
	Name  string
}

// String renders the object the way an operator reads it in an error.
func (o ExpectedObject) String() string {
	switch o.Kind {
	case ObjectColumn, ObjectConstraint:
		return fmt.Sprintf("%s %q of table %q", o.Kind, o.Name, o.Table)
	}
	return fmt.Sprintf("%s %q", o.Kind, o.Name)
}

// ExpectedSchema is the inventory of catalogue objects the embedded series
// creates, in the order the series creates them.
type ExpectedSchema struct {
	objects    []ExpectedObject
	structural []ExpectedObject

	tables      map[string]bool
	columns     map[string]map[string]bool
	constraints map[string]bool
	indexes     map[string]bool
	functions   map[string]bool
}

// Objects returns the inventory in series order. The order is what makes "the
// first missing object" a stable answer, so it is the order the series
// creates them in rather than the catalogue's.
func (e *ExpectedSchema) Objects() []ExpectedObject { return e.objects }

// Structural returns the objects whose absence means a database is not the
// schema this build serves: everything the series declares except its indexes.
//
// An index is left out of the decision because it is a performance object
// rather than part of the shape a query depends on, and because the reference
// does not treat its vector indexes as part of its migration state at all: two
// databases of the reference at the same recorded revision were observed with
// differently named HNSW indexes on the same columns, so a later reference
// binary or an operator created them outside the migrations. Refusing to start
// over one, or building it on a live table during startup, are both worse than
// serving without it.
func (e *ExpectedSchema) Structural() []ExpectedObject { return e.structural }

// HasTable reports whether the inventory expects a table of this name. It is
// how an empty schema is told apart from one that already carries some of the
// series' objects.
func (e *ExpectedSchema) HasTable(name string) bool { return e.tables[name] }

var (
	expectedOnce sync.Once
	expectedVal  *ExpectedSchema
	expectedErr  error
)

// ExpectedSchemaOf parses the embedded migration series into the inventory of
// objects it creates. The parse is cached: the series is embedded, so it can
// only change with the binary.
func ExpectedSchemaOf() (*ExpectedSchema, error) {
	expectedOnce.Do(func() {
		series, err := MigrationSeries()
		if err != nil {
			expectedErr = err
			return
		}
		expectedVal = parseExpectedSchema(series)
		if len(expectedVal.tables) == 0 {
			expectedErr = fmt.Errorf("the migration series creates no tables")
			expectedVal = nil
		}
	})
	return expectedVal, expectedErr
}

// parseExpectedSchema reads the created objects out of a migration series.
func parseExpectedSchema(series []Migration) *ExpectedSchema {
	expected := &ExpectedSchema{
		tables:      map[string]bool{},
		columns:     map[string]map[string]bool{},
		constraints: map[string]bool{},
		indexes:     map[string]bool{},
		functions:   map[string]bool{},
	}
	for _, migration := range series {
		for _, statement := range migration.Statements() {
			expected.addStatement(statement)
		}
	}
	for _, object := range expected.objects {
		if object.Kind != ObjectIndex {
			expected.structural = append(expected.structural, object)
		}
	}
	return expected
}

// addStatement classifies one DDL statement and records the object it creates.
// A statement that creates nothing the inventory tracks (an extension, a
// setting) is ignored: the inventory only has to describe the schema the target
// database would have to already carry.
func (e *ExpectedSchema) addStatement(statement string) {
	words := strings.Fields(statement)
	if len(words) < 3 {
		return
	}
	switch {
	case words[0] == "CREATE" && words[1] == "TABLE":
		e.addTable(trimIdentQuotes(words[2]), tableColumns(statement))
	case words[0] == "ALTER" && words[1] == "TABLE" && len(words) >= 6 &&
		words[3] == "ADD" && words[4] == "CONSTRAINT":
		table, name := trimIdentQuotes(words[2]), trimIdentQuotes(words[5])
		e.constraints[table+"\x00"+name] = true
		e.objects = append(e.objects, ExpectedObject{Kind: ObjectConstraint, Table: table, Name: name})
	case words[0] == "CREATE" && indexStatementName(words) != "":
		name := indexStatementName(words)
		e.indexes[name] = true
		e.objects = append(e.objects, ExpectedObject{Kind: ObjectIndex, Name: name})
	case words[0] == "CREATE" && functionStatementName(words) != "":
		name := functionStatementName(words)
		e.functions[name] = true
		e.objects = append(e.objects, ExpectedObject{Kind: ObjectFunction, Name: name})
	}
}

// addTable records a table and its columns, in declared order.
func (e *ExpectedSchema) addTable(name string, columns []string) {
	e.tables[name] = true
	e.objects = append(e.objects, ExpectedObject{Kind: ObjectTable, Name: name})
	if e.columns[name] == nil {
		e.columns[name] = map[string]bool{}
	}
	for _, column := range columns {
		e.columns[name][column] = true
		e.objects = append(e.objects, ExpectedObject{Kind: ObjectColumn, Table: name, Name: column})
	}
}

// indexStatementName returns the index name of a CREATE INDEX statement, or ""
// when the statement is not one. Both the UNIQUE form and the IF NOT EXISTS
// guard are accepted, because the series may use either.
func indexStatementName(words []string) string {
	position := 1
	if position < len(words) && words[position] == "UNIQUE" {
		position++
	}
	if position >= len(words) || words[position] != "INDEX" {
		return ""
	}
	position++
	if position+2 < len(words) && words[position] == "IF" && words[position+1] == "NOT" && words[position+2] == "EXISTS" {
		position += 3
	}
	if position >= len(words) {
		return ""
	}
	return trimIdentQuotes(words[position])
}

// functionStatementName returns the function name of a CREATE FUNCTION
// statement, or "" when the statement is not one. A name is qualified by the
// migration's search path, so a written-out schema qualification is dropped.
func functionStatementName(words []string) string {
	position := 1
	if position+1 < len(words) && words[position] == "OR" && words[position+1] == "REPLACE" {
		position += 2
	}
	if position >= len(words) || words[position] != "FUNCTION" {
		return ""
	}
	position++
	if position >= len(words) {
		return ""
	}
	name := words[position]
	if opening := strings.Index(name, "("); opening >= 0 {
		name = name[:opening]
	}
	name = trimIdentQuotes(name)
	if dot := strings.LastIndex(name, "."); dot >= 0 {
		name = name[dot+1:]
	}
	return name
}

// tableColumns reads the column names out of a CREATE TABLE statement.
//
// The body is divided on its top-level commas rather than on line breaks: an
// emitted column definition may wrap across lines, because a definition long
// enough to pass the file's width is broken there. A line-based parse would
// read the continuation of one column as the next column.
func tableColumns(statement string) []string {
	opening := strings.Index(statement, "(")
	closing := strings.LastIndex(statement, ")")
	if opening < 0 || closing <= opening {
		return nil
	}
	var columns []string
	for _, piece := range splitTopLevelCommas(statement[opening+1 : closing]) {
		piece = strings.TrimSpace(piece)
		if piece == "" {
			continue
		}
		name := trimIdentQuotes(strings.Fields(piece)[0])
		if isTableConstraintKeyword(strings.ToUpper(name)) {
			continue
		}
		columns = append(columns, name)
	}
	return columns
}

// isTableConstraintKeyword reports whether a column position in a table body
// actually opens a table-level constraint. The series adds its constraints with
// ALTER TABLE, so this is a guard rather than an expectation.
func isTableConstraintKeyword(word string) bool {
	switch word {
	case "CONSTRAINT", "PRIMARY", "UNIQUE", "CHECK", "FOREIGN", "EXCLUDE", "LIKE":
		return true
	}
	return false
}

// splitTopLevelCommas divides a definition list on the commas that sit outside
// every nested parenthesis, quoted string and quoted identifier, so a default
// such as DEFAULT '{"a": 1, "b": 2}'::jsonb is one piece rather than three.
func splitTopLevelCommas(input string) []string {
	var (
		out     []string
		current strings.Builder
		depth   int
		single  bool
		double  bool
	)
	for i := 0; i < len(input); i++ {
		c := input[i]
		switch {
		case single:
			current.WriteByte(c)
			if c == '\'' {
				if i+1 < len(input) && input[i+1] == '\'' {
					current.WriteByte('\'')
					i++
					continue
				}
				single = false
			}
			continue
		case double:
			current.WriteByte(c)
			if c == '"' {
				if i+1 < len(input) && input[i+1] == '"' {
					current.WriteByte('"')
					i++
					continue
				}
				double = false
			}
			continue
		}
		switch c {
		case '\'':
			single = true
		case '"':
			double = true
		case '(':
			depth++
		case ')':
			if depth > 0 {
				depth--
			}
		case ',':
			if depth == 0 {
				out = append(out, current.String())
				current.Reset()
				continue
			}
		}
		current.WriteByte(c)
	}
	if strings.TrimSpace(current.String()) != "" {
		out = append(out, current.String())
	}
	return out
}

// trimIdentQuotes removes the quoting around an SQL identifier. An unquoted
// name is returned unchanged; a doubled quote inside a quoted name is folded
// back to one.
func trimIdentQuotes(name string) string {
	if len(name) < 2 || name[0] != '"' || name[len(name)-1] != '"' {
		return name
	}
	return strings.ReplaceAll(name[1:len(name)-1], `""`, `"`)
}
