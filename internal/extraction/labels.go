package extraction

import (
	"fmt"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/provider"
)

// This file renders a bank's controlled label vocabulary into the extraction
// contract: a clearly-labelled prompt block that tells the model what labels
// exist, a structured 'labels' object in the schema that constrains a value
// group to its allowed values, and the reader that turns the model's answer
// into canonical key:value selections.
//
// With no vocabulary configured the prompt and schema are exactly what they
// were before this file existed: every function here is reached only when the
// vocabulary is non-empty.

// labelSentinel reports whether a value is a placeholder the model uses for
// "nothing" rather than a real selection.
func labelSentinel(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "none", "null", "n/a":
		return true
	default:
		return false
	}
}

// labelsSchema builds the 'labels' object the fact schema declares for a
// vocabulary. A value group is enum-constrained to its allowed values, a
// multi-values group to an array of them, text groups take a free string,
// multi-text groups an array of them, and a map group an array of nested
// objects. A group the schema cannot express (an enum group with no values) is
// omitted rather than declared unconstrained.
func labelsSchema(vocab *entitylabels.Vocabulary) *provider.Schema {
	properties := make(map[string]*provider.Schema, len(vocab.Attributes))
	required := make([]string, 0, len(vocab.Attributes))
	for _, group := range vocab.Attributes {
		if group.Key == "" {
			continue
		}
		node := typeSchema(group.Type, group.Values, group.Fields)
		if node == nil {
			continue
		}
		node.Description = group.Description
		properties[group.Key] = node
		if group.Type == entitylabels.TypeValue && !group.Optional {
			required = append(required, group.Key)
		}
	}
	schema := &provider.Schema{Type: "object", Properties: properties}
	if len(required) > 0 {
		schema.Required = required
	}
	return schema
}

// typeSchema maps one label type onto its JSON-schema node. It returns nil for
// a shape it cannot express, so the caller can drop it.
func typeSchema(labelType string, values []entitylabels.Value, fields map[string]entitylabels.Field) *provider.Schema {
	switch labelType {
	case entitylabels.TypeValue:
		enum := declaredValues(values)
		if len(enum) == 0 {
			return nil
		}
		return &provider.Schema{Type: "string", Enum: enum}
	case entitylabels.TypeMultiValues:
		enum := declaredValues(values)
		if len(enum) == 0 {
			return nil
		}
		return &provider.Schema{Type: "array", Items: &provider.Schema{Type: "string", Enum: enum}}
	case entitylabels.TypeText:
		return &provider.Schema{Type: "string"}
	case entitylabels.TypeMultiText:
		return &provider.Schema{Type: "array", Items: &provider.Schema{Type: "string"}}
	case entitylabels.TypeMap:
		items := mapSchema(fields)
		if items == nil {
			return nil
		}
		return &provider.Schema{Type: "array", Items: items}
	default:
		return nil
	}
}

// mapSchema builds the object schema for a map group's or nested map field's
// fields, recursing through nested maps.
func mapSchema(fields map[string]entitylabels.Field) *provider.Schema {
	if len(fields) == 0 {
		return nil
	}
	properties := make(map[string]*provider.Schema, len(fields))
	for name, field := range fields {
		node := typeSchema(field.Type, field.Values, field.Fields)
		if node == nil {
			continue
		}
		node.Description = field.Description
		properties[name] = node
	}
	if len(properties) == 0 {
		return nil
	}
	return &provider.Schema{Type: "object", Properties: properties}
}

// declaredValues returns a group's or field's non-empty allowed values.
func declaredValues(values []entitylabels.Value) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value.Value != "" {
			out = append(out, value.Value)
		}
	}
	return out
}

// BuildFreeFormEntitiesInstruction renders the one line entities_allow_free_form
// decides, inside the entity-labels section. It is shared with the prompt
// preview, which reports this line as its own block so the flag is visible as
// something shaping the prompt rather than hiding inside the labels text.
func BuildFreeFormEntitiesInstruction(freeForm bool) string {
	if freeForm {
		return "Classify each fact using the structured 'labels' field below. " +
			"Continue extracting regular named entities in the 'entities' field."
	}
	return "Classify each fact using the structured 'labels' field below. " +
		"Do NOT add regular named entities — labels-only mode."
}

// labelsPromptSection renders the vocabulary as a clearly-labelled block
// appended to the fact-extraction prompt. It mirrors the reference's two-part
// layout: classification attributes first, then the structured (map) entity
// types. The opening sentence is the one line entities_allow_free_form decides:
// whether regular named entities keep being extracted alongside the labels.
func labelsPromptSection(vocab *entitylabels.Vocabulary, freeForm bool) string {
	var builder strings.Builder
	builder.WriteString("\n\n")
	builder.WriteString("══════════════════════════════════════════════════════════════════════════\n")
	builder.WriteString("ENTITY LABELS - CLASSIFICATION ATTRIBUTES\n")
	builder.WriteString("══════════════════════════════════════════════════════════════════════════\n\n")
	builder.WriteString(BuildFreeFormEntitiesInstruction(freeForm))
	builder.WriteString("\n\n")
	builder.WriteString("For each fact, fill the 'labels' object. Each field is a label group:\n\n")

	hasClassification := false
	hasMap := false
	for _, group := range vocab.Attributes {
		if group.Key == "" {
			continue
		}
		switch group.Type {
		case entitylabels.TypeMap:
			hasMap = true
		case entitylabels.TypeText:
			hasClassification = true
			fmt.Fprintf(&builder, "- %s (free text or null): %s\n\n", group.Key, group.Description)
		case entitylabels.TypeMultiText:
			hasClassification = true
			fmt.Fprintf(&builder, "- %s (list of free text, empty list if none): %s\n\n", group.Key, group.Description)
		default:
			hasClassification = true
			mode := "single value or null"
			if group.Type == entitylabels.TypeMultiValues {
				mode = "multi-value (list)"
			}
			fmt.Fprintf(&builder, "- %s (%s): %s\n", group.Key, mode, group.Description)
			for _, value := range group.Values {
				if value.Value == "" {
					continue
				}
				detail := ""
				if value.Description != "" {
					detail = " — " + value.Description
				}
				fmt.Fprintf(&builder, "    • %q%s\n", value.Value, detail)
			}
			builder.WriteString("\n")
		}
	}
	if hasClassification {
		builder.WriteString("Only assign labels when clearly applicable. Leave null/empty if the fact does not match.\n")
	}
	if hasMap {
		builder.WriteString("\n")
		builder.WriteString("══════════════════════════════════════════════════════════════════════════\n")
		builder.WriteString("STRUCTURED ENTITY TYPES\n")
		builder.WriteString("══════════════════════════════════════════════════════════════════════════\n\n")
		builder.WriteString("For each fact, extract structured entities into the corresponding list field in 'labels'.\n")
		builder.WriteString("Each structured entity type has defined fields. Return a list of objects, one per entity found.\n\n")
		for _, group := range vocab.Attributes {
			if group.Type != entitylabels.TypeMap || len(group.Fields) == 0 || group.Key == "" {
				continue
			}
			if group.Description != "" {
				fmt.Fprintf(&builder, "- %s: %s\n", group.Key, group.Description)
			} else {
				fmt.Fprintf(&builder, "- %s\n", group.Key)
			}
			writeMapFieldsPrompt(&builder, group.Fields, 4)
			builder.WriteString("\n")
		}
		builder.WriteString("Only extract structured entities when clearly present in the text. Leave the list empty if none found.\n")
	}
	return builder.String()
}

// writeMapFieldsPrompt appends a map's field descriptions, recursing through
// nested maps. Field names are sorted so the rendered prompt is deterministic
// even though a Go map is not.
func writeMapFieldsPrompt(builder *strings.Builder, fields map[string]entitylabels.Field, indent int) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	pad := strings.Repeat(" ", indent)
	for _, name := range names {
		field := fields[name]
		detail := ""
		if field.Description != "" {
			detail = ": " + field.Description
		}
		switch {
		case field.Type == entitylabels.TypeMap && len(field.Fields) > 0:
			fmt.Fprintf(builder, "%s• %s (object)%s\n", pad, name, detail)
			writeMapFieldsPrompt(builder, field.Fields, indent+4)
		case field.Type == entitylabels.TypeMultiText:
			fmt.Fprintf(builder, "%s• %s (list of free text, [] if none)%s\n", pad, name, detail)
		case field.Type == entitylabels.TypeMultiValues:
			if values := joinValues(field.Values); values != "" {
				fmt.Fprintf(builder, "%s• %s (multi-values: %s)%s\n", pad, name, values, detail)
			} else {
				fmt.Fprintf(builder, "%s• %s (multi-values)%s\n", pad, name, detail)
			}
		case field.Type == entitylabels.TypeValue && len(field.Values) > 0:
			fmt.Fprintf(builder, "%s• %s (one of: %s)%s\n", pad, name, joinValues(field.Values), detail)
		default:
			fmt.Fprintf(builder, "%s• %s (text)%s\n", pad, name, detail)
		}
	}
}

// joinValues joins a value list's declared spellings for the prompt.
func joinValues(values []entitylabels.Value) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value.Value != "" {
			parts = append(parts, value.Value)
		}
	}
	return strings.Join(parts, ", ")
}

// extractLabels reads one fact entry's 'labels' object into canonical key:value
// selections. A value outside a group's allowed set is dropped rather than
// stored as an invention, and the group order of the vocabulary fixes the
// output order.
func extractLabels(entry map[string]any, vocab *entitylabels.Vocabulary) []string {
	if vocab.Empty() {
		return nil
	}
	raw, ok := entry["labels"].(map[string]any)
	if !ok || len(raw) == 0 {
		return nil
	}
	var out []string
	seen := map[string]bool{}
	add := func(label string) {
		if label == "" || seen[label] {
			return
		}
		seen[label] = true
		out = append(out, label)
	}
	for _, group := range vocab.Attributes {
		if group.Key == "" {
			continue
		}
		value, present := raw[group.Key]
		if !present {
			continue
		}
		switch group.Type {
		case entitylabels.TypeValue:
			text, ok := value.(string)
			if !ok || labelSentinel(text) {
				continue
			}
			if allowed, ok := group.AllowedValue(text); ok {
				add(group.Label(allowed))
			}
		case entitylabels.TypeMultiValues:
			for _, text := range stringValues(value) {
				if labelSentinel(text) {
					continue
				}
				if allowed, ok := group.AllowedValue(text); ok {
					add(group.Label(allowed))
				}
			}
		case entitylabels.TypeText:
			text, ok := value.(string)
			if !ok || labelSentinel(text) {
				continue
			}
			add(group.Label(strings.TrimSpace(text)))
		case entitylabels.TypeMultiText:
			for _, text := range stringValues(value) {
				if !labelSentinel(text) {
					add(group.Label(strings.TrimSpace(text)))
				}
			}
		case entitylabels.TypeMap:
			for _, object := range objectValues(value) {
				collectMapLabels(object, group.Fields, group.Key, add)
			}
		}
	}
	return out
}

// collectMapLabels flattens one map entity into key:field:...:value strings,
// recursing through nested maps.
func collectMapLabels(object map[string]any, fields map[string]entitylabels.Field, prefix string, add func(string)) {
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		field := fields[name]
		value, present := object[name]
		if !present {
			continue
		}
		path := prefix + ":" + name
		switch field.Type {
		case entitylabels.TypeMap:
			for _, nested := range objectValues(value) {
				collectMapLabels(nested, field.Fields, path, add)
			}
		case entitylabels.TypeMultiValues, entitylabels.TypeMultiText:
			for _, text := range stringValues(value) {
				if !labelSentinel(text) {
					add(path + ":" + strings.TrimSpace(text))
				}
			}
		default:
			text, ok := value.(string)
			if ok && !labelSentinel(text) {
				add(path + ":" + strings.TrimSpace(text))
			}
		}
	}
}

// stringList reads a value that may be one string or a list of strings.
func stringValues(value any) []string {
	switch typed := value.(type) {
	case string:
		return []string{typed}
	case []any:
		out := make([]string, 0, len(typed))
		for _, entry := range typed {
			if text, ok := entry.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

// objectList reads a value that may be one object or a list of objects.
func objectValues(value any) []map[string]any {
	switch typed := value.(type) {
	case map[string]any:
		return []map[string]any{typed}
	case []any:
		out := make([]map[string]any, 0, len(typed))
		for _, entry := range typed {
			if object, ok := entry.(map[string]any); ok {
				out = append(out, object)
			}
		}
		return out
	default:
		return nil
	}
}
