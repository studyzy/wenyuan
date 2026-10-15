package extraction

import (
	"reflect"
	"strings"
	"testing"

	"github.com/studyzy/wenyuan/internal/entitylabels"
)

func testVocabulary(t *testing.T) *entitylabels.Vocabulary {
	t.Helper()
	vocab, err := entitylabels.Parse([]any{
		map[string]any{
			"key":  "pedagogy",
			"type": "value",
			"values": []any{
				map[string]any{"value": "scaffolding"},
				map[string]any{"value": "direct"},
			},
		},
		map[string]any{
			"key":  "interest",
			"type": "multi-values",
			"values": []any{
				map[string]any{"value": "active"},
				map[string]any{"value": "dormant"},
			},
		},
		map[string]any{"key": "note", "type": "text"},
		map[string]any{"key": "topic", "type": "multi-text"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return vocab
}

// With a vocabulary the schema declares a 'labels' object whose value group is
// enum-constrained to its allowed values.
func TestFactSchemaConstrainsValueGroup(t *testing.T) {
	schema := schemaFor(Options{Vocabulary: testVocabulary(t)})
	labels := schema.Properties["facts"].Items.Properties["labels"]
	if labels == nil || labels.Type != "object" {
		t.Fatalf("labels schema missing: %+v", schema.Properties["facts"].Items.Properties)
	}
	pedagogy := labels.Properties["pedagogy"]
	if pedagogy == nil || pedagogy.Type != "string" {
		t.Fatalf("pedagogy schema = %+v", pedagogy)
	}
	if !reflect.DeepEqual(pedagogy.Enum, []string{"scaffolding", "direct"}) {
		t.Fatalf("pedagogy enum = %v, want its allowed values", pedagogy.Enum)
	}
	interest := labels.Properties["interest"]
	if interest == nil || interest.Type != "array" || interest.Items == nil {
		t.Fatalf("multi-values schema = %+v", interest)
	}
	if !reflect.DeepEqual(interest.Items.Enum, []string{"active", "dormant"}) {
		t.Fatalf("interest item enum = %v", interest.Items.Enum)
	}
	if note := labels.Properties["note"]; note == nil || note.Type != "string" || len(note.Enum) != 0 {
		t.Fatalf("text schema = %+v", note)
	}
	if topic := labels.Properties["topic"]; topic == nil || topic.Type != "array" || topic.Items.Enum != nil {
		t.Fatalf("multi-text schema = %+v", topic)
	}
	if !containsString(schema.Properties["facts"].Items.Required, "labels") {
		t.Fatalf("labels must be required: %v", schema.Properties["facts"].Items.Required)
	}
}

// Without a vocabulary the schema and prompt are exactly what this package has
// always produced — the compatibility contract.
func TestDefaultSchemaAndPromptUnchanged(t *testing.T) {
	schema := schemaFor(Options{})
	if _, present := schema.Properties["facts"].Items.Properties["labels"]; present {
		t.Fatalf("no vocabulary must not add a labels property")
	}
	wantRequired := []string{"what", "when", "where", "who", "why", "fact_type"}
	if !reflect.DeepEqual(schema.Properties["facts"].Items.Required, wantRequired) {
		t.Fatalf("required = %v, want the five dimensions plus fact_type", schema.Properties["facts"].Items.Required)
	}
	prompt := composeExtractionPrompt(Options{})
	if prompt != factExtractionPrompt {
		t.Fatalf("default prompt changed:\n%q", prompt)
	}
	if strings.Contains(prompt, "ENTITY LABELS") {
		t.Fatalf("default prompt must not mention labels")
	}
}

// A vocabulary renders a clearly-labelled block naming the groups and their
// allowed values.
func TestPromptRendersVocabulary(t *testing.T) {
	prompt := composeExtractionPrompt(Options{Vocabulary: testVocabulary(t)})
	for _, want := range []string{"ENTITY LABELS", "- pedagogy", "scaffolding", "- interest", "active"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
	structured := composeExtractionPrompt(Options{Vocabulary: mustParse(t, []any{
		map[string]any{"key": "person", "type": "map", "fields": map[string]any{
			"name": map[string]any{"type": "text"},
		}},
	})})
	if !strings.Contains(structured, "STRUCTURED ENTITY TYPES") || !strings.Contains(structured, "name (text)") {
		t.Fatalf("map group not rendered:\n%s", structured)
	}
}

// A selection outside an allowed set is dropped rather than stored as an
// invention; a valid one becomes its canonical key:value label.
func TestExtractLabelsDropsOutOfVocabularyValues(t *testing.T) {
	vocab := testVocabulary(t)
	structured := map[string]any{"facts": []any{
		map[string]any{
			"text": "A fact.",
			"labels": map[string]any{
				"pedagogy": "invented",
				"interest": []any{"active", "bogus"},
				"note":     "free form",
				"topic":    []any{"memory", "systems"},
			},
		},
		map[string]any{
			"text": "Another fact.",
			"labels": map[string]any{
				// A different casing of an allowed value is still allowed and
				// becomes the declared spelling.
				"pedagogy": "Scaffolding",
				"note":     "none",
			},
		},
	}}
	facts, err := parseFacts(structured, vocab)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 2 {
		t.Fatalf("facts = %d, want two", len(facts))
	}
	want := []string{"interest:active", "note:free form", "topic:memory", "topic:systems"}
	if !reflect.DeepEqual(facts[0].Labels, want) {
		t.Fatalf("labels = %v, want %v", facts[0].Labels, want)
	}
	if !reflect.DeepEqual(facts[1].Labels, []string{"pedagogy:scaffolding"}) {
		t.Fatalf("second fact labels = %v", facts[1].Labels)
	}
}

// With no vocabulary the answer's labels are ignored, so a caller that
// configures none stores exactly what it did before.
func TestExtractLabelsIgnoredWithoutVocabulary(t *testing.T) {
	structured := map[string]any{"facts": []any{
		map[string]any{"text": "A fact.", "labels": map[string]any{"pedagogy": "scaffolding"}},
	}}
	facts, err := parseFacts(structured, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || len(facts[0].Labels) != 0 {
		t.Fatalf("labels without a vocabulary = %v, want none", facts[0].Labels)
	}
}

// A map group is an array of nested objects in the schema, and its answer
// flattens to key:field:...:value labels.
func TestMapGroupSchemaAndLabels(t *testing.T) {
	vocab := mustParse(t, []any{
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
	person := schemaFor(Options{Vocabulary: vocab}).Properties["facts"].Items.Properties["labels"].Properties["person"]
	if person == nil || person.Type != "array" || person.Items == nil || person.Items.Type != "object" {
		t.Fatalf("map schema = %+v", person)
	}
	address := person.Items.Properties["address"]
	if address == nil || address.Type != "array" || address.Items.Properties["city"] == nil {
		t.Fatalf("nested map schema = %+v", address)
	}
	structured := map[string]any{"facts": []any{
		map[string]any{
			"text": "A person.",
			"labels": map[string]any{
				"person": []any{map[string]any{
					"name":    "Alice",
					"address": []any{map[string]any{"city": "Berlin"}},
				}},
			},
		},
	}}
	facts, err := parseFacts(structured, vocab)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"person:address:city:Berlin", "person:name:Alice"}
	if !reflect.DeepEqual(facts[0].Labels, want) {
		t.Fatalf("map labels = %v, want %v", facts[0].Labels, want)
	}
}

// mustParse is a test helper that fails on an invalid vocabulary.
func mustParse(t *testing.T, raw any) *entitylabels.Vocabulary {
	t.Helper()
	vocab, err := entitylabels.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return vocab
}

// containsString reports whether the list holds want.
func containsString(list []string, want string) bool {
	for _, entry := range list {
		if entry == want {
			return true
		}
	}
	return false
}
