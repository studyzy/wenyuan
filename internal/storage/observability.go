package storage

import (
	"context"
	"errors"
	"time"
)

// ErrAuditEntryNotFound is returned when an audit identifier is unknown.
var ErrAuditEntryNotFound = errors.New("audit entry not found")

// AuditEntry is one recorded API interaction.
type AuditEntry struct {
	ID        string
	Action    string
	Transport string
	BankID    string
	StartedAt time.Time
	EndedAt   *time.Time
	Request   map[string]any
	Response  map[string]any
	Metadata  map[string]any
}

// AuditQuery pages and filters the audit trail.
type AuditQuery struct {
	BankID    string
	Action    string
	Transport string
	// StartDate and EndDate bound the window on the entry's start time,
	// inclusive at the start and exclusive at the end.
	StartDate *time.Time
	EndDate   *time.Time
	Limit     int
	Offset    int
}

// AuditPage is one page of audit entries with the pre-paging total.
type AuditPage struct {
	Entries []AuditEntry
	Total   int
	Limit   int
	Offset  int
}

// AuditBucket is one time bucket of the audit histogram.
type AuditBucket struct {
	Time    time.Time
	Actions map[string]int
	Total   int
}

// AuditStats is the audit histogram over a period.
type AuditStats struct {
	BankID string
	Period string
	Trunc  string
	Start  time.Time
	Bucket []AuditBucket
}

// LLMRequest is one recorded model call.
type LLMRequest struct {
	ID             string
	BankID         string
	Operation      string
	Scope          string
	TraceID        string
	SpanID         string
	ParentSpanID   string
	Provider       string
	Model          string
	Status         string
	StartedAt      time.Time
	EndedAt        *time.Time
	DurationMS     *int
	InputTokens    *int
	OutputTokens   *int
	CachedTokens   *int
	ThoughtsTokens *int
	TotalTokens    *int
	Input          any
	Output         any
	Error          string
	LLMInfo        map[string]any
	Metadata       map[string]any
}

// LLMRequestQuery pages and filters the model-request trace.
type LLMRequestQuery struct {
	BankID    string
	Status    string
	Operation string
	Scope     string
	Provider  string
	TraceID   string
	// DocumentID matches the trace's recorded document, carried in the row's
	// metadata.
	DocumentID string
	// DocumentIDSet reports whether the caller supplied document_id at all. The
	// reference filters on `document_id is not None`, so an explicitly empty
	// value is a real filter — a row whose recorded document id is empty matches
	// it — rather than an absent one.
	DocumentIDSet bool
	// MemoryID matches a trace that produced the unit (metadata.memory_ids) or
	// consumed it as a consolidation source (metadata.source_memory_ids).
	MemoryID string
	// MemoryIDSet reports whether the caller supplied memory_id, so an empty
	// value is a real filter for the same reason as DocumentIDSet.
	MemoryIDSet bool
	// Group paginates by operation run (all calls sharing a trace_id) rather
	// than by call, so every returned run is whole. Limit, offset and total
	// then count runs.
	Group bool
	// StartDate and EndDate bound the window on the call's start time,
	// inclusive at the start and exclusive at the end.
	StartDate *time.Time
	EndDate   *time.Time
	Limit     int
	Offset    int
}

// LLMRequestPage is one page of model requests with the pre-paging total.
type LLMRequestPage struct {
	Requests []LLMRequest
	Total    int
	Limit    int
	Offset   int
}

// LLMTokenSums is the token accounting within one bucket.
type LLMTokenSums struct {
	InputTokens    int
	OutputTokens   int
	CachedTokens   int
	ThoughtsTokens int
	TotalTokens    int
}

// LLMRequestBucket is one time bucket of the model-request histogram.
type LLMRequestBucket struct {
	Time     time.Time
	Statuses map[string]int
	Total    int
	Tokens   LLMTokenSums
}

// LLMRequestStats is the model-request histogram over a period.
type LLMRequestStats struct {
	BankID string
	Period string
	Trunc  string
	Start  time.Time
	Bucket []LLMRequestBucket
}

// maxLinksPerEntity is the historical writer cap on how many bidirectional
// entity edges one shared entity contributes to a bank's link total: for an
// entity shared by n units, LEAST(n-1, maxLinksPerEntity). Entity links are no
// longer stored, so the stats surface derives their total from unit/entity
// membership under this same cap.
const maxLinksPerEntity = 10

// BankStatsRefresher is the optional capability a store implements when it can
// recompute a bank summary bypassing its cache, as the reference's force_refresh
// does (the freshly computed value also refreshes the cache for later callers).
// The reporting handler asks for it when a request carries refresh=true; a store
// that does not implement it serves the cached summary unchanged.
type BankStatsRefresher interface {
	BankStatsRefreshing(ctx context.Context, bankID string, forceRefresh bool) (BankStats, error)
}

// AsyncOperationGroup is one bucket of the async-operation queue: how many
// non-terminal operations of one type and status a deployment holds. BankID is
// set only when the caller asked for a per-bank breakdown.
type AsyncOperationGroup struct {
	OperationType string
	Status        string
	BankID        string
	Count         int
}

// AsyncOperationCounter is the optional capability a store implements to group
// its non-terminal async operations by type and status, the shape the reference
// renders as its hindsight_async_operations gauge. A store that does not
// implement it reports no async-operation series.
type AsyncOperationCounter interface {
	CountAsyncOperations(ctx context.Context, perBank bool) ([]AsyncOperationGroup, error)
}

// BankStats is the per-bank operational summary.
type BankStats struct {
	BankID               string
	TotalNodes           int
	TotalLinks           int
	TotalDocuments       int
	NodesByFactType      map[string]int
	LinksByLinkType      map[string]int
	LinksByFactType      map[string]int
	LinksBreakdown       map[string]map[string]int
	PendingOperations    int
	FailedOperations     int
	OperationsByStatus   map[string]int
	LastConsolidatedAt   *time.Time
	LastMemoryWriteAt    *time.Time
	PendingConsolidation int
	FailedConsolidation  int
	TotalObservations    int
}

// TimeseriesBucket is one bucket of the memory-count histogram. The per-type
// counts let a client chart the composition of each bucket rather than only its
// total.
type TimeseriesBucket struct {
	Time        time.Time
	Count       int
	World       int
	Experience  int
	Observation int
}

// Timeseries is the memory-count histogram over a period.
type Timeseries struct {
	BankID    string
	Period    string
	Trunc     string
	TimeField string
	Buckets   []TimeseriesBucket
}

// MemoryAttachmentStore is the optional capability a store implements when it
// can resolve the attachments a memory's evidence came from. It is a narrow
// capability interface rather than part of Store so a store that does not carry
// attachments (and every test fake) is unaffected.
type MemoryAttachmentStore interface {
	// MemoryAttachments returns each unit's attachments, keyed by unit id. An
	// observation reports the attachments of the facts it was consolidated from.
	MemoryAttachments(ctx context.Context, bankID string, unitIDs []string) (map[string][]Attachment, error)
}

// ObservationScope is one distinct observation scope recorded for a bank. The
// scope is the exact set of tags its observations were consolidated with, tag
// order normalized; the empty list is the global/untagged scope.
type ObservationScope struct {
	Tags  []string
	Count int
}

// ObservationScopePage is one page of observation scopes.
type ObservationScopePage struct {
	Scopes []ObservationScope
	Total  int
	Limit  int
	Offset int
}

// KnowledgeKind selects which kind of knowledge node a record is.
type KnowledgeKind string

// The accepted knowledge-node kinds.
const (
	KnowledgeKindFolder KnowledgeKind = "folder"
	KnowledgeKindPage   KnowledgeKind = "page"
)

// ErrKnowledgeNodeNotFound is returned when a knowledge node identifier is
// unknown to a bank.
var ErrKnowledgeNodeNotFound = errors.New("knowledge node not found")

// ErrMemoryNotFound is returned when a memory identifier is unknown to a bank.
var ErrMemoryNotFound = errors.New("memory not found")

// KnowledgeNode is one node of a bank's knowledge tree. A page is backed by a
// mental model that holds its text and tags; a folder is a container with none.
type KnowledgeNode struct {
	ID       string
	BankID   string
	ParentID string
	Kind     KnowledgeKind
	Title    string
	// MentalModelID names the model behind a page, empty for a folder. It is
	// the row's identity in mental_models, so a reader can reach the page's
	// content through either surface.
	MentalModelID string
	Text          string
	Tags          []string
	// Managed reports whether the page was created by the API rather than the
	// user, and SortOrder positions it among its siblings.
	Managed   bool
	SortOrder int
	CreatedAt time.Time
	UpdatedAt time.Time
}

// KnowledgeNodePatch carries a partial knowledge-node update.
type KnowledgeNodePatch struct {
	Title    *string
	Text     *string
	ParentID *string
	Tags     *[]string
}

// KnowledgeSearchQuery is one knowledge-page search: the bank's text query, an
// optional query embedding for the vector arm, and a result bound.
//
// Embedding is produced by the caller (the deployment's embedding provider)
// because storage neither imports a model provider nor knows which one is
// configured. A nil or empty Embedding leaves the search on its text arm alone,
// which is what a deployment with no embedding model — or a caller like the MCP
// tool that has no model to reach — gets.
type KnowledgeSearchQuery struct {
	BankID    string
	Query     string
	Embedding []float32
	Limit     int
	// DisableTextArm drops the keyword arm, for a bank whose resolved config has
	// `enable_text_search` off: the vector arm then answers alone, and with no
	// query embedding either the search has no arm left and returns nothing
	// rather than a degraded ranking off an index the bank switched off.
	DisableTextArm bool
	// Tags/TagsMatch/TagGroups carry the caller's tag filter into the arms, so a
	// page filter is applied before ranking rather than after: the returned
	// `limit` hits are always the top-ranked matching pages, never a short set
	// left by pages that ranked above the window. An empty filter applies none.
	Tags      []string
	TagsMatch string
	TagGroups []TagGroup
}

// KnowledgeHit is one knowledge search result.
type KnowledgeHit struct {
	Node  KnowledgeNode
	Score float64
}

// MemoryQuery pages and filters a memory listing.
type MemoryQuery struct {
	BankID string
	Type   string
	// Q is a case-insensitive substring match on the unit text.
	Q string
	// DocumentID narrows to one document's units.
	DocumentID string
	// EntityID narrows to units mentioning one entity.
	EntityID string
	// Tags and TagsMatch narrow by tag.
	Tags      []string
	TagsMatch string
	// TagGroups, when set, replaces the flat tag filter with a compound tree,
	// which is how a caller's resolved read scope is AND-ed into the listing.
	TagGroups []TagGroup
	// State filters by curation state: "valid" (the live view, the default) or
	// "invalidated" (the archive).
	State string
	// ConsolidationState filters source facts by consolidation state: "failed",
	// "pending" or "done".
	ConsolidationState string
	// TimeField selects which timestamp the bounds apply to.
	TimeField string
	TimeStart *time.Time
	TimeEnd   *time.Time
	Limit     int
	Offset    int
}

// MemoryPage is one page of memories with the pre-paging total.
type MemoryPage struct {
	Memories []MemoryUnit
	Total    int
	Limit    int
	Offset   int
}

// MemoryPatch carries a partial memory-unit update. A nil field is left
// unchanged; a non-nil one is applied. State moves the unit between the live
// and invalidated views ("valid" reverts an invalidated unit); Reason is the
// free text recorded alongside an invalidation.
type MemoryPatch struct {
	Text     *string
	Tags     *[]string
	Metadata *map[string]any
	// Context, OccurredStart, OccurredEnd and FactType correct the extracted
	// fields; an empty string clears a date or context to NULL.
	Context       *string
	OccurredStart *string
	OccurredEnd   *string
	// ClearOccurredStart / ClearOccurredEnd clear the corresponding date to
	// NULL, which an explicit JSON null requests and which a nil pointer cannot
	// express on its own (nil also means "leave unchanged").
	ClearOccurredStart bool
	ClearOccurredEnd   bool
	FactType           *string
	// Entities replaces the unit's entity set; ResolveEntities selects whether
	// the names are resolved against the bank or taken literally.
	Entities        *[]string
	ResolveEntities *bool
	// State and Reason change the unit's curation state.
	State  *string
	Reason *string
}

// MemoryHistoryEntry is one recorded change to an observation: the snapshot a
// consolidation rewrite left in the reference's observation_history table.
type MemoryHistoryEntry struct {
	ChangedAt time.Time
	// PreviousText, PreviousTags and the previous timestamps describe the
	// observation before the change. The timestamps keep the snapshot's own
	// ISO-8601 spelling, because the reference emits them verbatim.
	PreviousText          *string
	PreviousTags          []string
	PreviousOccurredStart *string
	PreviousOccurredEnd   *string
	PreviousMentionedAt   *string
	// NewSourceMemoryIDs are the source memories this change folded in.
	NewSourceMemoryIDs []string
}

// ErrWebhookNotFound is returned when a webhook identifier is unknown.
var ErrWebhookNotFound = errors.New("webhook not found")

// ErrWebhookURLNotAllowed reports a URL the SSRF guard refuses.
var ErrWebhookURLNotAllowed = errors.New("webhook url is not allowed")

// Webhook is one registered delivery target.
type Webhook struct {
	ID         string
	BankID     string
	URL        string
	Secret     string
	EventTypes []string
	Enabled    bool
	HTTPConfig map[string]any
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// WebhookPatch carries a partial webhook update. A nil pointer means the field
// was not sent and is left unchanged; ClearSecret reports an explicit null
// secret, which the reference treats as a clear rather than as "unchanged".
type WebhookPatch struct {
	URL         *string
	Secret      *string
	ClearSecret bool
	EventTypes  *[]string
	Enabled     *bool
	HTTPConfig  *map[string]any
}

// WebhookDelivery is one recorded delivery attempt, read from the delivery
// operation that owns it.
type WebhookDelivery struct {
	ID        string
	WebhookID string
	BankID    string
	URL       string
	EventType string
	Status    string
	Attempts  int
	// NextRetryAt is when a retry is scheduled, nil when none is.
	NextRetryAt *time.Time
	// LastError is the recorded failure, empty when none.
	LastError string
	// LastResponseStatus is the receiver's last HTTP status, nil when the
	// receiver never answered.
	LastResponseStatus *int
	// LastResponseBody is the receiver's last response body. It is withheld
	// from API callers by default, so this is nil on the read path.
	LastResponseBody *string
	// LastAttemptAt is when the last attempt ran, nil when it never did.
	LastAttemptAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// WebhookDeliveryPage is one page of delivery records. HasMore reports that at
// least one more record follows, so the caller can emit a next cursor.
type WebhookDeliveryPage struct {
	Deliveries []WebhookDelivery
	HasMore    bool
}

// WebhookEventTypes returns the event names a webhook may subscribe to.
func WebhookEventTypes() []string {
	return []string{"consolidation.completed", "retain.completed", "memory_defense.triggered"}
}

// ErrAttachmentNotFound reports an attachment identifier the bank does not hold.
var ErrAttachmentNotFound = errors.New("attachment not found")
