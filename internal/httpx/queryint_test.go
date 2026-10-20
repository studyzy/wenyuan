package httpx

import "testing"

// Contract reference: the framework binds a typed `Query(int, ge=…, le=…)`
// through its lax integer grammar, then compares the parsed Python int against
// the declared bounds. So `1.0`, `1_0` and ` 1 ` are the integers 1, 10 and 1
// rather than parse failures, a real fraction is a parse failure, and a value
// too large for int64 is a bound violation instead of another parse failure.
func TestParseQueryIntReadsTheFrameworkGrammar(t *testing.T) {
	for _, tc := range []struct {
		raw   string
		min   int
		max   int
		value int
		kind  string // "" when the value is accepted
	}{
		{raw: "5", min: 1, max: 100, value: 5},
		{raw: "1.0", min: 1, max: 100, value: 1},
		{raw: "1_0", min: 1, max: 100, value: 10},
		{raw: " 1 ", min: 1, max: 100, value: 1},
		{raw: "+1", min: 1, max: 100, value: 1},
		{raw: "1.5", min: 1, max: 100, kind: "int_parsing"},
		{raw: "abc", min: 1, max: 100, kind: "int_parsing"},
		{raw: "", min: 1, max: 100, kind: "int_parsing"},
		{raw: "0", min: 1, max: 100, kind: "greater_than_equal"},
		{raw: "101", min: 1, max: 100, kind: "less_than_equal"},
		{raw: "99999999999999999999", min: 1, max: 100, kind: "less_than_equal"},
		{raw: "-99999999999999999999", min: 1, max: 100, kind: "greater_than_equal"},
	} {
		value, item, ok := ParseQueryInt(tc.raw, "limit", tc.min, tc.max)
		if tc.kind == "" {
			if !ok || value != tc.value {
				t.Fatalf("%q = (%d, ok=%v), want %d", tc.raw, value, ok, tc.value)
			}
			continue
		}
		if ok {
			t.Fatalf("%q was accepted, want %s", tc.raw, tc.kind)
		}
		if item.Type != tc.kind {
			t.Fatalf("%q type = %q, want %q", tc.raw, item.Type, tc.kind)
		}
		// The framework echoes the raw string the client sent, not the parsed
		// integer.
		if item.Input != tc.raw {
			t.Fatalf("%q input = %v, want the raw string", tc.raw, item.Input)
		}
	}
}

// max 0 is the "no upper bound" spelling the callers use.
func TestParseQueryIntWithNoUpperBound(t *testing.T) {
	if value, _, ok := ParseQueryInt("1000000", "offset", 0, 0); !ok || value != 1000000 {
		t.Fatalf("value = %d ok = %v, want 1000000", value, ok)
	}
}
