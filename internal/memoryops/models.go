package memoryops

import (
	"encoding/json"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// includeRequestPath is the JSON path from the request body root to the include
// object. Both request models name the member "include", and the include
// unmarshallers are the only layer that knows a nested member failed, so they
// supply this prefix for a localized decode failure.
var includeRequestPath = []string{"include"}

// includeMemberPath returns the JSON path from the body root to one include
// member: the prefix a member's field-level decode localizes its failures onto.
func includeMemberPath(member string) []string {
	return append(append([]string(nil), includeRequestPath...), member)
}

// includeMemberFailures records the structured items each of an include
// object's members produced when its own fields failed to decode, keyed by
// member name.
//
// The reference reports every invalid member of one request in a single
// validation failure, and every invalid field within one member, so an include
// decoder that stopped at the first failing member — or the first failing field
// of a member — would under-report. The decoders therefore decode each member
// field by field and record all of a member's items here; the validation pass
// splices them back into the include's member order, alongside the shape items
// for members that are not objects at all.
type includeMemberFailures struct {
	items map[string][]httpx.ValidationError
}

// record stores the items a member's decode produced. A member object decodes
// field by field, so one member may carry several items — one per field the
// reference's model rejects — in field-declaration order.
func (f *includeMemberFailures) record(member string, errs []error) {
	items := httpx.NestedFailureItems(errs)
	if len(items) == 0 {
		return
	}
	if f.items == nil {
		f.items = map[string][]httpx.ValidationError{}
	}
	f.items[member] = items
}

// lookup returns the items a member's own decode produced, if any.
func (f *includeMemberFailures) lookup(member string) ([]httpx.ValidationError, bool) {
	items, ok := f.items[member]
	return items, ok
}

// includeMemberFailureSource exposes the decode failures an include decoder
// recorded, so one validation pass serves both request models. The lookup
// tolerates a nil receiver: a request that omitted the include key leaves the
// member pointer nil.
type includeMemberFailureSource interface {
	memberFailure(member string) ([]httpx.ValidationError, bool)
}

// memberFailure returns the items the named member's decode produced, if any.
func (i *IncludeRequest) memberFailure(member string) ([]httpx.ValidationError, bool) {
	if i == nil {
		return nil, false
	}
	return i.failures.lookup(member)
}

// memberFailure returns the items the named member's decode produced, if any.
func (i *ReflectIncludeRequest) memberFailure(member string) ([]httpx.ValidationError, bool) {
	if i == nil {
		return nil, false
	}
	return i.failures.lookup(member)
}

// RecallRequest is the recall body. Every field name is public surface. Fields
// are optional so a request carrying only the query stays valid.
type RecallRequest struct {
	Query              string                 `json:"query"`
	Types              []string               `json:"types,omitempty"`
	Budget             string                 `json:"budget,omitempty"`
	MaxTokens          *int                   `json:"max_tokens,omitempty"`
	MinScores          map[string]float64     `json:"min_scores,omitempty"`
	TagsMatch          string                 `json:"tags_match,omitempty"`
	TagGroups          TagGroupsField         `json:"tag_groups,omitempty"`
	Tags               []string               `json:"tags,omitempty"`
	Include            *IncludeRequest        `json:"include,omitempty"`
	TemporalWindow     *TemporalWindowRequest `json:"temporal_window,omitempty"`
	Trace              bool                   `json:"trace,omitempty"`
	PreferObservations bool                   `json:"prefer_observations,omitempty"`
	QueryTimestamp     string                 `json:"query_timestamp,omitempty"`
}

// IncludeRequest is the recall include sub-object. Entity states are included
// by default unless the entities sub-object is explicitly nulled; chunks and
// source facts are excluded unless their sub-object is supplied.
type IncludeRequest struct {
	Entities    *IncludeEntityRequest     `json:"entities"`
	Chunks      *IncludeChunkRequest      `json:"chunks"`
	SourceFacts *IncludeSourceFactRequest `json:"source_facts"`

	// failures holds the item each member's own decode produced, so every
	// invalid member is reported in one failure rather than only the first.
	failures includeMemberFailures
}

// UnmarshalJSON applies the reference's default-factory semantics: an omitted
// entities key behaves like the default options object, so entities are
// included, and only an explicit null disables them. Chunks and source facts
// stay excluded unless supplied. Distinguishing "key absent" from "key null"
// needs this decoder, because a plain *IncludeEntityRequest collapses both to
// nil.
//
// A member (or the whole value) that is neither an object nor null is left at
// its disabled default rather than rejected here: the reference reports that as
// a validation item naming the member, which ValidateRecall builds from the raw
// body, so the decoder stays lenient and the error keeps its exact location.
//
// A member object is decoded field by field through httpx.DecodeStructFields,
// so every field its model rejects contributes an item and the items stay in
// field-declaration order. The decoder reads the member from a sub-slice, so
// the field decoder's offset is relative to that sub-slice and its path lacks
// the enclosing levels; record rebases each item onto the request body and
// keeps the full path with the offending value. Recording rather than returning
// is deliberate: the reference reports every invalid member of one request in a
// single failure, so the decoder must keep decoding the members that follow.
func (i *IncludeRequest) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" || trimmed[0] != '{' {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil
	}
	i.Entities = &IncludeEntityRequest{}
	if raw, ok := fields["entities"]; ok {
		i.Entities = nil
		if value := trimSpaceBytes(raw); len(value) > 0 && string(value) != "null" {
			i.Entities = &IncludeEntityRequest{}
			if value[0] == '{' {
				i.failures.record("entities", httpx.DecodeStructFields(raw, i.Entities, includeMemberPath("entities")))
			}
		}
	}
	if raw, ok := fields["chunks"]; ok && string(trimSpaceBytes(raw)) != "null" {
		i.Chunks = &IncludeChunkRequest{}
		if value := trimSpaceBytes(raw); len(value) > 0 && value[0] == '{' {
			i.failures.record("chunks", httpx.DecodeStructFields(raw, i.Chunks, includeMemberPath("chunks")))
		}
	}
	if raw, ok := fields["source_facts"]; ok && string(trimSpaceBytes(raw)) != "null" {
		i.SourceFacts = &IncludeSourceFactRequest{}
		if value := trimSpaceBytes(raw); len(value) > 0 && value[0] == '{' {
			i.failures.record("source_facts", httpx.DecodeStructFields(raw, i.SourceFacts, includeMemberPath("source_facts")))
		}
	}
	return nil
}

// IncludeEntityRequest carries the entity-state inclusion options. MaxTokens
// is a pointer so an omitted value is distinguishable from an explicit zero:
// the reference defaults an omitted budget to 500 tokens.
type IncludeEntityRequest struct {
	MaxTokens *int `json:"max_tokens,omitempty"`
}

// IncludeChunkRequest carries the chunk inclusion options. An omitted budget
// defaults to 8192 tokens.
type IncludeChunkRequest struct {
	MaxTokens *int `json:"max_tokens,omitempty"`
}

// IncludeSourceFactRequest carries the source-fact inclusion options. An
// omitted total budget defaults to 4096 tokens and an omitted per-observation
// budget to -1 (unlimited).
type IncludeSourceFactRequest struct {
	MaxTokens               *int `json:"max_tokens,omitempty"`
	MaxTokensPerObservation *int `json:"max_tokens_per_observation,omitempty"`
}

// TemporalWindowRequest is the caller-supplied temporal window. Bounds are
// inclusive; a naive bound is read as UTC.
type TemporalWindowRequest struct {
	// Start and End hold the parsed bounds. The reference's window carries two
	// required `datetime` fields, so both are always present when the window
	// itself parsed; a missing or unparseable bound is reported as a field-level
	// validation item rather than as a bad request (see ValidateRecall).
	Start time.Time `json:"-"`
	End   time.Time `json:"-"`

	// members holds the window object's own key/value pairs, which is the input a
	// missing bound reports; fields holds each bound's own decode outcome, so the
	// validation pass can report both bounds of one window.
	members map[string]any
	fields  temporalWindowFields
}

// temporalWindowFields records what each bound of a window contributed: the item
// for a bound that is missing or rejected, and the range check the window model
// raises once both bounds parsed.
type temporalWindowFields struct {
	items    []httpx.ValidationError
	inverted bool
}

// UnmarshalJSON decodes a window bound the way the reference's `datetime` field
// does: an ISO date or datetime string, or a number read as a Unix timestamp.
// A bound that is absent, null or unparseable is recorded as an item at its own
// path instead of failing the whole body, because the reference reports both
// bounds of a window in one response.
func (w *TemporalWindowRequest) UnmarshalJSON(data []byte) error {
	if w == nil {
		return nil
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		return err
	}
	w.members = nil
	if err := json.Unmarshal(data, &w.members); err != nil {
		w.members = map[string]any{}
	}
	if w.members == nil {
		w.members = map[string]any{}
	}
	start, startFailure := decodeTemporalBound(w.members, members, "start")
	end, endFailure := decodeTemporalBound(w.members, members, "end")
	w.fields.items = nil
	for _, failure := range []*httpx.ValidationError{startFailure, endFailure} {
		if failure != nil {
			w.fields.items = append(w.fields.items, *failure)
		}
	}
	w.fields.inverted = startFailure == nil && endFailure == nil && end.Before(start)
	w.Start, w.End = start, end
	return nil
}

// decodeTemporalBound decodes one bound, returning the item for a bound that is
// missing, null or unparseable.
func decodeTemporalBound(window map[string]any, members map[string]json.RawMessage, name string) (time.Time, *httpx.ValidationError) {
	loc := []string{"body", "temporal_window", name}
	raw, present := members[name]
	if !present {
		return time.Time{}, &httpx.ValidationError{Type: "missing", Loc: loc, Msg: "Field required", Input: window}
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return time.Time{}, &httpx.ValidationError{Type: pythonDateTimeTypeKind, Loc: loc, Msg: pythonDateTimeTypeMessage}
	}
	switch typed := value.(type) {
	case string:
		parsed, err := parsePythonDateTime(typed)
		if err != nil {
			item := pythonDateTimeItem(loc, typed, err)
			return time.Time{}, &item
		}
		return parsed, nil
	case float64:
		item := pythonDateTimeItem(loc, typed, formatUnixTimestampError(typed))
		parsed := unixTimestampToTime(typed)
		if !parsed.IsZero() {
			// A numeric bound the range check accepts is the instant it names.
			return parsed, nil
		}
		return time.Time{}, &item
	default:
		input := value
		item := httpx.ValidationItem(pythonDateTimeTypeKind, loc, pythonDateTimeTypeMessage, input, nil)
		return time.Time{}, &item
	}
}

// TagGroupRequest is one compound tag-filter node. Its marshal/unmarshal shape
// nests boolean nodes under and/or/not and leaves under tag(s), resolve and
// match. A member's *presence* matters to the union (`and`, `or` and `not` are
// required members of their branches), so each container is a pointer: an
// omitted member must not decode to the same empty value a supplied one does.
type TagGroupRequest struct {
	And TagGroupChildren `json:"and,omitempty"`
	Or  TagGroupChildren `json:"or,omitempty"`
	Not TagGroupNot      `json:"not,omitempty"`

	// andGiven and orGiven record that the body stated the member, which a nil
	// slice cannot: `null` supplies the member while `[]` and a bare object both
	// decode to an empty list.
	andGiven bool
	orGiven  bool

	// tagsGiven records that the body stated `tags`, which an empty slice cannot:
	// `"tags": []` is a leaf that names no tag and still filters, whereas a node
	// without `tags` is not a leaf at all.
	tagsGiven bool

	Tag     string   `json:"tag,omitempty"`
	Tags    []string `json:"tags,omitempty"`
	Resolve string   `json:"resolve,omitempty"`
	Match   string   `json:"match,omitempty"`
}

// RecallResultItem is one recall result object. Every field name is public
// surface. The reference keeps nulls for this route, so every optional member
// is present on the wire and reports null when the fact carries no value; a
// strict client can therefore read a key unconditionally.
type RecallResultItem struct {
	ID            string            `json:"id"`
	Text          string            `json:"text"`
	Type          *string           `json:"type"`
	Entities      []string          `json:"entities"`
	Context       *string           `json:"context"`
	OccurredStart *string           `json:"occurred_start"`
	OccurredEnd   *string           `json:"occurred_end"`
	MentionedAt   *string           `json:"mentioned_at"`
	DocumentID    *string           `json:"document_id"`
	Metadata      map[string]any    `json:"metadata"`
	ChunkID       *string           `json:"chunk_id"`
	Tags          []string          `json:"tags"`
	SourceFactIDs []string          `json:"source_fact_ids"`
	Scores        RecallScoreObject `json:"scores"`
	Attachments   []Attachment      `json:"attachments"`
}

// RecallScoreObject separates the final ranking score from the reranker
// relevance and the per-strategy retrieval scores. Every stage is present on
// the wire: the reference keeps nulls for this route, so a stage that did not
// surface the result reports null rather than disappearing — a client reading
// `scores.semantic` unconditionally must not hit a missing key.
type RecallScoreObject struct {
	Final    float64  `json:"final"`
	Reranker *float64 `json:"reranker"`
	Semantic *float64 `json:"semantic"`
	Keyword  *float64 `json:"keyword"`
}

// Attachment is an optional recall-result side value.
type Attachment struct {
	ID   string `json:"id"`
	Kind string `json:"kind,omitempty"`
	URI  string `json:"uri,omitempty"`
	// The reflect citation shape carries the fetched bytes' identity instead of
	// a bare uri: hash, media_type, byte_size and url. recall results keep uri;
	// both are emitted so one type serves both renderers.
	Hash      string `json:"hash,omitempty"`
	MediaType string `json:"media_type,omitempty"`
	ByteSize  int64  `json:"byte_size,omitempty"`
	Filename  string `json:"filename,omitempty"`
	URL       string `json:"url,omitempty"`
}

// attachmentFromRecall maps a storage recall attachment into the response
// shape, filling both the recall-side uri and the citation-side url with the
// same bank-scoped path.
func attachmentFromRecall(a storage.RecallAttachment) Attachment {
	return Attachment{
		ID:        a.ID,
		Kind:      a.Kind,
		URI:       a.URI,
		Hash:      a.Hash,
		MediaType: a.MediaType,
		ByteSize:  a.ByteSize,
		Filename:  a.Filename,
		URL:       a.URI,
	}
}

// EntityStateEntry is one entry of the entity-state side channel. It is keyed
// by entity name at the envelope layer.
type EntityStateEntry struct {
	EntityID      string                   `json:"entity_id"`
	CanonicalName string                   `json:"canonical_name"`
	Observations  []EntityObservationEntry `json:"observations"`
}

// EntityObservationEntry is one observation carried on an entity-state entry.
// mentioned_at is present and null when the observation carries no stamp.
type EntityObservationEntry struct {
	Text        string  `json:"text"`
	MentionedAt *string `json:"mentioned_at"`
}

// ChunkEntry is one entry of the chunk side channel. It is keyed by chunk id at
// the envelope layer. attachments is present and null when the chunk
// references none.
type ChunkEntry struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	ChunkIndex  int          `json:"chunk_index"`
	Truncated   bool         `json:"truncated"`
	Attachments []Attachment `json:"attachments"`
}

// RecallResponse is the recall response envelope with its optional side
// channels. The reference keeps nulls for this route and gates no key, so every
// member is always on the wire: an absent side channel reports null rather than
// disappearing, and source_facts_truncated is null unless source facts were
// requested.
type RecallResponse struct {
	Results              []RecallResultItem          `json:"results"`
	Trace                map[string]any              `json:"trace"`
	Entities             map[string]EntityStateEntry `json:"entities"`
	Chunks               map[string]ChunkEntry       `json:"chunks"`
	SourceFacts          map[string]RecallResultItem `json:"source_facts"`
	SourceFactsTruncated *bool                       `json:"source_facts_truncated"`
}

// ReflectRequest is the reflection body. Every field name is public surface,
// and every field is optional so a request carrying only the query stays valid.
// The deprecated Context field is appended to Query before validation.
type ReflectRequest struct {
	Query                                    string                 `json:"query"`
	Context                                  string                 `json:"context,omitempty"`
	Budget                                   string                 `json:"budget,omitempty"`
	MaxTokens                                *int                   `json:"max_tokens,omitempty"`
	Include                                  *ReflectIncludeRequest `json:"include,omitempty"`
	ResponseSchema                           map[string]any         `json:"response_schema,omitempty"`
	Tags                                     []string               `json:"tags,omitempty"`
	TagsMatch                                string                 `json:"tags_match,omitempty"`
	TagGroups                                TagGroupsField         `json:"tag_groups,omitempty"`
	ApplyAllDirectives                       bool                   `json:"apply_all_directives,omitempty"`
	FactTypes                                []string               `json:"fact_types,omitempty"`
	ExcludeMentalModels                      bool                   `json:"exclude_mental_models,omitempty"`
	ExcludeMentalModelIDs                    []string               `json:"exclude_mental_model_ids,omitempty"`
	ReflectSearchObservationsMaxTokens       *int                   `json:"reflect_search_observations_max_tokens,omitempty"`
	ReflectSearchObservationsIncludeEntities *bool                  `json:"reflect_search_observations_include_entities,omitempty"`
}

// ReflectIncludeRequest is the reflection include sub-object. The facts member
// enables the cited-evidence block; the tool-calls member enables the
// execution trace; each may carry nested options.
type ReflectIncludeRequest struct {
	Facts     *ReflectFactsIncludeRequest     `json:"facts,omitempty"`
	ToolCalls *ReflectToolCallsIncludeRequest `json:"tool_calls,omitempty"`

	// failures holds the item each member's own decode produced, so every
	// invalid member is reported in one failure rather than only the first.
	failures includeMemberFailures
}

// UnmarshalJSON keeps a member (or the whole value) that is neither an object
// nor null at its disabled default rather than rejecting it here. The reference
// reports such a value as a validation item naming it, which ValidateReflect
// builds from the raw body, so the decoder stays lenient and the error keeps its
// exact location. A member object whose own fields fail to decode is recorded
// through includeMemberFailures.record for the same reason as the recall
// include: the reference reports every invalid member of one request in a single
// failure, so the decoder keeps decoding the members that follow. Each member is
// decoded field by field, so every field its model rejects contributes an item
// in field-declaration order.
func (i *ReflectIncludeRequest) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" || trimmed[0] != '{' {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return nil
	}
	if raw, ok := fields["facts"]; ok && string(trimSpaceBytes(raw)) != "null" {
		i.Facts = &ReflectFactsIncludeRequest{}
		if value := trimSpaceBytes(raw); len(value) > 0 && value[0] == '{' {
			i.failures.record("facts", httpx.DecodeStructFields(raw, i.Facts, includeMemberPath("facts")))
		}
	}
	if raw, ok := fields["tool_calls"]; ok && string(trimSpaceBytes(raw)) != "null" {
		i.ToolCalls = &ReflectToolCallsIncludeRequest{}
		if value := trimSpaceBytes(raw); len(value) > 0 && value[0] == '{' {
			i.failures.record("tool_calls", httpx.DecodeStructFields(raw, i.ToolCalls, includeMemberPath("tool_calls")))
		}
	}
	return nil
}

// ReflectFactsIncludeRequest carries the cited-evidence inclusion options. The
// reference's model declares no fields — the member is a pure enable switch —
// so any member it carries is ignored and only its presence matters.
type ReflectFactsIncludeRequest struct{}

// ReflectToolCallsIncludeRequest carries the trace inclusion options. Output
// controls whether each tool entry carries its output payload; a nil pointer
// means "not false", so the payload is carried.
type ReflectToolCallsIncludeRequest struct {
	Output *bool `json:"output,omitempty"`
}

// ReflectCitedFact is one cited fact. Every field name is public surface and is
// shared with the read path, so one renderer serves both. The reference keeps
// nulls here, so every optional member is present and reports null when the
// fact carries no value.
type ReflectCitedFact struct {
	ID            string         `json:"id"`
	Text          string         `json:"text"`
	Type          *string        `json:"type"`
	Context       *string        `json:"context"`
	OccurredStart *string        `json:"occurred_start"`
	OccurredEnd   *string        `json:"occurred_end"`
	MentionedAt   *string        `json:"mentioned_at"`
	DocumentID    *string        `json:"document_id"`
	ChunkID       *string        `json:"chunk_id"`
	Tags          []string       `json:"tags"`
	Metadata      map[string]any `json:"metadata"`
	Attachments   []Attachment   `json:"attachments"`
}

// ReflectCitedMentalModel is one cited synthesized perspective.
type ReflectCitedMentalModel struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	Context string `json:"context,omitempty"`
}

// ReflectCitedDirective is one applied directive.
type ReflectCitedDirective struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Content string `json:"content"`
}

// CitedEvidence is the cited-evidence object. It is always an object with the
// three named collections, each an array, and is never a bare list.
type CitedEvidence struct {
	Memories     []ReflectCitedFact        `json:"memories"`
	MentalModels []ReflectCitedMentalModel `json:"mental_models"`
	Directives   []ReflectCitedDirective   `json:"directives"`
}

// ReflectToolCall is one trace tool-call entry.
type ReflectToolCall struct {
	Tool       string         `json:"tool"`
	Input      map[string]any `json:"input"`
	Output     map[string]any `json:"output,omitempty"`
	DurationMs int            `json:"duration_ms"`
	Iteration  int            `json:"iteration"`
}

// ReflectModelCall is one trace model-call entry.
type ReflectModelCall struct {
	Scope      string `json:"scope"`
	DurationMs int    `json:"duration_ms"`
}

// ReflectTrace is the optional execution trace.
type ReflectTrace struct {
	ToolCalls []ReflectToolCall  `json:"tool_calls"`
	LLMCalls  []ReflectModelCall `json:"llm_calls"`
}

// ReflectUsage reports token accounting for a reflection.
type ReflectUsage struct {
	InputTokens    int `json:"input_tokens"`
	OutputTokens   int `json:"output_tokens"`
	TotalTokens    int `json:"total_tokens"`
	CachedTokens   int `json:"cached_tokens"`
	ThoughtsTokens int `json:"thoughts_tokens"`
}

// ReflectResponse is the reflection response envelope. Text is required; the
// reference keeps nulls for this route and gates no key, so every other member
// is always on the wire and reports null when the request did not ask for it.
type ReflectResponse struct {
	Text                  string         `json:"text"`
	BasedOn               *CitedEvidence `json:"based_on"`
	StructuredOutput      map[string]any `json:"structured_output"`
	StructuredOutputError *string        `json:"structured_output_error"`
	Usage                 *ReflectUsage  `json:"usage"`
	Trace                 *ReflectTrace  `json:"trace"`
}

// Usage reports token accounting for a memory write. cached_tokens and
// thoughts_tokens are reported on the wire; the deterministic write path does
// not account for them, so they stay zero.
type Usage struct {
	InputTokens    int `json:"input_tokens"`
	OutputTokens   int `json:"output_tokens"`
	TotalTokens    int `json:"total_tokens"`
	CachedTokens   int `json:"cached_tokens"`
	ThoughtsTokens int `json:"thoughts_tokens"`
}
