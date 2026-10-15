package storage

import (
	"errors"
	"strings"
	"time"
)

// ReflectProfile resolves a bank's prompt identity: its name, its reflect
// mission and its graded disposition.
//
// A config override wins over the legacy column, exactly as the reference
// resolves the profile: “reflect_mission“ beats “banks.mission“, and each
// “disposition_<trait>“ beats the matching trait in “banks.disposition“.
func ReflectProfile(bank Bank) (name, mission string, disposition map[string]int) {
	name = bank.Name
	mission = bank.Mission
	if value, ok := configStringValue(bank.Config["reflect_mission"]); ok {
		mission = value
	}
	disposition = bank.NormalizedDisposition()
	for _, key := range []string{DispositionKeySkepticism, DispositionKeyLiteralism, DispositionKeyEmpathy} {
		if value, ok := configIntValue(bank.Config["disposition_"+key]); ok {
			disposition[key] = value
		}
	}
	return name, mission, disposition
}

// configStringValue reads a config entry as a string.
func configStringValue(value any) (string, bool) {
	text, ok := value.(string)
	if !ok || strings.TrimSpace(text) == "" {
		return "", false
	}
	return text, true
}

// configIntValue reads a config entry as an integer, accepting the float64 a
// JSON decode produces as well as a native int.
func configIntValue(value any) (int, bool) {
	switch typed := value.(type) {
	case int:
		return typed, true
	case int64:
		return int(typed), true
	case float64:
		return int(typed), true
	}
	return 0, false
}

// Reflection sentinel errors. Callers map each to a distinct public status:
// a no-answer outcome and a tool-loop failure are internal errors, an
// unavailable model is a bad request, a shed retrieval is a retryable
// service-unavailable, and an over-budget reflection is a gateway timeout.
var (
	// ErrReflectNoAnswer reports that the loop ran but produced no answer.
	ErrReflectNoAnswer = errors.New("reflection produced no answer")
	// ErrReflectModelUnavailable reports that the configured model cannot run
	// the reflection.
	ErrReflectModelUnavailable = errors.New("configured model is unavailable for reflection")
	// ErrReflectToolDriving reports that the model cannot drive the tool loop.
	ErrReflectToolDriving = errors.New("model cannot drive the tool loop")
	// ErrReflectRetrieval reports a retrieval-tool failure that is not a shed.
	ErrReflectRetrieval = errors.New("reflection retrieval failed")
	// ErrReflectTimeout reports that the reflection exceeded its wall-clock
	// budget.
	ErrReflectTimeout = errors.New("reflection exceeded its wall-clock budget")
)

// ReflectBudget levels accepted by the effort control. The stored string
// values are the compatibility surface.
const (
	ReflectBudgetLow  = "low"
	ReflectBudgetMid  = "mid"
	ReflectBudgetHigh = "high"
)

// ReflectBudgetLevels returns the accepted effort levels in stable order.
func ReflectBudgetLevels() []string {
	return []string{ReflectBudgetLow, ReflectBudgetMid, ReflectBudgetHigh}
}

// IsReflectBudgetLevel reports whether a value is an accepted effort level.
func IsReflectBudgetLevel(value string) bool {
	return value == ReflectBudgetLow || value == ReflectBudgetMid || value == ReflectBudgetHigh
}

// ReflectInclude selects the optional response blocks. A nil facts pointer
// omits the cited-evidence object; a non-nil one includes it. ToolOutput
// controls whether each trace tool entry carries its output payload; a nil
// pointer means "not false", so the payload is carried.
type ReflectInclude struct {
	Facts      *ReflectFactsInclude
	ToolCalls  bool
	ToolOutput *bool
}

// ReflectFactsInclude carries the nested cited-evidence options.
type ReflectFactsInclude struct {
	MaxTokens int
}

// ReflectDirective is one directive candidate considered for a reflection.
// Tags scope it to matching requests; Scope is the execution scope, where an
// empty value is global.
type ReflectDirective struct {
	ID      string
	Name    string
	Content string
	Tags    []string
	Scope   string
	Active  bool
}

// ReflectMentalModel is one synthesized perspective available to the loop.
type ReflectMentalModel struct {
	ID      string
	Text    string
	Context string
	// Fresh reports whether the perspective is current enough to answer
	// without descending to raw facts; Stale is its inverse.
	Stale bool
}

// ReflectCitation is one cited fact. Field names cross the wall through the
// memory-ops response and are shared with the read path so the same renderer
// works for both.
type ReflectCitation struct {
	ID            string
	Text          string
	Type          string
	Context       string
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	DocumentID    string
	ChunkID       string
	Metadata      map[string]any
	Tags          []string
	Attachments   []RecallAttachment
	// Fresh reports that a perspective is current enough to answer from
	// without descending to raw facts. It is meaningful only for a mental-model
	// citation; a fact never carries it.
	Fresh bool
}

// ReflectMentalModelCitation is one cited synthesized perspective.
type ReflectMentalModelCitation struct {
	ID      string
	Text    string
	Context string
}

// ReflectDirectiveCitation is one applied directive.
type ReflectDirectiveCitation struct {
	ID      string
	Name    string
	Content string
}

// ReflectToolCall is one trace tool-call entry.
type ReflectToolCall struct {
	// ID is the wire call identifier, made unique within the reflection.
	ID         string
	Tool       string
	Input      map[string]any
	Output     map[string]any
	HasOutput  bool
	DurationMs int
	Iteration  int
}

// ReflectModelCall is one trace model-call entry.
type ReflectModelCall struct {
	Scope      string
	DurationMs int
}

// ReflectTrace is the optional execution trace.
type ReflectTrace struct {
	ToolCalls []ReflectToolCall
	LLMCalls  []ReflectModelCall
}

// ReflectUsage reports token accounting for a reflection.
type ReflectUsage struct {
	InputTokens    int
	OutputTokens   int
	TotalTokens    int
	CachedTokens   int
	ThoughtsTokens int
}

// ReflectOutcome is the storage-level reflection result handed back to the
// memory-ops layer for response assembly.
type ReflectOutcome struct {
	// Answer is the synthesized markdown; empty means no answer was produced.
	Answer string
	// RawAnswer is the answer before the visible-length rewrite, used as the
	// fallback when a rewrite yields nothing.
	RawAnswer string
	// Facts, MentalModels and Directives are the cited evidence.
	Facts        []ReflectCitation
	MentalModels []ReflectMentalModelCitation
	Directives   []ReflectDirectiveCitation
	// StructuredOutput is the answer reshaped into the caller's schema, when
	// the request supplied one and the reshape succeeded.
	StructuredOutput map[string]any
	// StructuredOutputError reports why an asked-for reshape produced nothing,
	// so a failed extraction cannot read as an empty one.
	StructuredOutputError string
	// Usage reports token accounting.
	Usage ReflectUsage
	// Trace is present only when the request asked for it.
	Trace *ReflectTrace
	// AvailableTools lists the retrieval tools the loop was allowed to use,
	// so the handler can report which tools a filter removed.
	AvailableTools []string
}

// ReflectQuery is the fully resolved reflection request after decoding,
// validation and default resolution. It carries every field the retrieval loop
// consumes.
type ReflectQuery struct {
	// MaxIterations bounds the model-driven loop's retrieval turns; zero means
	// the runner's default.
	MaxIterations int

	// AnswerAsDocument takes the answer from the loop's `done` call as a
	// structure rather than composing prose afterwards. A mental-model refresh
	// sets it so the stored document is the render of the model's structure.
	AnswerAsDocument bool

	// Reflection, when non-nil, replaces the deterministic in-process
	// reflection with the model-driven agentic loop. The caller supplies it
	// because the loop needs a provider, and the storage area deliberately does
	// not depend on one.
	Reflection ReflectRunner

	BankID string
	// Query is the question after appending the deprecated context field.
	Query string
	// Budget is the effort level (low, mid or high).
	Budget string
	// HasBudget reports whether the request set the effort explicitly.
	HasBudget bool
	// MaxTokens bounds the visible answer length; zero is unbounded.
	MaxTokens int
	// Include gates the optional response blocks.
	Include ReflectInclude
	// ResponseSchema is the optional structured-output schema; nil is absent.
	ResponseSchema map[string]any
	// Types is the resolved fact-type selection; empty means every type.
	Types []string
	// ExcludeMentalModels excludes every synthesized perspective.
	ExcludeMentalModels bool
	// ExcludeMentalModelIDs excludes specific perspectives by identifier.
	ExcludeMentalModelIDs []string
	// Tags and TagGroups express the tag scope; mutually exclusive.
	Tags      []string
	TagsMatch string
	TagGroups []TagGroup
	// ApplyAllDirectives ignores tag scope for directives.
	ApplyAllDirectives bool
	// Directives are the candidate directives for the bank.
	Directives []ReflectDirective
	// MentalModels are the candidate perspectives for the bank.
	MentalModels []ReflectMentalModel
	// SearchObservationsMaxTokens and SearchObservationsIncludeEntities are
	// the resolved per-bank reflection defaults.
	SearchObservationsMaxTokens       int
	SearchObservationsIncludeEntities bool
	// Timeout is the reflection wall-clock budget; zero is unbounded.
	Timeout time.Duration
}
