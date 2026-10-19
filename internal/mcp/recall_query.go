package mcp

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// The recall tool's own defaults. The schema declares them, and the server
// applies them again so a client that omits a key gets the reference's
// behaviour rather than a zero value: recall searches on the high budget and
// caps the returned text at 4096 tokens.
const (
	defaultRecallBudget    = "high"
	defaultRecallMaxTokens = 4096
)

// recallRequest decodes the tool's arguments into the HTTP route's recall
// request. Both surfaces share one decoder, so the compound tag filter, the
// query anchor, the score floors and the temporal window are parsed by the same
// rules in both — a malformed filter is refused here exactly as it is there.
func recallRequest(args map[string]any) (memoryops.RecallRequest, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return memoryops.RecallRequest{}, err
	}
	var request memoryops.RecallRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return memoryops.RecallRequest{}, err
	}
	return request, nil
}

// buildRecallQuery resolves a decoded recall request into the storage query,
// applying the tool's defaults and rejecting a malformed anchor or window.
//
// The conversion of the tag filter, the anchor and the window repeats the
// rules the HTTP route applies, because those helpers live behind the
// memory-ops package's private surface. The one rule not reproduced here is the
// derivation of a window from a relative phrase in the query text, which stays
// with that package; an explicit window is honoured in full.
func buildRecallQuery(bankID string, request memoryops.RecallRequest) (storage.RecallQuery, error) {
	timestamp, err := parseToolTimestamp(request.QueryTimestamp)
	if err != nil {
		return storage.RecallQuery{}, errors.New("query_timestamp must be an ISO-8601 date string")
	}
	window, err := parseToolTemporalWindow(request.TemporalWindow)
	if err != nil {
		return storage.RecallQuery{}, errors.New("temporal_window bounds must be ISO-8601 date strings")
	}
	budget := strings.TrimSpace(request.Budget)
	if budget == "" {
		budget = defaultRecallBudget
	}
	maxTokens := defaultRecallMaxTokens
	if request.MaxTokens != nil {
		maxTokens = *request.MaxTokens
	}
	tagsMatch := strings.TrimSpace(request.TagsMatch)
	if tagsMatch == "" {
		tagsMatch = string(storage.TagsMatchAny)
	}
	return storage.RecallQuery{
		BankID:             bankID,
		Query:              request.Query,
		Types:              request.Types,
		PreferObservations: request.PreferObservations,
		Budget:             budget,
		MaxTokens:          maxTokens,
		MinScores:          request.MinScores,
		Tags:               request.Tags,
		TagsMatch:          tagsMatch,
		TagGroups:          toStorageTagGroups(request.TagGroups.Groups),
		Temporal:           window,
		QueryTimestamp:     timestamp,
	}, nil
}

// toStorageTagGroups converts the decoded tag-filter nodes into the storage
// shape, preserving the boolean nesting.
func toStorageTagGroups(groups []memoryops.TagGroupRequest) []storage.TagGroup {
	if len(groups) == 0 {
		return nil
	}
	out := make([]storage.TagGroup, 0, len(groups))
	for _, group := range groups {
		out = append(out, storage.TagGroup{
			And:     toStorageTagGroups(group.And),
			Or:      toStorageTagGroups(group.Or),
			Not:     toStorageTagGroups(group.Not),
			Tag:     group.Tag,
			Tags:    group.Tags,
			Resolve: group.Resolve,
			Match:   group.Match,
		})
	}
	return out
}

// parseToolTimestamp parses the query anchor, reading a naive value as UTC. An
// empty anchor is absent.
func parseToolTimestamp(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseToolISODate(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

// parseToolTemporalWindow parses the caller's window into the storage shape. A
// bound that is present but malformed, or a range that runs backwards, is
// rejected rather than silently matching nothing.
func parseToolTemporalWindow(request *memoryops.TemporalWindowRequest) (*storage.TemporalWindow, error) {
	if request == nil {
		return nil, nil
	}
	// The window model parses its own bounds (and records a rejected one as an
	// item), so an incomplete window is the one whose bounds did not parse.
	if request.Start.IsZero() || request.End.IsZero() {
		return nil, errors.New("incomplete window")
	}
	start, end := request.Start, request.End
	if end.Before(start) {
		return nil, errors.New("inverted window")
	}
	return &storage.TemporalWindow{Start: start, End: end, HasStart: true, HasEnd: true}, nil
}

// parseToolISODate parses an ISO-8601 timestamp, reading a naive value as UTC.
func parseToolISODate(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	for _, layout := range []string{"2006-01-02T15:04:05", "2006-01-02"} {
		if parsed, err := time.Parse(layout, trimmed); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("not an ISO date")
}
