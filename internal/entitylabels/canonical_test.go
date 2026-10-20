package entitylabels

import (
	"encoding/json"
	"testing"
)

// decodeVocabulary parses JSON the way the config route does: through a generic
// decode, so every object arrives as a map with no key order.
func decodeVocabulary(t *testing.T, text string) any {
	t.Helper()
	var raw any
	if err := json.Unmarshal([]byte(text), &raw); err != nil {
		t.Fatalf("input %s: %v", text, err)
	}
	return raw
}

// The reference validates the caller's value with its model and then stores the
// caller's own dict, so a stored group carries exactly the members the caller
// sent — its serialiser's order, its defaults, and no `type` the legacy
// migration merely supplied to validate. Reverting any of that changes the bytes
// this asserts.
func TestCanonicalKeepsTheCallersMembersAndTheSerialiserOrder(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "a group naming only its key keeps only its key",
			in:   `{"attributes":[{"key":"k"}]}`,
			want: `{"attributes":[{"key":"k"}]}`,
		},
		{
			name: "the serialiser's declaration order",
			in:   `{"attributes":[{"optional":false,"tag":true,"values":[{"description":"d","value":"a"}],"key":"k","type":"value"}]}`,
			want: `{"attributes":[{"key":"k","type":"value","optional":false,"tag":true,"values":[{"value":"a","description":"d"}]}]}`,
		},
		{
			name: "a default member the caller set is dropped",
			in:   `{"attributes":[{"key":"k","optional":true,"tag":false}]}`,
			want: `{"attributes":[{"key":"k"}]}`,
		},
		{
			name: "map fields nest in the same order",
			in:   `{"attributes":[{"key":"k","type":"map","fields":{"f":{"values":[{"value":"z"}],"type":"value"}}}]}`,
			want: `{"attributes":[{"key":"k","type":"map","fields":{"f":{"type":"value","values":[{"value":"z"}]}}}]}`,
		},
		{
			name: "an empty attributes list is still stored",
			in:   `{"attributes":[]}`,
			want: `{"attributes":[]}`,
		},
		{
			name: "a bare list is wrapped the same way",
			in:   `[{"key":"k","type":"text"}]`,
			want: `{"attributes":[{"key":"k","type":"text"}]}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			encoded, err := json.Marshal(Canonical(decodeVocabulary(t, tc.in)))
			if err != nil {
				t.Fatal(err)
			}
			if string(encoded) != tc.want {
				t.Fatalf("stored value =\n  %s\nwant\n  %s", encoded, tc.want)
			}
		})
	}
}

// A value read back from the jsonb column has lost its member order, so the
// read path re-states it. Without that the response lists the members
// alphabetically, which the reference never does.
func TestReorderRestatesTheSerialiserOrderAfterAStorageRoundTrip(t *testing.T) {
	const stored = `{"attributes":[{"key":"k","type":"value","optional":false,"tag":true,` +
		`"values":[{"value":"a","description":"d"}],"fields":{"f":{"type":"value"}}}]}`
	var roundTripped any
	if err := json.Unmarshal([]byte(stored), &roundTripped); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(Reorder(roundTripped))
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != stored {
		t.Fatalf("read-back value =\n  %s\nwant\n  %s", encoded, stored)
	}
}

// The config read path only re-states the order of a value a model declares;
// any other value passes through untouched, so unrelated overrides keep their
// own encoding.
func TestReorderLeavesOtherValuesAlone(t *testing.T) {
	value := map[string]any{"recall_budget_min": 50, "retain_mission": "m"}
	reordered, ok := Reorder(value).(map[string]any)
	if !ok {
		t.Fatalf("Reorder changed the type of a plain object: %T", Reorder(value))
	}
	if len(reordered) != len(value) {
		t.Fatalf("members = %v", reordered)
	}
}
