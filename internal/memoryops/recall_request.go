package memoryops

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// TagGroupsField decodes the compound tag-filter field, accepting both the
// boolean-node object form and the legacy list-of-tag-lists form.
type TagGroupsField struct {
	Groups []TagGroupRequest

	// Present records whether the body stated the field at all (an explicit null
	// counts as omitting it, as the reference's default-as-None field means).
	Present bool

	// singleObject records that the body stated the field as one object rather
	// than a list: recall also reads that shape, while reflect's field is
	// `list[TagGroup]` alone and reports it as a list-type failure. rawInput is
	// the value the failure names.
	singleObject bool
	rawInput     any

	// failures holds the items the reference's discriminated union reports for a
	// group that matched none of its four branches (`TagGroupLeaf`, `…And`,
	// `…Or`, `…Not`). They are field-level items at the group's own path, so they
	// are reported before any body-level check — which is the order the framework
	// walks a request in.
	failures []httpx.ValidationError
}

// UnmarshalJSON accepts either an object, a single group, a list of groups or a
// list of tag lists.
func (f *TagGroupsField) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		f.Groups = nil
		f.Present = false
		return nil
	}
	f.Present = true
	// A list-of-lists or list-of-objects form.
	if strings.HasPrefix(trimmed, "[") {
		var rawItems []json.RawMessage
		if err := json.Unmarshal(data, &rawItems); err != nil {
			return err
		}
		for index, item := range rawItems {
			group, err := decodeTagGroup(item)
			if err != nil {
				f.failures = append(f.failures, tagGroupUnionItems(index, item)...)
				continue
			}
			f.Groups = append(f.Groups, group)
		}
		return nil
	}
	// A single object form.
	f.singleObject = true
	_ = json.Unmarshal(data, &f.rawInput)
	group, err := decodeTagGroup(data)
	if err != nil {
		f.failures = append(f.failures, tagGroupUnionItems(0, data)...)
		return nil
	}
	f.Groups = []TagGroupRequest{group}
	return nil
}

// TagGroupChildren decodes a node's and/or member. The reference accepts both a
// single child object and a list of them, so a filter written as
// {"and": {...}} is as valid as {"and": [{...}]}.
//
// A NOT group is not one of these: its member is a single node there
// (`filter: TagGroup`, engine/search/tags.py:333-337) and a list is refused, so
// it has its own type below rather than sharing this lenient one.
type TagGroupChildren []TagGroupRequest

// UnmarshalJSON accepts a single child object or a list of them.
func (c *TagGroupChildren) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		*c = nil
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal(data, &items); err != nil {
			return err
		}
		out := make(TagGroupChildren, 0, len(items))
		for _, item := range items {
			group, err := decodeTagGroup(item)
			if err != nil {
				return err
			}
			out = append(out, group)
		}
		*c = out
		return nil
	}
	group, err := decodeTagGroup(data)
	if err != nil {
		return err
	}
	*c = TagGroupChildren{group}
	return nil
}

// TagGroupNot decodes a node's `not` member: exactly one child node.
//
// The reference models it as `filter: TagGroup` and a list is a validation
// error there, so a list is one here too — accepting it would make a filter
// mean something the reference rejects outright.
type TagGroupNot struct {
	Child *TagGroupRequest
	// listItems holds the children of a list form, which the reference refuses.
	// It is kept so the union branch can report one item per child, the way the
	// reference's does, instead of the whole group failing as unconstrained.
	listItems []json.RawMessage
}

// UnmarshalJSON accepts one child object and refuses a list.
func (n *TagGroupNot) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		n.Child = nil
		return nil
	}
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal(data, &n.listItems); err != nil {
			return err
		}
		return nil
	}
	group, err := decodeTagGroup(data)
	if err != nil {
		return err
	}
	n.Child = &group
	return nil
}

// decodeTagGroup decodes one tag-group item, which may be a tag list or a
// boolean/leaf object.
//
// An object carrying neither a tag set nor one of the boolean keys is rejected.
// Accepting it would decode to a node that constrains nothing, so a client that
// meant to narrow the search would silently get a wider answer than it asked
// for — the one direction a filter must never fail in.
func decodeTagGroup(item json.RawMessage) (TagGroupRequest, error) {
	trimmed := strings.TrimSpace(string(item))
	if strings.HasPrefix(trimmed, "[") {
		// The reference's union has no list branch: a group is a leaf object
		// (`{tags, match, resolve}`) or a compound one, so a bare tag list is a
		// decode failure reported at each branch, not a shorthand.
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	var group TagGroupRequest
	if err := json.Unmarshal(item, &group); err != nil {
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	// A `not` given as a list is not a branch the reference reads: it reports
	// the list as a shape error at every member of the NOT's child union.
	if group.Not.listItems != nil {
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	if !group.constrains() {
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	return group, nil
}

// constrains reports whether a tag-group node is one of the reference's union
// branches: the leaf's required `tags` (a bare `tag` is not a member the union
// reads, so a group carrying only it matches no branch), or one of the compound
// members. A node that is none of them would silently widen a filter, which is
// the one direction a filter must never fail in.
func (g TagGroupRequest) constrains() bool {
	if len(g.Tags) > 0 {
		return true
	}
	return len(g.And) > 0 || len(g.Or) > 0 || g.Not.Child != nil
}

// toStorageTagNot converts a not member, which holds at most one node.
func toStorageTagNot(n TagGroupNot) *storage.TagGroup {
	if n.Child == nil {
		return nil
	}
	converted := toStorageTagGroups([]TagGroupRequest{*n.Child})[0]
	return &converted
}

// toStorageTagGroups converts the decoded request groups into the storage
// shape.
func toStorageTagGroups(groups []TagGroupRequest) []storage.TagGroup {
	out := make([]storage.TagGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, storage.TagGroup{
			And:     toStorageTagGroups(g.And),
			Or:      toStorageTagGroups(g.Or),
			Not:     toStorageTagNot(g.Not),
			Tag:     g.Tag,
			Tags:    g.Tags,
			Resolve: g.Resolve,
			Match:   g.Match,
		})
	}
	return out
}

// acceptedFactTypes are the fact-type values a recall selection accepts.
var acceptedFactTypes = []string{
	string(storage.TypeWorld),
	string(storage.TypeExperience),
	string(storage.TypeObservation),
}

// unsupportedTypes returns the selection values outside the accepted set.
func unsupportedTypes(types []string) []string {
	out := make([]string, 0)
	for _, t := range types {
		if !containsValue(acceptedFactTypes, t) {
			out = append(out, t)
		}
	}
	return out
}

func containsValue(values []string, target string) bool {
	for _, v := range values {
		if v == target {
			return true
		}
	}
	return false
}

// ErrUnknownTemporalWindow reports an unparseable temporal-window bound.
var ErrUnknownTemporalWindow = errors.New("invalid temporal window bound")

// ErrMalformedTagGroup reports a tag group that constrains nothing: a leaf with
// no tags, or an object with none of the boolean keys.
var ErrMalformedTagGroup = errors.New("each tag group must be a non-empty list of tags or an object with and/or/not")

// parseTemporalWindow renders the request window into the storage shape. A bound
// the request could not parse never reaches here: the decode records it as a
// field-level validation item, which is how the reference reports each bound of
// a window at its own path. An unparseable window is ignored.
func parseTemporalWindow(req *TemporalWindowRequest) (*storage.TemporalWindow, error) {
	if req == nil || len(req.fields.items) > 0 {
		return nil, nil
	}
	// Both bounds are required and the range must be ordered, so an inverted or
	// half-open window is rejected before the query is built.
	if req.fields.inverted {
		return nil, ErrUnknownTemporalWindow
	}
	return &storage.TemporalWindow{Start: req.Start, End: req.End, HasStart: true, HasEnd: true}, nil
}

// parseISODate parses an ISO-8601 timestamp, reading a naive value as UTC.
func parseISODate(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.UTC(), nil
		}
	}
	// A naive date or datetime is read as UTC.
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, trimmed); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("not an ISO date")
}

// tagGroupUnionMember names one branch of the reference's discriminated union,
// in the order it tries them, with the member each branch requires.
var tagGroupUnionBranches = []struct{ name, member string }{
	{"TagGroupLeaf", "tags"},
	{"TagGroupAnd", "and"},
	{"TagGroupOr", "or"},
	{"TagGroupNot", "not"},
}

// tagGroupUnionItems builds the items the reference's union reports for a group
// that matched none of its branches: a list-valued group cannot be read as any
// branch's object, and an object is reported as missing each branch's own
// required member.
func tagGroupUnionItems(index int, raw json.RawMessage) []httpx.ValidationError {
	var input any
	_ = json.Unmarshal(raw, &input)
	trimmed := strings.TrimSpace(string(raw))
	items := make([]httpx.ValidationError, 0, len(tagGroupUnionBranches))
	for _, branch := range tagGroupUnionBranches {
		loc := []string{"body", "tag_groups", strconv.Itoa(index), branch.name}
		if !strings.HasPrefix(trimmed, "{") {
			items = append(items, httpx.ValidationItem("model_attributes_type", loc,
				includeShapeMessage, input, nil))
			continue
		}
		// A `not` given as a list satisfies the NOT branch's shape check, so that
		// branch reports its children's shape errors instead of a missing member.
		if branch.name == "TagGroupNot" && tagGroupNotIsList(raw) {
			items = append(items, tagGroupNotListItems(index, raw)...)
			continue
		}
		items = append(items, httpx.Missing(append(loc, branch.member), input))
	}
	return items
}

// tagGroupNotIsList reports whether a group carries `not` as a list, which the
// reference refuses.
func tagGroupNotIsList(raw json.RawMessage) bool {
	var probe struct {
		Not json.RawMessage `json:"not"`
	}
	if err := json.Unmarshal(raw, &probe); err != nil {
		return false
	}
	return strings.HasPrefix(strings.TrimSpace(string(probe.Not)), "[")
}

// tagGroupNotListItems reports a `not` given as a list the way the reference
// does: the list matched the NOT branch's shape check, so the error lands on
// each member of the NOT's own child union rather than on the branch itself.
func tagGroupNotListItems(index int, raw json.RawMessage) []httpx.ValidationError {
	var input any
	_ = json.Unmarshal(raw, &input)
	var child any
	_ = json.Unmarshal(raw, &struct {
		Not any `json:"not"`
	}{Not: &child})
	items := make([]httpx.ValidationError, 0, len(tagGroupUnionBranches))
	for _, branch := range tagGroupUnionBranches {
		loc := []string{"body", "tag_groups", strconv.Itoa(index), "TagGroupNot", "not", branch.name}
		items = append(items, httpx.ValidationItem("model_attributes_type", loc, includeShapeMessage, child, nil))
	}
	return items
}
