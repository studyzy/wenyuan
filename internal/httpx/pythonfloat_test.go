package httpx

import (
	"encoding/json"
	"testing"
)

// The reference's response models carry plain Python floats, so a whole number
// renders with a fractional part and a very large or small one in Python's
// scientific form. A Go float64 renders `1` for the same value, which is a
// different token on the wire.
func TestPythonFloatRendersLikeCPythonRepr(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{1, "1.0"},
		{0, "0.0"},
		{0.5, "0.5"},
		{1.0 / 3.0, "0.3333333333333333"},
		{1e15, "1000000000000000.0"},
		{1e16, "1e+16"},
		{0.0001, "0.0001"},
		{0.00001, "1e-05"},
		{-2.5, "-2.5"},
	}
	for _, tc := range cases {
		got, err := json.Marshal(PythonFloat(tc.in))
		if err != nil {
			t.Fatalf("%v: %v", tc.in, err)
		}
		if string(got) != tc.want {
			t.Errorf("python float %v = %s, want %s", tc.in, got, tc.want)
		}
	}
}
