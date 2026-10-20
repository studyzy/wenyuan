package memoryops

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// TagGroupsField decodes the compound tag-filter field: the reference's
// `list[TagGroup]`, whose groups are decoded best effort so a filter the union
// accepts still reaches the store.
type TagGroupsField struct {
	Groups []TagGroupRequest

	// Present records whether the body stated the field at all (an explicit null
	// counts as omitting it, as the reference's default-as-None field means).
	Present bool

	// diagnosed carries the union's own items plus the few facts about the raw
	// value a Go decoder cannot report: whether the field arrived as a list at
	// all, and the value itself for the list-type failure to name. It is what
	// the route reports when it holds any item.
	diagnosed tagGroupTree

	// failures holds the items the reference's left-to-right union reports for
	// the groups that matched none of its branches (`TagGroupLeaf`, `…And`,
	// `…Or`, `…Not`). They are field-level items at the group's own path, so they
	// are reported before any body-level check — the order the framework walks a
	// request in.
	failures []httpx.ValidationError
}

// UnmarshalJSON decodes the field's raw value. The value is kept for the
// diagnostic pass, which the route runs once it knows the field's placement in
// the request (a body the decoder already refused is reported from the walk
// instead). The decoder itself never fails: a value that is not a list decodes
// to no groups and is reported by the diagnostic as the reference's list-type
// failure.
func (f *TagGroupsField) UnmarshalJSON(data []byte) error {
	f.diagnosed = diagnoseTagGroups(data)
	f.Present = f.diagnosed.present
	f.failures = f.diagnosed.items
	if !f.diagnosed.list {
		return nil
	}
	var rawItems []json.RawMessage
	if err := json.Unmarshal(data, &rawItems); err != nil {
		return nil
	}
	for _, item := range rawItems {
		group, err := decodeTagGroup(item)
		if err != nil {
			continue
		}
		f.Groups = append(f.Groups, group)
	}
	return nil
}

// UnmarshalJSON decodes one group, recording which boolean members the body
// stated so the union's constraints can tell a supplied member from an omitted
// one.
func (g *TagGroupRequest) UnmarshalJSON(data []byte) error {
	type plain TagGroupRequest
	var decoded plain
	if err := json.Unmarshal(data, &decoded); err != nil {
		return err
	}
	*g = TagGroupRequest(decoded)
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	g.andGiven = hasMember(members, "and")
	g.orGiven = hasMember(members, "or")
	g.tagsGiven = hasMember(members, "tags")
	return nil
}

// hasMember reports whether a decoded member map states the named key.
func hasMember(members map[string]json.RawMessage, name string) bool {
	_, present := members[name]
	return present
}

// Items reports every validation item the field's value raises: the
// reference's list-type failure when the value was not a list, otherwise the
// union's own items. Both are field-level items, so they are reported before
// the body-level rules the request model raises after its fields validate.
func (f TagGroupsField) Items() []httpx.ValidationError {
	if !f.Present {
		return nil
	}
	if !f.diagnosed.list {
		return []httpx.ValidationError{httpx.TypeMismatch("list_type", []string{"body", "tag_groups"}, f.diagnosed.input)}
	}
	return f.failures
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
			// A child the union cannot read is dropped rather than failing the
			// whole field: the diagnostic has already reported it at that
			// child's own path, and the field's decoder never fails.
			group, err := decodeTagGroup(item)
			if err != nil {
				continue
			}
			out = append(out, group)
		}
		*c = out
		return nil
	}
	group, err := decodeTagGroup(data)
	if err != nil {
		*c = nil
		return nil
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
	// given records that the body stated the member, which a nil Child also
	// reports for an explicit null.
	given bool
}

// UnmarshalJSON accepts one child object and refuses a list.
func (n *TagGroupNot) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		n.Child = nil
		return nil
	}
	n.given = true
	if strings.HasPrefix(trimmed, "[") {
		return nil
	}
	group, err := decodeTagGroup(data)
	if err != nil {
		// The diagnostic reports what the union refused; the store simply gets
		// no child, so the group cannot silently widen the filter.
		n.Child = nil
		return nil
	}
	n.Child = &group
	return nil
}

// decodeTagGroup decodes one tag-group item best effort for the store: a group
// is a leaf object (`{tags, match, resolve}`) or a compound one, and anything
// else the union cannot read is reported by the diagnostic instead.
func decodeTagGroup(item json.RawMessage) (TagGroupRequest, error) {
	var group TagGroupRequest
	if err := json.Unmarshal(item, &group); err != nil {
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	if !group.constrains() {
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	return group, nil
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
