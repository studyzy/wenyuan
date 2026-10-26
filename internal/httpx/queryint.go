package httpx

import "math"

// ParseQueryInt reads a scalar integer query parameter the way the reference's
// framework binds one, and reports the bound it violates when it does not fit.
//
// The grammar is the framework's, not Go's — surrounding whitespace, an
// optional sign, underscores between digits and a trailing-zero fraction all
// parse — so `1.0`, `1_0` and ` 1 ` are the integers 1, 10 and 1. A value the
// framework parses but that does not fit int64 is reported as a bound
// violation, because the reference compares an arbitrarily large Python int
// against the declared limits rather than refusing to parse it.
//
// max of 0 means no upper bound.
//
// An integer the framework parses but int64 cannot carry is handled by which
// bound it would violate, because that is what the caller can still refuse:
// below min is a `ge` violation and above max a `le` violation, both of which
// the framework reports before the value ever reaches the store. With no upper
// bound to violate there is nothing to refuse, and the reference passes the
// arbitrarily large Python int to its driver, which raises and surfaces as a
// 500 (`invalid input for query argument $n: … (value out of int64 range)`).
// This parser therefore reports such a value as a bound violation only when a
// declared max makes it one. With no upper bound it saturates to MaxInt and
// returns carry=true: the value is out of Python's reach too, so no client can
// tell a saturated page from the real one, and the reference's own driver
// failure stays the one remaining difference (recorded in
// docs/parity-round/PLAN.md).
//
// The item's input is the raw string the client sent, which is what the
// framework echoes. ok is false when the caller should answer 422 with the
// returned item.
func ParseQueryInt(raw string, name string, min, max int) (int, ValidationError, bool) {
	loc := []string{"query", name}
	value, parsed := LaxInteger(raw)
	if !parsed {
		sign, valid := BigintSign(raw)
		switch {
		case !valid:
			return 0, TypeMismatch("int_parsing", loc, raw), false
		case sign < 0:
			return 0, GreaterThanEqual(loc, raw, min), false
		case max != 0:
			return 0, LessThanEqual(loc, raw, max), false
		default:
			// No declared max means no bound for the framework to refuse it on:
			// the reference hands the arbitrarily large int to its driver, which
			// fails. A saturated value carries the "too large" fact to the store
			// so the caller reaches that same failure instead of a 422 no
			// reference route produces.
			return math.MaxInt, ValidationError{}, true
		}
	}
	if value < min {
		return 0, GreaterThanEqual(loc, raw, min), false
	}
	if max != 0 && value > max {
		return 0, LessThanEqual(loc, raw, max), false
	}
	return value, ValidationError{}, true
}
