package memoryops

import (
	"net/http"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// applyRecallTagScope AND-s the caller's resolved tag scope into a recall
// query's own tag filter, so a scoped caller can narrow but never widen what
// recall sees. An empty scope leaves the query unchanged.
func applyRecallTagScope(r *http.Request, query storage.RecallQuery) storage.RecallQuery {
	scope := tagScopeFromRequest(r)
	if len(scope) == 0 {
		return query
	}
	query.TagGroups, query.Tags, query.TagsMatch = andTagScope(
		query.TagGroups, query.Tags, query.TagsMatch, scope)
	return query
}

// andTagScope combines the request's own tag filter with the caller's scope.
// The top level of a group list is an AND, so appending the scope narrows the
// filter rather than replacing it. A request written with the flat fields
// becomes one leaf group first, so both constraints still apply; a request with
// no tag filter leaves only the scope.
func andTagScope(groups []storage.TagGroup, tags []string, tagsMatch string, scope []storage.TagGroup) ([]storage.TagGroup, []string, string) {
	requestGroups := groups
	if len(requestGroups) == 0 && (len(tags) > 0 || tagsMatch == storage.TagsMatchExact) {
		mode := tagsMatch
		if mode == "" {
			mode = storage.TagsMatchAny
		}
		requestGroups = []storage.TagGroup{{Tags: tags, Match: mode}}
	}
	combined := make([]storage.TagGroup, 0, len(requestGroups)+len(scope))
	combined = append(combined, requestGroups...)
	combined = append(combined, scope...)
	return combined, nil, ""
}

// ScopeFromRequest returns the caller's resolved read tag scope on a request in
// the storage filter shape, or nil when the request carries none. Handlers that
// read by id use it to confirm the caller may see the row at all.
func ScopeFromRequest(r *http.Request) []storage.TagGroup {
	return tagScopeFromRequest(r)
}

// ApplyMemoryTagScope AND-s the caller's resolved read scope into a memory
// listing's flat tag filter, so a scoped reader pages over the memories it may
// read and the total agrees. A request with no scope is left exactly as the
// caller wrote it; the top level of a group list is an AND, so appending the
// scope narrows the filter rather than replacing it.
func ApplyMemoryTagScope(r *http.Request, tags []string, tagsMatch string) ([]storage.TagGroup, []string, string) {
	scope := tagScopeFromRequest(r)
	if len(scope) == 0 {
		return nil, tags, tagsMatch
	}
	return andTagScope(nil, tags, tagsMatch, scope)
}

// WriteScopeFromRequest returns the tags the caller may write and whether a
// write scope confines them.
func WriteScopeFromRequest(r *http.Request) ([]string, bool) {
	return httpx.WriteTagScopeFrom(r)
}

// tagScopeFromRequest converts the caller's resolved tag scope into the storage
// shape. A request with no scope yields nil.
func tagScopeFromRequest(r *http.Request) []storage.TagGroup {
	scope := httpx.TagScopeFrom(r)
	if len(scope) == 0 {
		return nil
	}
	return toStorageScope(scope)
}

// toStorageScope converts an httpx tag scope tree into the storage filter tree.
func toStorageScope(groups []httpx.TagGroup) []storage.TagGroup {
	out := make([]storage.TagGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, toStorageScopeGroup(group))
	}
	return out
}

func toStorageScopeGroup(group httpx.TagGroup) storage.TagGroup {
	converted := storage.TagGroup{
		Tag:     group.Tag,
		Tags:    group.Tags,
		Resolve: group.Resolve,
		Match:   group.Match,
	}
	if len(group.And) > 0 {
		converted.And = toStorageScope(group.And)
	}
	if len(group.Or) > 0 {
		converted.Or = toStorageScope(group.Or)
	}
	if len(group.Not) > 0 {
		converted.Not = toStorageScope(group.Not)
	}
	return converted
}
