package storage

import (
	"encoding/json"
	"time"
)

// Bank is the stored bank representation. Field names on the JSON representation
// are the compatibility surface and are declared by the bank API area.
type Bank struct {
	// BankID is the bank's primary key, named for the reference's column.
	BankID string
	Name   string
	// Disposition is the reference's single jsonb column holding the graded
	// traits. It is one column rather than three because that is the stored
	// shape; the accessors below expose a trait by name so a caller reads a
	// value it can use rather than a map it must know the keys of.
	Disposition map[string]int
	// Mission is the standing instruction applied when the bank reflects.
	Mission   string
	CreatedAt time.Time
	UpdatedAt time.Time

	// FactCount is the number of stored memory units owned by the bank.
	FactCount int
	// LastDocumentAt is the last document ingestion time, nil when none.
	LastDocumentAt *time.Time
	// LastWriteAt is the last write watermark, nil for a never-written bank.
	LastWriteAt *time.Time
}

// Disposition trait keys, which are the reference's own key names inside the
// jsonb object. Declared once so a read and a write cannot disagree on spelling.
const (
	DispositionKeySkepticism = "skepticism"
	DispositionKeyLiteralism = "literalism"
	DispositionKeyEmpathy    = "empathy"
)

// Trait reads one graded disposition by its reference key, returning the neutral
// value when the bank has not set it.
func (b Bank) Trait(key string) int {
	if value, ok := b.Disposition[key]; ok && value != 0 {
		return value
	}
	return DispositionTraitNeutral
}

// WithTrait returns a copy of the disposition with one trait replaced. It copies
// rather than mutates so a shared map cannot be changed through one bank.
func (b Bank) WithTrait(key string, value int) map[string]int {
	next := make(map[string]int, len(b.Disposition)+1)
	for existing, current := range b.Disposition {
		next[existing] = current
	}
	next[key] = value
	return next
}

// NormalizedDisposition returns the disposition with every trait present,
// filling an absent one with the neutral value so a stored row and a freshly
// built value have the same shape.
func (b Bank) NormalizedDisposition() map[string]int {
	return map[string]int{
		DispositionKeySkepticism: b.Trait(DispositionKeySkepticism),
		DispositionKeyLiteralism: b.Trait(DispositionKeyLiteralism),
		DispositionKeyEmpathy:    b.Trait(DispositionKeyEmpathy),
	}
}

// BankPatch carries a partial bank update; nil fields are left unchanged.
type BankPatch struct {
	Name *string
	// Disposition, when set, merges into the bank's traits rather than
	// replacing the set, so a caller setting one trait does not clear the rest.
	Disposition *map[string]int
	Mission     *string
}

// BankAlias is one extra identifier that routes to a bank. Persisted in the
// bank_aliases table; Primary marks the displayed alias.
type BankAlias struct {
	Alias     string
	BankID    string
	Primary   bool
	CreatedAt time.Time
}

// BankQuery is the list query: an optional case-insensitive substring filter
// and a non-negative page window applied after matching.
type BankQuery struct {
	Search string
	Limit  int
	Offset int
}

// BankPage is one page of list results together with the pre-paging match
// count. Rows are ordered most-recently-written first.
type BankPage struct {
	Banks  []Bank
	Total  int
	Limit  int
	Offset int
	// MatchedAliases maps a bank identifier to the aliases that matched the
	// search filter, in match order.
	MatchedAliases map[string][]string
}

// DispositionTraitBounds are the accepted bounds for the three bounded integer
// disposition traits.
const (
	DispositionTraitMin = 1
	DispositionTraitMax = 5
	// DispositionTraitNeutral is the neutral default applied when a bank is
	// created without explicit trait values.
	DispositionTraitNeutral = 3
)

// WriteRequest is a memory write submitted through the retain route.
type WriteRequest struct {
	BankID     string
	Content    string
	Timestamp  *time.Time
	Context    string
	Metadata   map[string]any
	DocumentID string
	Entities   []string
	Tags       []string
	Strategy   string
	UpdateMode string
	// OperationID is the optional caller-supplied idempotency identifier.
	OperationID string
	// Async requests an acceptance response before work completes.
	Async bool
}

// WriteResult is the persisted outcome of a write.
type WriteResult struct {
	OperationID string
	MemoryID    string
	Status      OperationStatus
	Accepted    bool
}

// RecallRequest is the recall read request after decoding and validation.
type RecallRequest struct {
	BankID string
	Query  string
	Types  []string
	// Budget bounds recall work; MaxTokens bounds response size.
	Budget    int
	MaxTokens int
	// MinScores holds per-channel score thresholds.
	MinScores map[string]float64
	TagsMatch string
	TagGroups [][]string
	Include   []string
	Temporal  any
	Trace     bool
	Limit     int
	Offset    int
}

// RecallMatch is one recall result row before envelope assembly.
type RecallMatch struct {
	ID              string
	Text            string
	Type            string
	Context         string
	OccurredStart   *time.Time
	OccurredEnd     *time.Time
	MentionedAt     *time.Time
	ChunkID         string
	DocumentID      string
	Metadata        map[string]any
	SourceMemoryIDs []string
	Attachments     []RecallAttachment
	// Scores carries per-stage values keyed by stage name (semantic, keyword,
	// reranker). A stage that did not surface the result is absent.
	Scores   map[string]float64
	Entities []string
	Tags     []string
	// Final is the combined ranking score; the list is ordered by it.
	Final float64
	// ArmRanks records which retrieval arms surfaced this match and at what
	// rank (1 = best) for fusion and boosting.
	ArmRanks map[string]int
	// ObservationSourceIDs lists the raw fact ids an observation consolidated
	// from; empty for non-observation results.
	ObservationSourceIDs []string
}

// RecallResult is the storage-level recall outcome.
type RecallResult struct {
	Matches     []RecallMatch
	Truncated   bool
	Entities    []string
	Chunks      []string
	SourceFacts []string

	// Rich side channels produced by the recall pipeline.
	EntityStates         []EntityState
	ChunkEntries         []ChunkEntry
	SourceFactMap        map[string]string
	SourceFactsTruncated bool

	// Trace carries the recall debug counts. It is populated by the retrieval
	// pipeline so the trace route can report what was searched without
	// re-deriving it from the result list.
	Trace RecallTrace
}

// RecallTrace is the debug accounting for one recall: how many candidates the
// retrieval arms produced and how the time was spent.
type RecallTrace struct {
	// Budget and BudgetUsed count the candidate budget the pipeline consumed.
	Budget     int
	BudgetUsed int
	// CandidatesFound is the number of raw candidates before ranking.
	CandidatesFound int
	// StageCounts is the per-stage number of matches that carried a score.
	StageCounts map[string]int
	// PhaseMetrics carries per-phase timings in milliseconds.
	PhaseMetrics RecallPhaseMetrics
}

// RecallPhaseMetrics is the per-phase timing of a recall, in milliseconds.
type RecallPhaseMetrics struct {
	RetrievalMillis int
	FusionMillis    int
	AssemblyMillis  int
	TotalMillis     int
}

// ReflectRequest asks the store for retrieval context used by synthesis.
type ReflectRequest struct {
	BankID string
	Query  string
	Limit  int
}

// ReflectContext is the retrieval context handed to the synthesis step.
type ReflectContext struct {
	Matches []RecallMatch
	Trace   map[string]any
}

// RetainEntity is one entity a caller associated with an item. The kind is the
// caller's label for the entity and is used to group entities during
// resolution; it is not stored on the entity row, which carries the
// resolution's own entity-kind classification.
type RetainEntity struct {
	Text string
	Kind string
}

// RetainItem is one ordered content item inside a memory write after
// normalization. Content is the flattened canonical text.
type RetainItem struct {
	Content         string
	Timestamp       *time.Time
	HasTimestamp    bool
	Context         string
	Metadata        map[string]any
	DocumentID      string
	Entities        []RetainEntity
	ResolveEntities bool
	Tags            []string
	// ObservationScopes is the caller's observation scoping as the raw JSON
	// value they sent. The field accepts a scope mode name and a list of
	// explicit tag groups, which are not interchangeable, so the value is
	// stored verbatim rather than flattened to a string list.
	ObservationScopes json.RawMessage
	Strategy          string
	UpdateMode        string
	// AttachmentIDs are the short attachment ids this item's content carries.
	// The file route stores the uploaded bytes and records them here so the
	// units built from the item can name their provenance.
	AttachmentIDs []string
}

// RetainRequest is a normalized memory write submitted through the retain
// route, carrying an ordered item list.
type RetainRequest struct {
	BankID      string
	Items       []RetainItem
	Async       bool
	OperationID string
	// DocumentTags is the deprecated request-level tag alias.
	DocumentTags []string
	// Extraction turns items into facts. A nil value stores each item's text
	// verbatim, which is what a deployment with no model configured gets.
	Extraction FactExtraction
}

// RetainOutcome is the persisted outcome of a retain write.
type RetainOutcome struct {
	// ItemUnitIDs groups created memory-unit identifiers one inner list per
	// submitted item, in item order.
	ItemUnitIDs  [][]string
	Usage        Usage
	OperationID  string
	OperationIDs []string
	// ProcessedContentTokens is zero when nothing was re-extracted and unknown
	// (negative) when the path does not account for it.
	ProcessedContentTokens int
}

// Usage reports token accounting for a write.
type Usage struct {
	InputTokens  int
	OutputTokens int
	TotalTokens  int
}

// Document is a stored document's durable text, hash, metadata and parameters.
type Document struct {
	ID           string
	BankID       string
	OriginalText string
	ContentHash  string
	Metadata     map[string]any
	RetainParams map[string]any
	Tags         []string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	// MemoryUnitCount is how many units the document currently owns. It is
	// derived on read rather than stored, so it cannot drift from the units.
	MemoryUnitCount int
}

// Chunk is one ordered segment of a document.
type Chunk struct {
	ChunkID     string
	BankID      string
	DocumentID  string
	ChunkIndex  int
	ChunkText   string
	ContentHash string
	CreatedAt   time.Time
}

// Entity is a resolved entity identity within a bank.
type Entity struct {
	ID            string
	BankID        string
	CanonicalName string
	EntityKind    EntityKind
	Metadata      map[string]any
	FirstSeen     *time.Time
	LastSeen      *time.Time
	MentionCount  int
}

// MemoryUnit is one extracted fact to persist.
type MemoryUnit struct {
	ID            string
	BankID        string
	Text          string
	Embedding     []float32
	Context       string
	EventDate     *time.Time
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	// FactType is the reference's name for the unit's kind. It is not the
	// caller's label for an entity; that is EntityKind.
	FactType MemoryUnitType
	// ProofCount is how many facts an observation was folded from — evidence
	// strength, not the caller's certainty about the write.
	ProofCount int
	Metadata   map[string]any
	DocumentID string
	ChunkID    string
	Tags       []string
	// SourceMemoryIDs are the reference-side identities of the memories this
	// unit was drawn from, carried so an export/import round trip is lossless.
	SourceMemoryIDs []string
	// ObservationScopes is the scoping the unit was retained under, kept as
	// the raw JSON value so the mode and explicit-group forms both survive a
	// read/write round trip.
	ObservationScopes json.RawMessage
	// ConsolidatedAt and ConsolidationFailedAt record the consolidation pass
	// outcome for the unit.
	ConsolidatedAt        *time.Time
	ConsolidationFailedAt *time.Time
	// AttachmentIDs are the short attachment ids the unit was drawn from.
	AttachmentIDs []string
}

// Attachment is one document's copy of an inline retain attachment. The bytes
// live behind StorageKey, content-addressed by AttachmentHash, so an identical
// attachment dedupes across documents while still being owned by exactly one
// document.
type Attachment struct {
	BankID         string
	DocumentID     string
	AttachmentHash string
	ShortID        string
	MediaType      string
	ByteSize       int64
	StorageKey     string
	Kind           string
	Filename       string
}

// ConsolidationFact is one raw memory unit offered to a consolidation pass as
// new evidence.
type ConsolidationFact struct {
	ID            string
	Text          string
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
}

// ConsolidationObservation is one existing consolidated conclusion offered to a
// consolidation pass as a merge candidate.
type ConsolidationObservation struct {
	ID          string
	Text        string
	ProofCount  int
	SourceIDs   []string
	MentionedAt *time.Time
}

// MemoryLink is one directed link between two memory units.
// MemoryLink has no synthetic identifier: the reference identifies a link by its
// endpoints and its type, and carries a weight and an optional entity the link
// was resolved through.
type MemoryLink struct {
	BankID     string
	FromUnitID string
	ToUnitID   string
	LinkType   MemoryLinkType
	// EntityID is the entity the link was resolved through, empty when the link
	// is not entity-mediated.
	EntityID string
	// Weight is the link's strength, used by the propagation passes.
	Weight    float64
	CreatedAt time.Time
}

// OperationQuery is the operation listing query.
type OperationQuery struct {
	BankID string
	Status string
	Type   string
	// WorkerID narrows the listing to the operations one consumer currently
	// holds, which is what a graceful shutdown scans.
	WorkerID string
	Limit    int
	Offset   int
}

// OperationPage is one page of operations with the pre-paging total.
type OperationPage struct {
	Operations []Operation
	Total      int
	Limit      int
	Offset     int
}
