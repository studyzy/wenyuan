package httpx

import (
	"strconv"
	"strings"
)

// PythonFloat is a float that renders the way CPython's repr does: the shortest
// decimal that round-trips, always with a fractional part or an exponent. The
// reference's response models carry plain Python floats, so a score of 1 comes
// back as `1.0` rather than Go's `1` — a client that reads the field as a JSON
// number sees the same token either way, but a byte comparison does not.
type PythonFloat float64

// MarshalJSON writes the Python rendering of the value.
func (f PythonFloat) MarshalJSON() ([]byte, error) {
	return []byte(formatPythonFloat(float64(f))), nil
}

// formatPythonFloat renders a float the way CPython's repr does: in fixed
// notation while the decimal exponent is in [-4, 16), and in scientific notation
// (signed, at least two exponent digits) outside it. FormatFloat's 'e' reads the
// exponent because Go's 'g'/'f' switch to exponents on different bounds than
// Python's repr does.
func formatPythonFloat(value float64) string {
	scientific := strconv.FormatFloat(value, 'e', -1, 64)
	_, exponent, ok := strings.Cut(scientific, "e")
	if !ok {
		return strconv.FormatFloat(value, 'f', -1, 64)
	}
	exp, err := strconv.Atoi(exponent)
	if err != nil || exp < -4 || exp >= 16 {
		return scientific
	}
	fixed := strconv.FormatFloat(value, 'f', -1, 64)
	if !strings.Contains(fixed, ".") {
		fixed += ".0"
	}
	return fixed
}
