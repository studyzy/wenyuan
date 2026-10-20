package entitylabels

import (
	"encoding/json"
	"strings"
	"testing"
)

// The reference validates an entity_labels value with its model and answers the
// model's own report. These cases are the ones our previous shape check let
// through: each must be rejected, with the report text the reference produces.
// Reverting to a loose shape check makes every case below "accepted".
func TestShapeProblemsRejectsWhatTheReferenceRejects(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{
			name: "a group that is not an object",
			in:   `{"attributes":["str"]}`,
			want: "1 validation error for LabelGroup\n  Input should be a valid dictionary or instance of LabelGroup" +
				" [type=model_type, input_value='str', input_type=str]",
		},
		{
			name: "a non-string key",
			in:   `{"attributes":[{"key":1}]}`,
			want: "1 validation error for LabelGroup\nkey\n  Input should be a valid string" +
				" [type=string_type, input_value=1, input_type=int]",
		},
		{
			name: "a null type is a literal mismatch, not a type error",
			in:   `{"attributes":[{"key":"k","type":null}]}`,
			want: "1 validation error for LabelGroup\ntype\n  Input should be 'value', 'multi-values', 'text', 'multi-text' or 'map'" +
				" [type=literal_error, input_value=None, input_type=NoneType]",
		},
		{
			name: "a null values list",
			in:   `{"attributes":[{"key":"k","values":null}]}`,
			want: "1 validation error for LabelGroup\nvalues\n  Input should be a valid list" +
				" [type=list_type, input_value=None, input_type=NoneType]",
		},
		{
			name: "a non-string values description",
			in:   `{"attributes":[{"key":"k","values":[{"value":"v","description":1}]}]}`,
			want: "1 validation error for LabelGroup\nvalues.0.description\n  Input should be a valid string" +
				" [type=string_type, input_value=1, input_type=int]",
		},
		{
			name: "a map field with a non-string description",
			in:   `{"attributes":[{"key":"k","type":"map","fields":{"f":{"description":1}}}]}`,
			want: "1 validation error for LabelGroup\nfields.f.description\n  Input should be a valid string" +
				" [type=string_type, input_value=1, input_type=int]",
		},
		{
			name: "a non-boolean optional",
			in:   `{"attributes":[{"key":"k","optional":"x"}]}`,
			want: "1 validation error for LabelGroup\noptional\n  Input should be a valid boolean, unable to interpret input" +
				" [type=bool_parsing, input_value='x', input_type=str]",
		},
		{
			name: "a non-boolean tag",
			in:   `{"attributes":[{"key":"k","tag":"x"}]}`,
			want: "1 validation error for LabelGroup\ntag\n  Input should be a valid boolean, unable to interpret input" +
				" [type=bool_parsing, input_value='x', input_type=str]",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var raw any
			if err := json.Unmarshal([]byte(tc.in), &raw); err != nil {
				t.Fatalf("input %s: %v", tc.in, err)
			}
			report := ShapeProblems(raw)
			joined := strings.Join(report, "\n")
			if !strings.Contains(joined, tc.want) {
				t.Fatalf("report =\n%s\nwant it to contain\n%s", joined, tc.want)
			}
		})
	}
}

// A value the model accepts produces no report, so the shape gate cannot reject
// a valid vocabulary.
func TestShapeProblemsAcceptsAValidVocabulary(t *testing.T) {
	for _, in := range []string{
		`{"attributes":[{"key":"k"}]}`,
		`{"attributes":[{"key":"k","type":"value","values":[{"value":"v"}]}]}`,
		`{"attributes":[{"key":"k","type":"map","fields":{"f":{"type":"text"}}}]}`,
		`[{"key":"k"}]`,
		`{"attributes":[]}`,
	} {
		var raw any
		if err := json.Unmarshal([]byte(in), &raw); err != nil {
			t.Fatalf("input %s: %v", in, err)
		}
		if report := ShapeProblems(raw); len(report) > 0 {
			t.Fatalf("input %s: report = %v", in, report)
		}
	}
}

// A legacy group names free_values or multi_value instead of a type; the
// migration supplies one, and the report must show the migrated dict the model
// saw, with the migration's `type` last.
func TestShapeProblemsReportsTheMigratedGroup(t *testing.T) {
	var raw any
	if err := json.Unmarshal([]byte(`{"attributes":[{"values":[]}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	report := strings.Join(ShapeProblems(raw), "\n")
	want := "input_value={'values': [], 'type': 'value'}"
	if !strings.Contains(report, want) {
		t.Fatalf("report =\n%s\nwant it to contain\n%s", report, want)
	}
}
