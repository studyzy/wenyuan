package entitylabels

import "testing"

// The reference accepts two config shapes for the same vocabulary: a bare list
// of groups, or an object carrying that list under "attributes". Both must
// produce the same parsed vocabulary.
func TestParseAcceptsListAndAttributesForm(t *testing.T) {
	group := map[string]any{
		"key":  "pedagogy",
		"type": "value",
		"values": []any{
			map[string]any{"value": "scaffolding"},
			map[string]any{"value": "direct", "description": "explicit instruction"},
		},
	}
	list, err := Parse([]any{group})
	if err != nil {
		t.Fatalf("list form: %v", err)
	}
	wrapped, err := Parse(map[string]any{"attributes": []any{group}})
	if err != nil {
		t.Fatalf("attributes form: %v", err)
	}
	if list.Empty() || wrapped.Empty() {
		t.Fatalf("both forms must parse to a non-empty vocabulary")
	}
	if len(list.Attributes) != 1 || len(wrapped.Attributes) != 1 {
		t.Fatalf("attributes = %d and %d, want one each", len(list.Attributes), len(wrapped.Attributes))
	}
	got := list.Attributes[0]
	want := wrapped.Attributes[0]
	if got.Key != want.Key || got.Type != want.Type || len(got.Values) != len(want.Values) {
		t.Fatalf("forms disagree: %+v vs %+v", got, want)
	}
	if got.Values[1].Value != "direct" || got.Values[1].Description != "explicit instruction" {
		t.Fatalf("value description lost: %+v", got.Values[1])
	}
}

// The legacy migration: free_values ⇒ text, multi_value ⇒ multi-values, and
// neither ⇒ value. The legacy keys are dropped so they cannot leak.
func TestParseMigratesLegacyKeys(t *testing.T) {
	vocab, err := Parse([]any{
		map[string]any{"key": "notes", "free_values": true},
		map[string]any{"key": "topics", "multi_value": true},
		map[string]any{"key": "level"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{TypeText, TypeMultiValues, TypeValue}
	for index, group := range vocab.Attributes {
		if group.Type != want[index] {
			t.Fatalf("group %q type = %q, want %q", group.Key, group.Type, want[index])
		}
	}
	// An explicit type wins over a legacy key.
	explicit, err := Parse([]any{
		map[string]any{"key": "level", "type": "value", "free_values": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Attributes[0].Type != TypeValue {
		t.Fatalf("explicit type = %q, want value", explicit.Attributes[0].Type)
	}
}

// A definition the schema could not honour is an error the caller can surface
// as a 422, not a silent drop.
func TestParseRejectsInvalidDefinitions(t *testing.T) {
	cases := map[string]any{
		"unsupported group type": []any{
			map[string]any{"key": "k", "type": "checkbox"},
		},
		"values not a list": []any{
			map[string]any{"key": "k", "type": "value", "values": "nope"},
		},
		"value entry missing value": []any{
			map[string]any{"key": "k", "type": "value", "values": []any{map[string]any{"description": "x"}}},
		},
		"value entry not an object": []any{
			map[string]any{"key": "k", "type": "value", "values": []any{"scaffolding"}},
		},
		"group not an object": []any{"scaffolding"},
		"top level scalar":    "scaffolding",
		"missing key": []any{
			map[string]any{"type": "value"},
		},
		"map field unsupported type": []any{
			map[string]any{"key": "person", "type": "map", "fields": map[string]any{
				"age": map[string]any{"type": "number"},
			}},
		},
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse(raw); err == nil {
				t.Fatalf("Parse(%v) = nil error, want an error", raw)
			}
		})
	}
}

// map nests recursively; a field's type defaults to text like the reference's
// MapField.
func TestParseMapNestsRecursively(t *testing.T) {
	vocab, err := Parse([]any{
		map[string]any{
			"key":  "person",
			"type": "map",
			"fields": map[string]any{
				"name": map[string]any{},
				"address": map[string]any{
					"type": "map",
					"fields": map[string]any{
						"city": map[string]any{"type": "text"},
					},
				},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	group := vocab.Attributes[0]
	if group.Fields["name"].Type != TypeText {
		t.Fatalf("field default type = %q, want text", group.Fields["name"].Type)
	}
	nested := group.Fields["address"]
	if nested.Type != TypeMap || nested.Fields["city"].Type != TypeText {
		t.Fatalf("nested map not parsed: %+v", nested)
	}
}

// Empty covers both the nil vocabulary and a parsed-but-empty one.
func TestEmpty(t *testing.T) {
	var nilVocab *Vocabulary
	if !nilVocab.Empty() {
		t.Fatalf("nil vocabulary must be empty")
	}
	empty, err := Parse([]any{})
	if err != nil {
		t.Fatal(err)
	}
	if !empty.Empty() {
		t.Fatalf("empty list must parse to an empty vocabulary")
	}
	none, err := Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	if !none.Empty() {
		t.Fatalf("nil config must parse to an empty vocabulary")
	}
}

// The canonical rendering of a selection is "key:value".
func TestCanonicalLabel(t *testing.T) {
	if got := CanonicalLabel("pedagogy", "scaffolding"); got != "pedagogy:scaffolding" {
		t.Fatalf("CanonicalLabel = %q", got)
	}
	group := Group{Key: "interest"}
	if got := group.Label("active"); got != "interest:active" {
		t.Fatalf("group.Label = %q", got)
	}
}

// A tag group's selections are the subset of labels projected into the unit's
// tags.
func TestTagLabelsSelectsTagGroups(t *testing.T) {
	vocab, err := Parse([]any{
		map[string]any{"key": "topic", "type": "text", "tag": true},
		map[string]any{"key": "matter", "type": "text"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := vocab.TagLabels([]string{"topic:algorithms", "matter:day", "topic:algorithms"})
	if len(got) != 1 || got[0] != "topic:algorithms" {
		t.Fatalf("TagLabels = %v, want just the tag group's label once", got)
	}
	if vocab.TagKeys()["topic"] != true || vocab.TagKeys()["matter"] {
		t.Fatalf("TagKeys = %v", vocab.TagKeys())
	}
}
