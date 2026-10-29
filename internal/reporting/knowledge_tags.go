package reporting

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// knowledgeTagFilter is which knowledge pages a tree or search read may return,
// by the tags on their backing mental model. It carries the same three inputs as
// recall — flat tags with a match mode, and compound tag groups — and applies
// them with the same semantics.
type knowledgeTagFilter struct {
	tags      []string
	tagsMatch string
	groups    []knowledgeTagGroup
}

// knowledgeTagGroup is one compound tag-filter node: a boolean node (and/or/not)
// or a leaf naming tags with its own match mode.
//
// The shapes are the reference's exactly (engine/search/tags.py:307-337): `and`
// and `or` hold a *list* of children, while `not` holds a *single* child that is
// negated as a whole. `not` was previously read as a list here, which both
// accepted a document the reference refuses and negated differently.
type knowledgeTagGroup struct {
	And     []knowledgeTagGroup `json:"and"`
	Or      []knowledgeTagGroup `json:"or"`
	Not     *knowledgeTagGroup  `json:"not"`
	Tag     string              `json:"tag"`
	Tags    []string            `json:"tags"`
	Resolve string              `json:"resolve"`
	Match   string              `json:"match"`
}

// active reports whether the filter narrows anything. A filter with no tags and
// no groups filters nothing unless its mode is `exact`, which selects the
// untagged scope on its own.
func (f knowledgeTagFilter) active() bool {
	return len(f.tags) > 0 || len(f.groups) > 0 || f.tagsMatch == storage.TagsMatchExact
}

// matches reports whether a page carrying `tags` is visible under the filter:
// its flat tags must match AND every top-level compound group must match.
func (f knowledgeTagFilter) matches(tags []string) bool {
	if !knowledgeFlatFilterMatches(f.tags, tags, f.tagsMatch) {
		return false
	}
	for _, group := range f.groups {
		if !group.matches(tags) {
			return false
		}
	}
	return true
}

// knowledgeFlatFilterMatches is the reference's `filter_results_by_tags`: an
// empty flat filter (unless the mode is `exact`) passes everything.
func knowledgeFlatFilterMatches(requested, present []string, mode string) bool {
	if mode == "" {
		mode = storage.TagsMatchAny
	}
	if mode == storage.TagsMatchExact {
		return knowledgeSameTagSet(requested, present)
	}
	if len(requested) == 0 {
		return true
	}
	return knowledgeFlatMatch(requested, present, mode)
}

// matches evaluates one compound tag-group node against a page's tags.
func (g knowledgeTagGroup) matches(tags []string) bool {
	switch {
	case g.Not != nil:
		// `not` negates its single child as a whole.
		return !g.Not.matches(tags)
	case len(g.And) > 0:
		for _, child := range g.And {
			if !child.matches(tags) {
				return false
			}
		}
		return true
	case len(g.Or) > 0:
		for _, child := range g.Or {
			if child.matches(tags) {
				return true
			}
		}
		return false
	default:
		return g.leafMatches(tags)
	}
}

// leafMatches evaluates a group leaf: a literal match of its tags under its own
// mode, which defaults to the strict any-match a compound filter implies. A
// `resolve:"fuzzy"` leaf never reaches here: the request's fuzzy leaves are
// rewritten into exact ones against the bank's tag vocabulary before the filter
// is applied, so the leaf that is matched always names real tags.
func (g knowledgeTagGroup) leafMatches(tags []string) bool {
	requested := g.Tags
	if g.Tag != "" {
		requested = append([]string{g.Tag}, requested...)
	}
	mode := g.Match
	if mode == "" {
		mode = storage.TagsMatchAnyStrict
	}
	return knowledgeFlatMatch(requested, tags, mode)
}

// knowledgeFlatMatch applies a flat tag-match mode, mirroring the reference's
// recall semantics: the base overlap and contains modes include untagged pages,
// the strict variants exclude them, and `exact` is set equality with an empty
// request selecting the untagged scope.
func knowledgeFlatMatch(requested, present []string, mode string) bool {
	if mode == "" {
		mode = storage.TagsMatchAny
	}
	switch mode {
	case storage.TagsMatchExact:
		return knowledgeSameTagSet(requested, present)
	case storage.TagsMatchAll:
		if len(present) == 0 {
			return true
		}
		return knowledgeContainsAll(present, requested)
	case storage.TagsMatchAnyStrict:
		if len(present) == 0 {
			return false
		}
		return knowledgeOverlaps(present, requested)
	case storage.TagsMatchAllStrict:
		if len(present) == 0 {
			return false
		}
		return knowledgeContainsAll(present, requested)
	default: // any, and the empty default: overlap, includes untagged.
		if len(present) == 0 {
			return true
		}
		return knowledgeOverlaps(present, requested)
	}
}

func knowledgeUniqueTags(tags []string) map[string]bool {
	out := make(map[string]bool, len(tags))
	for _, tag := range tags {
		out[tag] = true
	}
	return out
}

func knowledgeSameTagSet(a, b []string) bool {
	sa := knowledgeUniqueTags(a)
	sb := knowledgeUniqueTags(b)
	if len(sa) != len(sb) {
		return false
	}
	for tag := range sa {
		if !sb[tag] {
			return false
		}
	}
	return true
}

func knowledgeOverlaps(have, want []string) bool {
	hs := knowledgeUniqueTags(have)
	for _, tag := range want {
		if hs[tag] {
			return true
		}
	}
	return false
}

func knowledgeContainsAll(have, want []string) bool {
	hs := knowledgeUniqueTags(have)
	for _, tag := range want {
		if !hs[tag] {
			return false
		}
	}
	return true
}

// knowledgeTagMatchModes is the set of accepted `tags_match` values.
var knowledgeTagMatchModes = []string{
	storage.TagsMatchAny,
	storage.TagsMatchAll,
	storage.TagsMatchAnyStrict,
	storage.TagsMatchAllStrict,
	storage.TagsMatchExact,
}

// knowledgeTagFilterFromRequest reads the `tags`, `tags_match` and `tag_groups`
// query parameters into a filter, writing the reference's 400 and reporting
// false when `tag_groups` is rejected. It is the single-parameter form used by
// the tree; the search route collects the whole run instead.
func knowledgeTagFilterFromRequest(w http.ResponseWriter, r *http.Request) (knowledgeTagFilter, bool) {
	filter, items, detail := knowledgeTagFilterItems(r)
	if detail != "" {
		// The reference's decoder raises its own HTTPException, whose detail is
		// the errors-repr string rather than an item list, and whose status is
		// the 400 of _decode_tag_groups_query (api/http.py:284-286), not the
		// framework's 422.
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, detail))
		return knowledgeTagFilter{}, false
	}
	if len(items) > 0 {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
		return knowledgeTagFilter{}, false
	}
	return filter, true
}

// knowledgeTagFilterItems reads the three tag parameters without writing a
// response: the rejection items of an out-of-enum `tags_match`, and the
// rendered report `detail` of a `tag_groups` the adapter refused.
//
// `tags_match` is validated before `tag_groups` because the reference declares
// it first: a bad enum and a bad tree in one request report only the enum, the
// tree's failure arriving from a dependency that never runs.
func knowledgeTagFilterItems(r *http.Request) (knowledgeTagFilter, []httpx.ValidationError, string) {
	// `tags_match` is not trimmed: the reference hands the raw string to its
	// enum, so `tags_match=` is a literal error naming "". `tag_groups` is
	// stripped below, the way its decoder reads it.
	filter := knowledgeTagFilter{tags: httpx.QueryValues(r, "tags"), tagsMatch: httpx.Query(r, "tags_match")}
	if filter.tagsMatch == "" && !queryHas(r, "tags_match") {
		filter.tagsMatch = storage.TagsMatchAny
	}
	if !containsKnowledgeString(knowledgeTagMatchModes, filter.tagsMatch) {
		return knowledgeTagFilter{}, []httpx.ValidationError{
			httpx.Literal([]string{"query", "tags_match"}, filter.tagsMatch, knowledgeTagMatchModes),
		}, ""
	}
	// A blank `tag_groups` is treated as absent: the reference's shared decoder
	// returns early on `not raw.strip()` (api/http.py:283), so `tag_groups=`
	// and a whitespace-only value yield no groups at all rather than a parse
	// failure.
	rawGroups := httpx.Query(r, "tag_groups")
	if strings.TrimSpace(rawGroups) == "" {
		return filter, nil, ""
	}
	groups, detail, ok := decodeKnowledgeTagGroups(rawGroups)
	if !ok {
		return knowledgeTagFilter{}, nil, detail
	}
	filter.groups = groups
	return filter, nil, ""
}

// queryHas reports whether the request carries the parameter at all, which
// distinguishes an absent one from an explicit empty value.
func queryHas(r *http.Request, name string) bool {
	_, present := httpx.QueryPresent(r, name)
	return present
}

// resolveKnowledgeFilter resolves a filter's fuzzy tag leaves against the bank's
// own tag vocabulary — the vocabulary recall resolves against, because a page's
// tags are scopes over the bank's memories. A filter with no fuzzy leaf is
// returned unchanged, so the common exact filter reads no vocabulary.
//
// A bank holding more distinct tags than the resolution cap is refused with the
// reference's 422 rather than resolved against a truncated vocabulary: matching
// against a silently shortened one would change which pages match with no
// signal to the caller.
func (h *Handlers) resolveKnowledgeFilter(ctx context.Context, bankID string, filter knowledgeTagFilter) (knowledgeTagFilter, error) {
	groups := toStorageTagGroups(filter.groups)
	if !storage.NeedsTagResolution(groups) {
		return filter, nil
	}
	counts, err := h.Store.ListTags(ctx, bankID)
	if err != nil {
		return filter, err
	}
	if len(counts) > storage.MaxTagVocabulary {
		return filter, httpx.NewHTTPError(httpx.KindUnprocessable, fmt.Sprintf(
			"Bank has %d distinct tags, above the %d that fuzzy tag matching can resolve "+
				"against. Use resolve='exact'.", len(counts), storage.MaxTagVocabulary))
	}
	vocabulary := make([]string, 0, len(counts))
	for _, count := range counts {
		vocabulary = append(vocabulary, count.Tag)
	}
	resolved, err := storage.ResolveTagGroups(groups, vocabulary)
	if err != nil {
		// The resolution cap reports itself through the same 422 envelope as the
		// vocabulary cap, carrying the message the resolver built.
		return filter, httpx.NewHTTPError(httpx.KindUnprocessable, err.Error())
	}
	filter.groups = fromStorageTagGroups(resolved)
	return filter, nil
}

// toStorageTagGroups adapts the query-parameter tag-group shape to the storage
// tag-group the resolver reads, so the resolution rule lives with the tag
// vocabulary and the similarity it shares with recall.
func toStorageTagGroups(groups []knowledgeTagGroup) []storage.TagGroup {
	if groups == nil {
		return nil
	}
	out := make([]storage.TagGroup, 0, len(groups))
	for _, group := range groups {
		var not *storage.TagGroup
		if group.Not != nil {
			not = toStorageTagGroup(group.Not)
		}
		out = append(out, storage.TagGroup{
			And:     toStorageTagGroups(group.And),
			Or:      toStorageTagGroups(group.Or),
			Not:     not,
			Tag:     group.Tag,
			Tags:    group.Tags,
			Resolve: group.Resolve,
			Match:   group.Match,
		})
	}
	return out
}

// toStorageTagGroup adapts one knowledge filter node to the storage shape.
func toStorageTagGroup(group *knowledgeTagGroup) *storage.TagGroup {
	converted := toStorageTagGroups([]knowledgeTagGroup{*group})[0]
	return &converted
}

// fromStorageTagGroup adapts one resolved storage node back to the shape the
// knowledge filter evaluates.
func fromStorageTagGroup(group *storage.TagGroup) *knowledgeTagGroup {
	converted := fromStorageTagGroups([]storage.TagGroup{*group})[0]
	return &converted
}

// fromStorageTagGroups adapts a resolved storage tag-group tree back to the
// shape the knowledge filter evaluates.
func fromStorageTagGroups(groups []storage.TagGroup) []knowledgeTagGroup {
	out := make([]knowledgeTagGroup, 0, len(groups))
	for _, group := range groups {
		var not *knowledgeTagGroup
		if group.Not != nil {
			not = fromStorageTagGroup(group.Not)
		}
		out = append(out, knowledgeTagGroup{
			And:     fromStorageTagGroups(group.And),
			Or:      fromStorageTagGroups(group.Or),
			Not:     not,
			Tag:     group.Tag,
			Tags:    group.Tags,
			Resolve: group.Resolve,
			Match:   group.Match,
		})
	}
	return out
}

// containsKnowledgeString reports membership in a small string set.
func containsKnowledgeString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
