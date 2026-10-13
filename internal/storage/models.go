package storage

import "time"

// Bank is the stored bank representation. Field names on the JSON representation
// are the compatibility surface and are declared by the bank API area.
type Bank struct {
	ID                    string
	Name                  string
	DispositionSkepticism int
	DispositionLiteralism int
	DispositionEmpathy    int
	ReflectMission        string
	CreatedAt             time.Time
	UpdatedAt             time.Time

	// FactCount is the number of stored memory units owned by the bank.
	FactCount int
	// LastDocumentAt is the last document ingestion time, nil when none.
	LastDocumentAt *time.Time
	// LastWriteAt is the last write watermark, nil for a never-written bank.
	LastWriteAt *time.Time
}

// BankPatch carries a partial bank update; nil fields are left unchanged.
type BankPatch struct {
	Name                  *string
	DispositionSkepticism *int
	DispositionLiteralism *int
	DispositionEmpathy    *int
	ReflectMission        *string
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
	ID            string
	Text          string
	Type          string
	Context       string
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	ChunkID       string
	DocumentID    string
	Metadata      map[string]any
	SourceFactIDs []string
	Attachments   []RecallAttachment
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

// RetainItem is one ordered content item inside a memory write after
// normalization. Content is the flattened canonical text.
type RetainItem struct {
	Content           string
	Timestamp         *time.Time
	HasTimestamp      bool
	Context           string
	Metadata          map[string]any
	DocumentID        string
	Entities          []string
	ResolveEntities   bool
	Tags              []string
	ObservationScopes []string
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
	ID          string
	BankID      string
	DocumentID  string
	Index       int
	Text        string
	ContentHash string
	CreatedAt   time.Time
}

// Entity is a resolved entity identity within a bank.
type Entity struct {
	ID           string
	BankID       string
	Name         string
	Kind         EntityKind
	Metadata     map[string]any
	FirstSeen    *time.Time
	LastSeen     *time.Time
	MentionCount int
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
	Type          MemoryUnitType
	Confidence    float64
	Metadata      map[string]any
	DocumentID    string
	ChunkID       string
	Tags          []string
	// SourceID is the archive-internal fact identity, carried so an
	// export/import round trip is lossless.
	SourceID string
	// ObservationScopes are the observation scopes the unit contributes to.
	ObservationScopes []string
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
type MemoryLink struct {
	ID       string
	BankID   string
	FromID   string
	ToID     string
	LinkType MemoryLinkType
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
