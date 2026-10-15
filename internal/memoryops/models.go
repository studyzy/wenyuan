package memoryops

import (
	"encoding/json"
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// Validation bounds for the write request.
const (
	MinWriteItemsCount = 1
)

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
}

// UnmarshalJSON applies the reference's default-factory semantics: an omitted
// entities key behaves like the default options object, so entities are
// included, and only an explicit null disables them. Chunks and source facts
// stay excluded unless supplied. Distinguishing "key absent" from "key null"
// needs this decoder, because a plain *IncludeEntityRequest collapses both to
// nil.
func (i *IncludeRequest) UnmarshalJSON(data []byte) error {
	trimmed := trimSpaceBytes(data)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	i.Entities = &IncludeEntityRequest{}
	if raw, ok := fields["entities"]; ok {
		i.Entities = nil
		if string(trimSpaceBytes(raw)) != "null" {
			i.Entities = &IncludeEntityRequest{}
			if err := json.Unmarshal(raw, i.Entities); err != nil {
				return err
			}
		}
	}
	if raw, ok := fields["chunks"]; ok && string(trimSpaceBytes(raw)) != "null" {
		i.Chunks = &IncludeChunkRequest{}
		if err := json.Unmarshal(raw, i.Chunks); err != nil {
			return err
		}
	}
	if raw, ok := fields["source_facts"]; ok && string(trimSpaceBytes(raw)) != "null" {
		i.SourceFacts = &IncludeSourceFactRequest{}
		if err := json.Unmarshal(raw, i.SourceFacts); err != nil {
			return err
		}
	}
	return nil
}

// IncludeEntityRequest carries the entity-state inclusion options.
type IncludeEntityRequest struct {
	MaxTokens int `json:"max_tokens,omitempty"`
}

// IncludeChunkRequest carries the chunk inclusion options.
type IncludeChunkRequest struct {
	MaxTokens int `json:"max_tokens,omitempty"`
}

// IncludeSourceFactRequest carries the source-fact inclusion options.
type IncludeSourceFactRequest struct {
	MaxTokens               int `json:"max_tokens,omitempty"`
	MaxTokensPerObservation int `json:"max_tokens_per_observation,omitempty"`
}

// TemporalWindowRequest is the caller-supplied temporal window. Bounds are
// inclusive; a naive bound is read as UTC.
type TemporalWindowRequest struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// TagGroupRequest is one compound tag-filter node. Its marshal/unmarshal shape
// nests boolean nodes under and/or/not and leaves under tag(s), resolve and
// match.
type TagGroupRequest struct {
	And     TagGroupChildren `json:"and,omitempty"`
	Or      TagGroupChildren `json:"or,omitempty"`
	Not     TagGroupChildren `json:"not,omitempty"`
	Tag     string           `json:"tag,omitempty"`
	Tags    []string         `json:"tags,omitempty"`
	Resolve string           `json:"resolve,omitempty"`
	Match   string           `json:"match,omitempty"`
}

// RecallResultItem is one recall result object. Every field name is public
// surface. Fields without omitempty are always present, defaulting to an empty
// collection, so consumers can rely on their shape.
type RecallResultItem struct {
	ID   string `json:"id"`
	Text string `json:"text"`
	Type string `json:"type"`
	// Context is always present: the reference reports the empty string when a
	// memory carries no context, so a client can read the key unconditionally.
	Context       string            `json:"context"`
	OccurredStart *time.Time        `json:"occurred_start,omitempty"`
	OccurredEnd   *time.Time        `json:"occurred_end,omitempty"`
	MentionedAt   *time.Time        `json:"mentioned_at,omitempty"`
	ChunkID       string            `json:"chunk_id,omitempty"`
	DocumentID    string            `json:"document_id,omitempty"`
	Metadata      map[string]any    `json:"metadata"`
	SourceFactIDs []string          `json:"source_fact_ids"`
	Scores        RecallScoreObject `json:"scores"`
	Entities      []string          `json:"entities"`
	Tags          []string          `json:"tags"`
	Attachments   []Attachment      `json:"attachments"`
}

// RecallScoreObject separates the final ranking score from the reranker
// relevance and the per-strategy retrieval scores. A stage that did not surface
// the result reports no value.
type RecallScoreObject struct {
	Final    float64  `json:"final"`
	Reranker *float64 `json:"reranker,omitempty"`
	Semantic *float64 `json:"semantic,omitempty"`
	Keyword  *float64 `json:"keyword,omitempty"`
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
type EntityObservationEntry struct {
	Text        string     `json:"text"`
	MentionedAt *time.Time `json:"mentioned_at,omitempty"`
}

// ChunkEntry is one entry of the chunk side channel. It is keyed by chunk id at
// the envelope layer.
type ChunkEntry struct {
	ID          string       `json:"id"`
	Text        string       `json:"text"`
	ChunkIndex  int          `json:"chunk_index"`
	Truncated   bool         `json:"truncated"`
	Attachments []Attachment `json:"attachments,omitempty"`
}

// RecallResponse is the recall response envelope with its optional side
// channels.
type RecallResponse struct {
	Results              []RecallResultItem          `json:"results"`
	Entities             map[string]EntityStateEntry `json:"entities,omitempty"`
	Chunks               map[string]ChunkEntry       `json:"chunks,omitempty"`
	SourceFacts          map[string]RecallResultItem `json:"source_facts,omitempty"`
	SourceFactsTruncated bool                        `json:"source_facts_truncated"`
	Trace                map[string]any              `json:"trace,omitempty"`
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
	// Trace is the deprecated top-level trace toggle; the include block takes
	// precedence when present.
	Trace bool `json:"trace,omitempty"`
}

// ReflectIncludeRequest is the reflection include sub-object. The facts member
// enables the cited-evidence block; the tool-calls member enables the
// execution trace; each may carry nested options.
type ReflectIncludeRequest struct {
	Facts     *ReflectFactsIncludeRequest     `json:"facts,omitempty"`
	ToolCalls *ReflectToolCallsIncludeRequest `json:"tool_calls,omitempty"`
}

// ReflectFactsIncludeRequest carries the cited-evidence inclusion options.
type ReflectFactsIncludeRequest struct {
	MaxTokens int `json:"max_tokens,omitempty"`
}

// ReflectToolCallsIncludeRequest carries the trace inclusion options. Output
// controls whether each tool entry carries its output payload; a nil pointer
// means "not false", so the payload is carried.
type ReflectToolCallsIncludeRequest struct {
	Output *bool `json:"output,omitempty"`
}

// ReflectCitedFact is one cited fact. Every field name is public surface and is
// shared with the read path, so one renderer serves both.
type ReflectCitedFact struct {
	ID            string         `json:"id"`
	Text          string         `json:"text"`
	Type          string         `json:"type"`
	Context       string         `json:"context,omitempty"`
	OccurredStart *time.Time     `json:"occurred_start,omitempty"`
	OccurredEnd   *time.Time     `json:"occurred_end,omitempty"`
	MentionedAt   *time.Time     `json:"mentioned_at,omitempty"`
	DocumentID    string         `json:"document_id,omitempty"`
	ChunkID       string         `json:"chunk_id,omitempty"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	Tags          []string       `json:"tags"`
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

// ReflectResponse is the reflection response envelope. Text is required; every
// other field is optional and gated by the request.
type ReflectResponse struct {
	Text                  string         `json:"text"`
	BasedOn               *CitedEvidence `json:"based_on,omitempty"`
	StructuredOutput      map[string]any `json:"structured_output,omitempty"`
	StructuredOutputError string         `json:"structured_output_error,omitempty"`
	Usage                 *ReflectUsage  `json:"usage,omitempty"`
	Trace                 *ReflectTrace  `json:"trace,omitempty"`
}

// Usage reports token accounting for a memory write.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
	TotalTokens  int `json:"total_tokens"`
}
