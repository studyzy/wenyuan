package memoryops

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// TagGroupsField decodes the compound tag-filter field, accepting both the
// boolean-node object form and the legacy list-of-tag-lists form.
type TagGroupsField struct {
	Groups []TagGroupRequest
}

// UnmarshalJSON accepts either an object, a single group, a list of groups or a
// list of tag lists.
func (f *TagGroupsField) UnmarshalJSON(data []byte) error {
	trimmed := strings.TrimSpace(string(data))
	if trimmed == "" || trimmed == "null" {
		f.Groups = nil
		return nil
	}
	// A list-of-lists or list-of-objects form.
	if strings.HasPrefix(trimmed, "[") {
		var rawItems []json.RawMessage
		if err := json.Unmarshal(data, &rawItems); err != nil {
			return err
		}
		for _, item := range rawItems {
			group, err := decodeTagGroup(item)
			if err != nil {
				return err
			}
			f.Groups = append(f.Groups, group)
		}
		return nil
	}
	// A single object form.
	group, err := decodeTagGroup(data)
	if err != nil {
		return err
	}
	f.Groups = []TagGroupRequest{group}
	return nil
}

// TagGroupChildren decodes a node's and/or/not member. The reference accepts
// both a single child object and a list of them, so a filter written as
// {"not": {...}} is as valid as {"not": [{...}]} — the object form is the one a
// caller reaches for when the group has exactly one child.
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
		var tags []string
		if err := json.Unmarshal(item, &tags); err != nil {
			return TagGroupRequest{}, ErrMalformedTagGroup
		}
		if len(tags) == 0 {
			return TagGroupRequest{}, ErrMalformedTagGroup
		}
		return TagGroupRequest{Tags: tags}, nil
	}
	var group TagGroupRequest
	if err := json.Unmarshal(item, &group); err != nil {
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	if !group.constrains() {
		return TagGroupRequest{}, ErrMalformedTagGroup
	}
	return group, nil
}

// constrains reports whether a tag-group node actually filters anything: a leaf
// names tags, and a compound node names at least one child.
func (g TagGroupRequest) constrains() bool {
	if strings.TrimSpace(g.Tag) != "" || len(g.Tags) > 0 {
		return true
	}
	return len(g.And) > 0 || len(g.Or) > 0 || len(g.Not) > 0
}

// toStorageTagGroups converts the decoded request groups into the storage
// shape.
func toStorageTagGroups(groups []TagGroupRequest) []storage.TagGroup {
	out := make([]storage.TagGroup, 0, len(groups))
	for _, g := range groups {
		out = append(out, storage.TagGroup{
			And:     toStorageTagGroups(g.And),
			Or:      toStorageTagGroups(g.Or),
			Not:     toStorageTagGroups(g.Not),
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

// parseTemporalWindow parses the request window into the storage shape. A bound
// that is present but malformed is a bad request; an empty window is ignored.
func parseTemporalWindow(req *TemporalWindowRequest) (*storage.TemporalWindow, error) {
	if req == nil {
		return nil, nil
	}
	// Both bounds are required and the range must be ordered, so an inverted or
	// half-open window is rejected rather than silently matching nothing.
	if strings.TrimSpace(req.Start) == "" || strings.TrimSpace(req.End) == "" {
		return nil, ErrUnknownTemporalWindow
	}
	start, err := parseISODate(req.Start)
	if err != nil {
		return nil, ErrUnknownTemporalWindow
	}
	end, err := parseISODate(req.End)
	if err != nil {
		return nil, ErrUnknownTemporalWindow
	}
	if end.Before(start) {
		return nil, ErrUnknownTemporalWindow
	}
	return &storage.TemporalWindow{Start: start, End: end, HasStart: true, HasEnd: true}, nil
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
