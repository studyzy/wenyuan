package config

import (
	"strings"
	"testing"
)

// The profiling variable holds JSON, and a malformed value is refused rather
// than quietly disabling the profile.
func TestResolveProfile(t *testing.T) {
	cases := []struct {
		raw     string
		want    ProfileSpec
		wantErr string
	}{
		{raw: "", want: ProfileSpec{}},
		{raw: "{}", want: DefaultProfileSpec},
		{raw: `{"every":5,"top":3}`, want: ProfileSpec{Every: 5, Top: 3, Mode: "cprofile"}},
		{raw: `{"every":0}`, wantErr: "'every' must be >= 1"},
		{raw: `{"mode":"pyinstrument"}`, wantErr: "unknown mode"},
		{raw: "true", wantErr: "must be a JSON object"},
		{raw: `{"every":"60"}`, wantErr: "'every' must be an integer"},
	}
	for _, tc := range cases {
		spec, err := Config{Profile: tc.raw}.ResolveProfile()
		if tc.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("%q: err = %v, want %q", tc.raw, err, tc.wantErr)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", tc.raw, err)
		}
		if tc.raw == "" {
			if spec != nil {
				t.Fatalf("an unset value must resolve to no profile")
			}
			continue
		}
		if *spec != tc.want {
			t.Fatalf("%q: spec = %+v, want %+v", tc.raw, *spec, tc.want)
		}
	}
}
