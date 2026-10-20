package memoryops

import (
	"errors"
	"fmt"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ReflectSchemaValidator is an optional second validator for a caller-supplied
// structured-output schema, run only after the reference's own rules pass. It
// returns nil when the schema is acceptable. The built-in rules are always
// enforced, so a nil hook accepts exactly the schemas the reference accepts.
type ReflectSchemaValidator func(schema map[string]any) error

// reflectSchemaPropertyTypes is the set of property types the structured-output
// builder can turn into a field; any other declared type is rejected at the
// boundary rather than failing later inside the extraction call.
var reflectSchemaPropertyTypes = []string{"array", "boolean", "integer", "number", "object", "string"}

// reflectSchemaPropertyTypesRendered is the reference's rendering of the
// accepted-type set (Python's sorted() over the set), which is part of the
// error text.
const reflectSchemaPropertyTypesRendered = "['array', 'boolean', 'integer', 'number', 'object', 'string']"

// validateReflectResponseSchema reproduces the reference's response-schema
// check: a response_schema is usable only when it is an object schema whose
// non-empty properties map holds well-formed property shapes and whose optional
// required list names known properties. The first violation is reported, the
// way the reference's validator raises on its first failure, with its exact
// wording.
func validateReflectResponseSchema(schema map[string]any) error {
	if schemaType, ok := schema["type"]; ok && schemaType != nil {
		text, isText := schemaType.(string)
		if !isText || text != "object" {
			return errors.New(`response_schema must be an object schema (its "type" must be "object")`)
		}
	}
	properties, isMap := schema["properties"].(map[string]any)
	if !isMap || len(properties) == 0 {
		return errors.New("response_schema must define a non-empty 'properties' object")
	}
	for name, raw := range properties {
		property, isProperty := raw.(map[string]any)
		if !isProperty {
			return fmt.Errorf("response_schema property '%s' must be an object", name)
		}
		if propertyType, ok := property["type"]; ok && propertyType != nil {
			text, isText := propertyType.(string)
			if !isText || !containsToken(reflectSchemaPropertyTypes, text) {
				return fmt.Errorf("response_schema property '%s' has unsupported type '%v'; expected one of %s",
					name, propertyType, reflectSchemaPropertyTypesRendered)
			}
		}
	}
	if required, ok := schema["required"]; ok && required != nil {
		list, isList := required.([]any)
		if !isList {
			return errors.New("response_schema 'required' must be a list of property names")
		}
		names := make([]string, 0, len(list))
		for _, item := range list {
			text, isText := item.(string)
			if !isText {
				return errors.New("response_schema 'required' must be a list of property names")
			}
			names = append(names, text)
		}
		var unknown []string
		for _, name := range names {
			if _, known := properties[name]; !known {
				unknown = append(unknown, name)
			}
		}
		if len(unknown) > 0 {
			return fmt.Errorf("response_schema 'required' references unknown properties: %s", renderPythonStringList(unknown))
		}
	}
	return nil
}

// ValidateResponseSchema applies the reference's response-schema check to a
// schema a non-reflect surface accepts (a bank's knowledge_page_default_trigger
// carries one), so both doors give the same answer with the same wording.
func ValidateResponseSchema(schema map[string]any) error {
	return validateReflectResponseSchema(schema)
}

// renderPythonStringList renders a list of names the way the reference's
// f-string over a Python list does, so the error text matches byte-for-byte.
func renderPythonStringList(names []string) string {
	rendered := "["
	for i, name := range names {
		if i > 0 {
			rendered += ", "
		}
		rendered += "'" + name + "'"
	}
	return rendered + "]"
}

// reflectIncludeMembers names the reflect include sub-object's model members,
// in the reference's field order.
var reflectIncludeMembers = []string{"facts", "tool_calls"}

// reflectQueryErrors reports the reference's model-level question check: the
// field is required and "null" is not a string, and a supplied string whose
// query-and-context text carries no word character is rejected at the model
// itself, with the body object as its input. The check is skipped when the
// field is absent, so a missing field reports only that one item.
func reflectQueryErrors(body map[string]any, req ReflectRequest) []httpx.ValidationError {
	raw, present := body["query"]
	switch {
	case !present:
		return []httpx.ValidationError{httpx.Missing([]string{"body", "query"}, body)}
	case raw == nil:
		return []httpx.ValidationError{httpx.TypeMismatch("string_type", []string{"body", "query"}, nil)}
	case !hasWordCharacter(appendContext(req.Query, req.Context)):
		return []httpx.ValidationError{httpx.ValueError([]string{"body"}, body,
			"'query' must not be empty or whitespace-only.")}
	}
	return nil
}

// ValidateReflect checks the reflection request fields: the required question,
// the include sub-object's shape, the fact-type selection, the budget level, the
// tag-scoping form, the tag-matching mode and the optional structured-output
// schema. Every failure here is an unprocessable-entity response raised before
// any model work.
func ValidateReflect(req ReflectRequest, body map[string]any, schemaValidator ReflectSchemaValidator) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	items = append(items, reflectQueryErrors(body, req)...)
	if req.Budget != "" && !storage.IsReflectBudgetLevel(req.Budget) {
		items = append(items, httpx.Enum([]string{"body", "budget"}, req.Budget, storageBudgetLevels()))
	}
	items = append(items, includeMemberErrors(body, req.Include, reflectIncludeMembers)...)
	if req.TagsMatch != "" && !containsToken(storageTagMatchModes(), req.TagsMatch) {
		items = append(items, httpx.Literal([]string{"body", "tags_match"}, req.TagsMatch, storageTagMatchModes()))
	}
	if req.FactTypes != nil && len(req.FactTypes) == 0 {
		items = append(items, httpx.ValueError([]string{"body", "fact_types"}, []any{},
			"fact_types must not be empty. Use null to include all fact types."))
	}
	// Every element is a literal, so a value outside the accepted set is a
	// per-element literal failure at that element's own index (the value it
	// carries included), from wherever the element starts.
	for _, item := range reflectFactTypeItems(body) {
		if !locationCovered(item.Loc, items) {
			items = append(items, item)
		}
	}
	// The field is `list[TagGroup]`, so a value that is not a list is the
	// reference's list-type failure; otherwise every branch item the left-to-
	// right union raised is reported. Either way the items are field-level, so
	// they appear beside (not instead of) the body-level rules.
	items = append(items, req.TagGroups.Items()...)
	if len(req.TagGroups.Items()) == 0 && tagsSupplied(body) && req.TagGroups.Present {
		items = append(items, httpx.ValueError([]string{"body"}, body,
			"'tags' and 'tag_groups' are mutually exclusive. Use 'tag_groups' for compound filtering."))
	}
	// The reference declares the per-observation search budget with `ge=1`, so a
	// zero or negative override is refused before any tool runs.
	if req.ReflectSearchObservationsMaxTokens != nil && *req.ReflectSearchObservationsMaxTokens < 1 {
		items = append(items, httpx.GreaterThanEqual([]string{"body", "reflect_search_observations_max_tokens"},
			*req.ReflectSearchObservationsMaxTokens, 1))
	}
	if req.ResponseSchema != nil {
		if err := validateReflectResponseSchema(req.ResponseSchema); err != nil {
			items = append(items, httpx.ValueError([]string{"body", "response_schema"}, req.ResponseSchema, err.Error()))
		} else if schemaValidator != nil {
			if err := schemaValidator(req.ResponseSchema); err != nil {
				items = append(items, httpx.ValueError([]string{"body", "response_schema"}, req.ResponseSchema, err.Error()))
			}
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}
