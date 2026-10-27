package config

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ProfileSpec is the reference's profiling request, parsed from
// WENYUAN_API_PROFILE.
//
// The variable holds JSON, the shape this codebase uses for structured env
// config:
//
//	WENYUAN_API_PROFILE='{"every": 60, "top": 20}'
//
// every  seconds between emissions (default 60)
// top    rows per emission (default 20)
// mode   "cprofile" (default); reserved so another mode can be added later
type ProfileSpec struct {
	// Every is the seconds between emissions.
	Every int
	// Top is the rows per emission.
	Top int
	// Mode selects the report shape.
	Mode string
}

// DefaultProfileSpec is the configuration a bare `{}` resolves to.
var DefaultProfileSpec = ProfileSpec{Every: 60, Top: 20, Mode: "cprofile"}

// ResolveProfile parses the profiling request. An unset value means profiling is
// off.
//
// A malformed value is an error rather than a silent disable: whoever set it
// wants a profile, and a typo that produces no output costs a whole test cycle
// to notice.
func (c Config) ResolveProfile() (*ProfileSpec, error) {
	raw := strings.TrimSpace(c.Profile)
	if raw == "" {
		return nil, nil
	}
	var fields map[string]any
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object: %w", EnvProfile, err)
	}
	spec := DefaultProfileSpec
	if every, ok := fields["every"]; ok {
		value, err := wholeNumber(every)
		if err != nil {
			return nil, fmt.Errorf("%s: 'every' must be an integer", EnvProfile)
		}
		spec.Every = value
	}
	if spec.Every < 1 {
		return nil, fmt.Errorf("%s: 'every' must be >= 1", EnvProfile)
	}
	if mode, ok := fields["mode"]; ok {
		spec.Mode = fmt.Sprint(mode)
	}
	if spec.Mode != "cprofile" {
		return nil, fmt.Errorf("%s: unknown mode %q", EnvProfile, spec.Mode)
	}
	if top, ok := fields["top"]; ok {
		value, err := wholeNumber(top)
		if err != nil {
			return nil, fmt.Errorf("%s: 'top' must be an integer", EnvProfile)
		}
		spec.Top = value
	}
	return &spec, nil
}

// wholeNumber reads a JSON number as an integer, rejecting a fractional value.
func wholeNumber(value any) (int, error) {
	number, ok := value.(float64)
	if !ok {
		return 0, fmt.Errorf("not a number")
	}
	if number != float64(int(number)) {
		return 0, fmt.Errorf("not a whole number")
	}
	return int(number), nil
}
