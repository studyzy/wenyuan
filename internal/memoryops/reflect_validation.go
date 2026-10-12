package memoryops

import (
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// ReflectSchemaValidator validates a caller-supplied structured-output schema.
// It returns nil when the schema is acceptable. The composition root may supply
// one; when nil every non-null schema is accepted.
type ReflectSchemaValidator func(schema map[string]any) error

// ValidateReflect checks the reflection request fields: the required question,
// the fact-type selection, the budget level, the tag-scoping form, the
// tag-matching mode and the optional structured-output schema. Every failure
// here is an unprocessable-entity response raised before any model work.
func ValidateReflect(req ReflectRequest, schemaValidator ReflectSchemaValidator) *httpx.ValidationFailure {
	var items []httpx.ValidationError
	if !hasWordCharacter(appendContext(req.Query, req.Context)) {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "query"},
			Msg:   "query must contain at least one word character",
			Type:  "value_error.missing",
			Input: req.Query,
		})
	}
	if req.Budget != "" && !storage.IsReflectBudgetLevel(req.Budget) {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "budget"},
			Msg:   "budget must be one of low, mid, high",
			Type:  "value_error.enum",
			Input: req.Budget,
		})
	}
	if req.TagsMatch != "" && !containsToken(storageTagMatchModes(), req.TagsMatch) {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "tags_match"},
			Msg:   "tags_match must be one of any, all, any_strict, all_strict, exact",
			Type:  "value_error.enum",
			Input: req.TagsMatch,
		})
	}
	if req.FactTypes != nil && len(req.FactTypes) == 0 {
		items = append(items, httpx.ValidationError{
			Loc:  []string{"body", "fact_types"},
			Msg:  "fact_types must not be empty; omit it to select every fact type",
			Type: "value_error.missing",
		})
	}
	if unsupported := unsupportedTypes(req.FactTypes); len(unsupported) > 0 {
		items = append(items, httpx.ValidationError{
			Loc:   []string{"body", "fact_types"},
			Msg:   "unsupported fact type; accepted types are " + joinTokens(acceptedFactTypes),
			Type:  "value_error.enum",
			Input: unsupported,
		})
	}
	if len(req.Tags) > 0 && len(req.TagGroups.Groups) > 0 {
		items = append(items, httpx.ValidationError{
			Loc:  []string{"body", "tag_groups"},
			Msg:  "tags and tag_groups are mutually exclusive",
			Type: "value_error.mode",
		})
	}
	if req.ResponseSchema != nil && schemaValidator != nil {
		if err := schemaValidator(req.ResponseSchema); err != nil {
			items = append(items, httpx.ValidationError{
				Loc:  []string{"body", "response_schema"},
				Msg:  "response_schema is not a valid structured-output schema: " + err.Error(),
				Type: "value_error.schema",
			})
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}
