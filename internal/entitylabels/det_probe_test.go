package entitylabels

import (
	"strings"
	"testing"
)

// The reference's migration is `dict(raw)` then `patched["type"] = ...`
// (engine/retain/entity_labels.py:90-104), so a rejected group's printed dict
// lists the caller's keys in the order the client wrote them, with `type`
// appended. Reverting to sorted order makes every non-alphabetical case below
// report the wrong member order.
func TestShapeProblemsPrintsTheCallersMemberOrder(t *testing.T) {
	cases := []struct {
		name   string
		order  []string
		body   map[string]any
		wanted string
	}{
		{
			name:   "written out of alphabetical order",
			order:  []string{"tag", "optional"},
			body:   map[string]any{"tag": "t", "optional": "x"},
			wanted: `{'tag': 't', 'optional': 'x', 'type': 'value'}`,
		},
		{
			name:   "written in alphabetical order",
			order:  []string{"optional", "tag"},
			body:   map[string]any{"tag": "t", "optional": "x"},
			wanted: `{'optional': 'x', 'tag': 't', 'type': 'value'}`,
		},
		{
			name:   "three members out of order",
			order:  []string{"b", "a", "c"},
			body:   map[string]any{"a": "2", "b": "1", "c": "3"},
			wanted: `{'b': '1', 'a': '2', 'c': '3', 'type': 'value'}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			report := ShapeProblemsInOrder(map[string]any{"attributes": []any{tc.body}}, [][]string{tc.order})
			rendered := strings.Join(report, "\n")
			if !strings.Contains(rendered, "input_value="+tc.wanted) {
				t.Fatalf("report =\n%s\nwant input_value=%s", rendered, tc.wanted)
			}
		})
	}
}

// Without a caller order the report is still deterministic; it just falls back
// to sorted members. Running the same value many times must not vary: the map
// iteration order Go randomises must never reach the wire.
func TestShapeProblemsIsDeterministicWithoutACallerOrder(t *testing.T) {
	body := map[string]any{"tag": "t", "optional": "x"}
	first := strings.Join(ShapeProblems(map[string]any{"attributes": []any{body}}), "\n")
	if !strings.Contains(first, `{'optional': 'x', 'tag': 't', 'type': 'value'}`) {
		t.Fatalf("fallback report =\n%s\nwant sorted members", first)
	}
	for i := range 500 {
		if got := strings.Join(ShapeProblems(map[string]any{"attributes": []any{body}}), "\n"); got != first {
			t.Fatalf("call %d rendered differently:\n  %s\nvs\n  %s", i, got, first)
		}
	}
}

// A member the caller order cannot name still prints, after the ones it can.
func TestMigrationDictOrderKeepsUnnamedMembers(t *testing.T) {
	migrated := map[string]any{"b": 1, "a": 2, "type": "value"}
	got := strings.Join(migrationDictOrder(migrated, []string{"b"}), ",")
	if want := "b,a,type"; got != want {
		t.Fatalf("order = %q, want %q", got, want)
	}
}
