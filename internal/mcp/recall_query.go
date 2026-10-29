package mcp

import (
	"encoding/json"
	"errors"
	"fmt"
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
	// The side-channel budgets the schema states beside their include flags.
	defaultRecallChunkTokens      = 8192
	defaultRecallEntityTokens     = 500
	defaultRecallSourceFactTokens = 4096
)

// recallRequest decodes the tool's arguments into the HTTP route's recall
// request. Both surfaces share one decoder, so the compound tag filter, the
// query anchor, the score floors and the temporal window are parsed by the same
// rules in both — a malformed filter is refused here exactly as it is there.
// The tool declares its include channels as six flat arguments where the HTTP
// body nests them under include, so they are folded into that shape here and
// both the storage query and the response envelope read one object.
func recallRequest(args map[string]any) (memoryops.RecallRequest, error) {
	encoded, err := json.Marshal(args)
	if err != nil {
		return memoryops.RecallRequest{}, err
	}
	var request memoryops.RecallRequest
	if err := json.Unmarshal(encoded, &request); err != nil {
		return memoryops.RecallRequest{}, err
	}
	request.Include = recallInclude(args)
	return request, nil
}

// recallInclude translates the tool's flat include arguments into the HTTP
// route's include object, applying the defaults the schema states: a channel
// the caller did not ask for is absent, and a budget the caller did not bound
// takes the reference's own figure.
func recallInclude(args map[string]any) *memoryops.IncludeRequest {
	include := &memoryops.IncludeRequest{}
	if boolArgument(args, "include_chunks", false) {
		tokens := intArgument(args, "max_chunk_tokens", defaultRecallChunkTokens)
		include.Chunks = &memoryops.IncludeChunkRequest{MaxTokens: &tokens}
	}
	if boolArgument(args, "include_entities", false) {
		tokens := intArgument(args, "max_entity_tokens", defaultRecallEntityTokens)
		include.Entities = &memoryops.IncludeEntityRequest{MaxTokens: &tokens}
	}
	if boolArgument(args, "include_source_facts", false) {
		tokens := intArgument(args, "max_source_facts_tokens", defaultRecallSourceFactTokens)
		include.SourceFacts = &memoryops.IncludeSourceFactRequest{MaxTokens: &tokens}
	}
	return include
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
		// The parser's own sentence is what the reference's recall reports for a
		// rejected anchor, so it is passed through unwrapped.
		return storage.RecallQuery{}, err
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
	// The include channels follow the tool's translated include object: a
	// channel present there is one the store must fill, and the chunk budget is
	// applied again at retrieval so an oversized channel is cut at the source.
	var chunks, entities, sourceFacts bool
	var maxChunkTokens int
	if inc := request.Include; inc != nil {
		entities = inc.Entities != nil
		chunks = inc.Chunks != nil
		sourceFacts = inc.SourceFacts != nil
		if inc.Chunks != nil && inc.Chunks.MaxTokens != nil {
			maxChunkTokens = *inc.Chunks.MaxTokens
		}
	}
	return storage.RecallQuery{
		BankID: bankID,
		Query:  request.Query,
		Types:  request.Types,
		// The MCP tool states the member in its own schema, so false stands here
		// for a request that omitted it; the recall tool resolves the bank's
		// recall_prefer_observations over this, as the HTTP route does.
		PreferObservations: request.PreferObservationsValue(false),
		Budget:             budget,
		MaxTokens:          maxTokens,
		MinScores:          request.MinScores,
		Tags:               request.Tags,
		TagsMatch:          tagsMatch,
		TagGroups:          toStorageTagGroups(request.TagGroups.Groups),
		Temporal:           window,
		QueryTimestamp:     timestamp,
		IncludeEntities:    entities,
		IncludeChunks:      chunks,
		MaxChunkTokens:     maxChunkTokens,
		IncludeSourceFacts: sourceFacts,
	}, nil
}

// toStorageTagNot converts a not member, which holds at most one node.
func toStorageTagNot(n memoryops.TagGroupNot) *storage.TagGroup {
	if n.Child == nil {
		return nil
	}
	converted := toStorageTagGroups([]memoryops.TagGroupRequest{*n.Child})[0]
	return &converted
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
			Not:     toStorageTagNot(group.Not),
			Tag:     group.Tag,
			Tags:    group.Tags,
			Resolve: group.Resolve,
			Match:   group.Match,
		})
	}
	return out
}

// parseToolTimestamp parses the query anchor, reading a naive value as UTC. An
// empty anchor is absent. A value the reference's parse_timestamp would refuse
// is refused with its own sentence, and a placeholder year — an open bound, not
// a date — is refused the same way.
func parseToolTimestamp(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseToolISODate(value)
	if err != nil {
		return nil, fmt.Errorf("Invalid timestamp format '%s'. "+ //nolint:staticcheck // the reference's own wording
			"Expected ISO format like '2024-01-15T10:30:00' or '2024-01-15T10:30:00Z'", value)
	}
	if isPlaceholderYear(parsed) {
		return nil, fmt.Errorf("Timestamp year %d is a placeholder, not a date.", parsed.Year()) //nolint:staticcheck // the reference's own wording, sentence-final
	}
	return &parsed, nil
}

// isPlaceholderYear reports whether a timestamp carries one of the open-bound
// years the reference refuses as a placeholder rather than stores (engine/
// stored_dates.py:14-16): year 1 and year 9999.
func isPlaceholderYear(value time.Time) bool {
	year := value.Year()
	return year == 1 || year == 9999
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
