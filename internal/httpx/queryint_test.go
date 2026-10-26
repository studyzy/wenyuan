package httpx

import (
	"math"
	"testing"
)

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

// A value past int64 with no declared upper bound reaches the store rather than
// being refused here.
//
// The reference declares such a parameter as a bare `int` (`offset: int =
// Query(default=0, ge=0)`), compares only the declared bound — which an
// arbitrarily large Python int satisfies — and hands the value to its driver,
// so the answer is a 500 from the driver that reads it. Refusing it with a 422
// here produced an envelope no reference route ever sends: `GET
// /v1/default/banks/{b}/documents?offset=9223372036854775808` answered 422
// `int_parsing` where the reference answers 500. The parser now saturates, so
// the query executes as a page past the end instead.
func TestParseQueryIntWithoutUpperBoundSaturatesPastInt64(t *testing.T) {
	for _, raw := range []string{
		"9223372036854775808",       // MaxInt64 + 1
		"99999999999999999999999",   // far past int64
		"9_223_372_036_854_775_808", // turns past int64
		"9223372036854775808.0",     // turns past int64, integral fraction
	} {
		value, item, ok := ParseQueryInt(raw, "offset", 0, 0)
		if !ok {
			t.Fatalf("%q was refused as %q, want it carried past int64 to the store", raw, item.Type)
		}
		if value != math.MaxInt {
			t.Fatalf("%q = %d, want the saturated maximum %d", raw, value, math.MaxInt)
		}
	}
}

// A declared upper bound still refuses the same value with the framework's own
// bound violation, because there the reference compares the Python int against
// that bound before the store is reached.
func TestParseQueryIntWithUpperBoundRefusesPastInt64(t *testing.T) {
	if _, item, ok := ParseQueryInt("9223372036854775808", "limit", 0, 1000); ok || item.Type != "less_than_equal" {
		t.Fatalf("type = %q ok = %v, want less_than_equal", item.Type, ok)
	}
	// A negative value past int64 still violates ge even with no upper bound.
	if _, item, ok := ParseQueryInt("-9223372036854775809", "offset", 0, 0); ok || item.Type != "greater_than_equal" {
		t.Fatalf("type = %q ok = %v, want greater_than_equal", item.Type, ok)
	}
}
