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
		items = append(items, httpx.ValueError([]string{"body", "query"}, req.Query,
			"query must contain at least one word character after normalization"))
	}
	if req.Budget != "" && !storage.IsReflectBudgetLevel(req.Budget) {
		items = append(items, httpx.Enum([]string{"body", "budget"}, req.Budget, storageBudgetLevels()))
	}
	if req.TagsMatch != "" && !containsToken(storageTagMatchModes(), req.TagsMatch) {
		items = append(items, httpx.Literal([]string{"body", "tags_match"}, req.TagsMatch, storageTagMatchModes()))
	}
	if req.FactTypes != nil && len(req.FactTypes) == 0 {
		items = append(items, httpx.ValueError([]string{"body", "fact_types"}, []any{},
			"fact_types must not be empty. Use null to include all fact types."))
	}
	if unsupported := unsupportedTypes(req.FactTypes); len(unsupported) > 0 {
		items = append(items, httpx.ValueError([]string{"body", "fact_types"}, unsupported,
			"unsupported fact type; accepted types are "+joinTokens(acceptedFactTypes)))
	}
	if len(req.Tags) > 0 && len(req.TagGroups.Groups) > 0 {
		items = append(items, httpx.ValueError([]string{"body"}, nil,
			"'tags' and 'tag_groups' are mutually exclusive. Use 'tag_groups' for compound filtering."))
	}
	if req.ResponseSchema != nil && schemaValidator != nil {
		if err := schemaValidator(req.ResponseSchema); err != nil {
			items = append(items, httpx.ValueError([]string{"body", "response_schema"}, req.ResponseSchema,
				"response_schema is not a valid structured-output schema: "+err.Error()))
		}
	}
	if len(items) == 0 {
		return nil
	}
	return &httpx.ValidationFailure{Errors: items}
}
