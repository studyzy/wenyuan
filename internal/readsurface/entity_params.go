package readsurface

import (
	"context"
	"errors"
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The graph and entity routes share one parameter pipeline, and its order is
// the reference's, not a matter of taste:
//
//  1. every `Query(...)` parameter is bound by the framework, so a bad one is a
//     422 listing all failures, in declaration order;
//  2. `tag_groups` is parsed by a helper the handler calls before it does
//     anything else, so a malformed tree (or `tags` sent with it) is a 400;
//  3. only then is the bank looked up, so an unknown bank is the 404.
//
// Validating any of these after the bank lookup would answer the wrong status
// for a request that is simply malformed — the defect this file exists to keep
// fixed.

// entityParams is the resolved query state of a graph or entity route.
type entityParams struct {
	Limit     int
	Offset    int
	MinCount  int
	Tags      []string
	TagsMatch string
	TagGroups []storage.TagGroup
}

// entityParamSpec describes what one route validates.
type entityParamSpec struct {
	// Page asks for limit/offset bounded below by zero.
	Page bool
	// Offset asks for the `offset` parameter as well. It is separate from Page
	// because neither graph route declares one: the reference's api_graph and
	// api_entity_graph take `limit` alone (api/http.py:5783-5794, 6805-6819), and
	// reading an undeclared parameter would answer 422 to a request the framework
	// serves — the same rule NoTagGroups exists for.
	Offset bool
	// LimitDefault replaces defaultPageLimit for the limit parameter.
	LimitDefault int
	// MinCount asks for the unbounded `min_count` filter.
	MinCount bool
	// TagsMatch validates `tags_match` against the reference's literal set.
	TagsMatch bool
	// TagsMatchDefault is the mode an absent `tags_match` takes.
	TagsMatchDefault string
	// NoTagGroups skips the `tag_groups` parameter entirely, for a route whose
	// reference signature does not declare it. The framework ignores a query
	// parameter no route declares, so parsing one here would answer 400 to a
	// request the reference serves.
	NoTagGroups bool
}

// readEntityParams validates a graph or entity route's parameters in the
// reference's order. When it reports false the response is already written.
func (h *Handlers) readEntityParams(w http.ResponseWriter, r *http.Request, spec entityParamSpec) (entityParams, bool) {
	var (
		params entityParams
		query  queryErrors
	)
	// Declaration order, as the reference's signatures spell it: limit,
	// offset, tags, tags_match, tag_groups — and for the entity graph
	// min_count, which sits between limit and the tag filters.
	if spec.Page {
		def := spec.LimitDefault
		if def == 0 {
			def = defaultPageLimit
		}
		params.Limit = query.int(r, "limit", def, 0, -1)
		if spec.Offset {
			params.Offset = query.int(r, "offset", 0, 0, -1)
		}
	}
	if spec.MinCount {
		params.MinCount = query.unboundedInt(r, "min_count", 1)
	}
	params.TagsMatch = spec.TagsMatchDefault
	if spec.TagsMatch {
		params.TagsMatch = query.literal(r, "tags_match", storage.RecallTagMatchModes(), spec.TagsMatchDefault)
	}
	if !query.empty() {
		query.flush(w)
		return entityParams{}, false
	}
	if spec.NoTagGroups {
		return params, true
	}
	// The tag filters come next: `tag_groups` is parsed before the bank is
	// known, so its own 400 outranks the bank's 404.
	params.Tags = queryValues(r, "tags")
	tagGroups, ok := tagGroupsQuery(w, r, params.Tags)
	if !ok {
		return entityParams{}, false
	}
	params.TagGroups = tagGroups
	return params, true
}

// validateEntityQuery is readEntityParams followed by the bank lookup, for the
// routes that need the bank before they can serve anything.
func (h *Handlers) validateEntityQuery(w http.ResponseWriter, r *http.Request, spec entityParamSpec) (entityParams, string, bool) {
	params, ok := h.readEntityParams(w, r, spec)
	if !ok {
		return entityParams{}, "", false
	}
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return entityParams{}, "", false
	}
	return params, bankID, true
}

// resolveFuzzyTagGroups rewrites a filter's `resolve: "fuzzy"` leaves into exact
// tags, the way the reference resolves them above every SQL builder
// (engine/memory_engine.py:15935-15972) before an entity read runs.
//
// It is the same resolution recall performs, against the bank's own tag
// vocabulary, so all three of the reference's entity doors
// (`list_entities`, `get_entity_graph`, `get_entity`) resolve their filter
// instead of comparing a fuzzy leaf literally — which would match nothing.
// The caps are the resolver's own: a vocabulary above storage.MaxTagVocabulary,
// or an `exact` leaf whose combinations pass the branch ceiling, is the
// reference's 422 rather than a silently truncated match set.
func (h *Handlers) resolveFuzzyTagGroups(ctx context.Context, bankID string, groups []storage.TagGroup) ([]storage.TagGroup, error) {
	if !storage.NeedsTagResolution(groups) {
		return groups, nil
	}
	tags, err := h.Store.ListTags(ctx, bankID)
	if err != nil {
		return nil, err
	}
	vocabulary := make([]string, 0, len(tags))
	for _, entry := range tags {
		vocabulary = append(vocabulary, entry.Tag)
	}
	resolved, err := storage.ResolveTagGroups(groups, vocabulary)
	if err != nil {
		return nil, &tagScopeResolutionError{message: err.Error()}
	}
	return resolved, nil
}

// tagScopeResolutionError carries a resolution refusal the caller maps to the
// reference's 422: the vocabulary cap and the branch ceiling are request
// problems, not internal faults.
type tagScopeResolutionError struct{ message string }

func (e *tagScopeResolutionError) Error() string { return e.message }

// resolveFuzzyTagGroupsFor is resolveFuzzyTagGroups with the reference's error
// mapping: a refusal is the 422 `OperationValidationError` its handler turns
// into, and anything else is the ordinary internal-error envelope. It reports
// false when a response was already written.
func (h *Handlers) resolveFuzzyTagGroupsFor(
	w http.ResponseWriter, r *http.Request, bankID string, groups []storage.TagGroup,
) ([]storage.TagGroup, bool) {
	resolved, err := h.resolveFuzzyTagGroups(r.Context(), bankID, groups)
	if err == nil {
		return resolved, true
	}
	var refusal *tagScopeResolutionError
	if errors.As(err, &refusal) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindUnprocessable, refusal.Error()))
		return nil, false
	}
	httpx.WriteErrorFor(w, r, asHTTPError(err))
	return nil, false
}
