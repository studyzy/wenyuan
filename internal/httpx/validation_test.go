package httpx

import (
	"encoding/json"
	"strings"
	"testing"
)

// wireItem is the on-the-wire shape of one error item, with list positions
// decoded as numbers.
type wireItem struct {
	Type  string         `json:"type"`
	Loc   []any          `json:"loc"`
	Msg   string         `json:"msg"`
	Input any            `json:"input"`
	Ctx   map[string]any `json:"ctx"`
}

// itemKeys returns the keys one marshalled item actually carries, so a test can
// assert the exact key set rather than only the keys it knows about.
func itemKeys(t *testing.T, item ValidationError) map[string]json.RawMessage {
	t.Helper()
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	return keys
}

func marshalItem(t *testing.T, item ValidationError) wireItem {
	t.Helper()
	raw, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	var out wireItem
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// The framework builds its items with include_url=False, so every item carries
// the five keys the served contract document declares and nothing else — in
// particular no documentation link.
func TestItemsCarryTheReferenceKeySet(t *testing.T) {
	cases := []struct {
		name       string
		item       ValidationError
		wantKeys   []string
		wantAbsent []string
	}{
		{
			"missing",
			Missing([]string{"body", "x"}, map[string]any{}),
			[]string{"type", "loc", "msg", "input"},
			[]string{"ctx", "url"},
		},
		{
			"greater_than_equal",
			GreaterThanEqual([]string{"body", "x"}, 0, 1),
			[]string{"type", "loc", "msg", "input", "ctx"},
			[]string{"url"},
		},
		{
			"less_than_equal",
			LessThanEqual([]string{"body", "x"}, 9, 5),
			[]string{"type", "loc", "msg", "input", "ctx"},
			[]string{"url"},
		},
		{
			"string_too_short",
			StringTooShort([]string{"body", "x"}, "", 1),
			[]string{"type", "loc", "msg", "input", "ctx"},
			[]string{"url"},
		},
		{"enum", Enum([]string{"body", "x"}, "z", []string{"a", "b"}), []string{"type", "loc", "msg", "input", "ctx"}, []string{"url"}},
		{"literal_error", Literal([]string{"body", "x"}, "z", []string{"a", "b"}), []string{"type", "loc", "msg", "input", "ctx"}, []string{"url"}},
		{"value_error", ValueError([]string{"body", "x"}, "z", "boom"), []string{"type", "loc", "msg", "input", "ctx"}, []string{"url"}},
		{"int_parsing", TypeMismatch("int_parsing", []string{"body", "x"}, "z"), []string{"type", "loc", "msg", "input"}, []string{"ctx", "url"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys := itemKeys(t, tc.item)
			for _, want := range tc.wantKeys {
				if _, ok := keys[want]; !ok {
					t.Fatalf("item is missing key %q: %v", want, keys)
				}
			}
			for _, absent := range tc.wantAbsent {
				if _, ok := keys[absent]; ok {
					t.Fatalf("item must not carry key %q: %v", absent, keys)
				}
			}
			if got := marshalItem(t, tc.item).Type; got != tc.name {
				t.Fatalf("type = %q, want %q", got, tc.name)
			}
		})
	}
}

// An item always names its input, a bound carries its context, and a custom
// validator's message is prefixed the way the framework prefixes it.
func TestItemShapeMatchesFramework(t *testing.T) {
	bound := marshalItem(t, LessThanEqual([]string{"body", "limit"}, 500, 5))
	if bound.Msg != "Input should be less than or equal to 5" {
		t.Fatalf("msg = %q", bound.Msg)
	}
	if le, ok := bound.Ctx["le"].(float64); !ok || le != 5 {
		t.Fatalf("ctx = %#v", bound.Ctx)
	}
	if bound.Input != float64(500) {
		t.Fatalf("input = %#v", bound.Input)
	}

	missing := marshalItem(t, Missing([]string{"body", "items"}, map[string]any{}))
	if input, ok := missing.Input.(map[string]any); !ok || len(input) != 0 {
		t.Fatalf("missing input = %#v, want an empty object", missing.Input)
	}
	if missing.Ctx != nil {
		t.Fatalf("missing item must carry no context, got %#v", missing.Ctx)
	}

	custom := marshalItem(t, ValueError([]string{"body", "when"}, "x", "bad value"))
	if custom.Msg != "Value error, bad value" {
		t.Fatalf("msg = %q", custom.Msg)
	}
	errorCtx, ok := custom.Ctx["error"].(map[string]any)
	if !ok || len(errorCtx) != 0 {
		t.Fatalf("ctx.error = %#v, want the empty object jsonable_encoder leaves of the exception", custom.Ctx)
	}

	enum := marshalItem(t, Enum([]string{"body", "budget"}, "z", []string{"low", "mid", "high"}))
	if enum.Msg != "Input should be 'low', 'mid' or 'high'" {
		t.Fatalf("msg = %q", enum.Msg)
	}
	if expected, _ := enum.Ctx["expected"].(string); expected != "'low', 'mid' or 'high'" {
		t.Fatalf("ctx.expected = %v", enum.Ctx["expected"])
	}
}

// The wording of a wrong-typed value is the framework's, and it depends on the
// declared type and on the JSON kind that was sent.
func TestTypeMessageVocabulary(t *testing.T) {
	cases := map[string]string{
		"int_parsing":    "Input should be a valid integer, unable to parse string as an integer",
		"int_type":       "Input should be a valid integer",
		"int_from_float": "Input should be a valid integer, got a number with a fractional part",
		"float_parsing":  "Input should be a valid number, unable to parse string as a number",
		"float_type":     "Input should be a valid number",
		"bool_parsing":   "Input should be a valid boolean, unable to interpret input",
		"bool_type":      "Input should be a valid boolean",
		"string_type":    "Input should be a valid string",
		"list_type":      "Input should be a valid array",
		"dict_type":      "Input should be an object",
	}
	for kind, want := range cases {
		if got := TypeMessage(kind); got != want {
			t.Errorf("TypeMessage(%q) = %q, want %q", kind, got, want)
		}
	}
	if strings.Contains(marshalRendered(t, TypeMismatch("string_type", []string{"body", "q"}, 5)), "url") {
		t.Fatal("a wrong-typed item must not carry a documentation link")
	}
}

// marshalRendered renders a whole failure so a test can inspect the bytes the
// client receives.
func marshalRendered(t *testing.T, item ValidationError) string {
	t.Helper()
	raw, err := json.Marshal(&ValidationFailure{Errors: []ValidationError{item}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
