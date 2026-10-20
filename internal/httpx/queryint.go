package httpx

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
// max of 0 means no upper bound. The item's input is the raw string the client
// sent, which is what the framework echoes. ok is false when the caller should
// answer 422 with the returned item.
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
			// No bound to violate, and the value cannot be carried: the
			// reference hands it to the store, which fails.
			return 0, TypeMismatch("int_parsing", loc, raw), false
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
