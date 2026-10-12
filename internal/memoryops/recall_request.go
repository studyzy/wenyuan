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

// decodeTagGroup decodes one tag-group item, which may be a tag list or a
// boolean/leaf object.
func decodeTagGroup(item json.RawMessage) (TagGroupRequest, error) {
	trimmed := strings.TrimSpace(string(item))
	if strings.HasPrefix(trimmed, "[") {
		var tags []string
		if err := json.Unmarshal(item, &tags); err != nil {
			return TagGroupRequest{}, err
		}
		return TagGroupRequest{Tags: tags}, nil
	}
	var group TagGroupRequest
	if err := json.Unmarshal(item, &group); err != nil {
		return TagGroupRequest{}, err
	}
	return group, nil
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

// parseTemporalWindow parses the request window into the storage shape. A bound
// that is present but malformed is a bad request; an empty window is ignored.
func parseTemporalWindow(req *TemporalWindowRequest) (*storage.TemporalWindow, error) {
	if req == nil {
		return nil, nil
	}
	window := &storage.TemporalWindow{}
	if strings.TrimSpace(req.Start) != "" {
		start, err := parseISODate(req.Start)
		if err != nil {
			return nil, ErrUnknownTemporalWindow
		}
		window.Start = start
		window.HasStart = true
	}
	if strings.TrimSpace(req.End) != "" {
		end, err := parseISODate(req.End)
		if err != nil {
			return nil, ErrUnknownTemporalWindow
		}
		window.End = end
		window.HasEnd = true
	}
	return window, nil
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
