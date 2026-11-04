package tenant

import (
	"encoding/json"
	"testing"
)

// TestRelaxJSONReadsWhatAModelWrites pins the two relaxations a live round
// tripped over: a reply carrying a // comment failed the strict decode with
// "invalid character '/' after object key:value pair", and a trailing comma
// fails it the same way. Neither changes the answer, so both are dropped.
func TestRelaxJSONReadsWhatAModelWrites(t *testing.T) {
	cases := map[string]string{
		"line comment": `{
  "facts": [ // the updated list
    {"text": "x"}, // kept
  ]
}`,
		"block comment":  `{"facts": [/* first */ {"text": "x"}]}`,
		"trailing comma": `{"facts": [{"text": "x"},]}`,
		"trailing comma in object": `{"facts": [
	{"text": "x",}
]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			var decoded struct {
				Facts []struct {
					Text string `json:"text"`
				} `json:"facts"`
			}
			if err := json.Unmarshal([]byte(RelaxJSON(raw)), &decoded); err != nil {
				t.Fatalf("relaxed text does not decode: %v", err)
			}
			if len(decoded.Facts) != 1 || decoded.Facts[0].Text != "x" {
				t.Fatalf("decoded = %+v, want one fact", decoded.Facts)
			}
		})
	}
}

// TestRelaxJSONLeavesStringContentsAlone keeps the one thing a scanner could
// break: a URL carries "//" and a fact's text may carry a comma before its
// closing brace.
func TestRelaxJSONLeavesStringContentsAlone(t *testing.T) {
	cases := map[string]string{
		"url":           `{"facts":[{"text":"see https://example.test/a//b"}]}`,
		"comma":         `{"facts":[{"text":"a, b"}]}`,
		"closing brace": `{"facts":[{"text":"ends with }"}]}`,
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if got := RelaxJSON(raw); got != raw {
				t.Fatalf("RelaxJSON changed %q to %q", raw, got)
			}
		})
	}
}

// TestRelaxJSONIsIdempotent pins that a reply needs relaxing at most once.
func TestRelaxJSONIsIdempotent(t *testing.T) {
	raw := `{"facts": [{"text": "x"},] // tail`
	once := RelaxJSON(raw)
	if twice := RelaxJSON(once); twice != once {
		t.Fatalf("RelaxJSON is not idempotent: %q then %q", once, twice)
	}
}
