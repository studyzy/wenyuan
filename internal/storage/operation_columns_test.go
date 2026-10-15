package storage

import (
	"strings"
	"testing"
)

// Every statement that returns an operation must return exactly the columns
// scanOperation reads. A shorter list is a run-time error rather than a compile
// error, which is how the cancel and retry paths were broken, so the invariant
// is asserted here.
func TestOperationColumnsMatchTheScanner(t *testing.T) {
	fields := splitTopLevelColumns(operationColumns)
	if len(fields) != 15 {
		t.Fatalf("operationColumns lists %d columns, want the 15 scanOperation reads", len(fields))
	}
	// The scan order matters as much as the count: the first and last columns
	// pin both ends of the list.
	first := strings.TrimSpace(fields[0])
	if first != "operation_id" {
		t.Fatalf("first column = %q", first)
	}
	last := strings.TrimSpace(fields[len(fields)-1])
	if last != "COALESCE(serialization_key,'')" {
		t.Fatalf("last column = %q", last)
	}
	// The expression columns the scanner needs present.
	for _, want := range []string{"result_metadata", "claimed_at", "task_payload", "worker_id"} {
		if !strings.Contains(operationColumns, want) {
			t.Fatalf("operationColumns is missing %q: %s", want, operationColumns)
		}
	}
}

// splitTopLevelColumns divides a select list on its top-level commas, so a
// function call's own comma does not read as a column boundary.
func splitTopLevelColumns(list string) []string {
	var (
		fields []string
		depth  int
		start  int
	)
	for index, char := range list {
		switch char {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				fields = append(fields, list[start:index])
				start = index + 1
			}
		}
	}
	return append(fields, list[start:])
}
