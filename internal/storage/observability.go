package storage

import (
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
	BankID string
	Status string
	Limit  int
	Offset int
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

// BankStats is the per-bank operational summary.
type BankStats struct {
	BankID               string
	TotalNodes           int
	TotalLinks           int
	TotalDocuments       int
	NodesByFactType      map[string]int
	LinksByLinkType      map[string]int
	PendingOperations    int
	FailedOperations     int
	OperationsByStatus   map[string]int
	LastConsolidatedAt   *time.Time
	LastMemoryWriteAt    *time.Time
	PendingConsolidation int
	FailedConsolidation  int
	TotalObservations    int
}

// TimeseriesBucket is one bucket of the memory-count histogram.
type TimeseriesBucket struct {
	Time  time.Time
	Count int
}

// Timeseries is the memory-count histogram over a period.
type Timeseries struct {
	BankID    string
	Period    string
	Trunc     string
	TimeField string
	Buckets   []TimeseriesBucket
}

// ObservationScope is one distinct observation scope recorded for a bank.
type ObservationScope struct {
	Scope string
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

// KnowledgeNode is one node of a bank's knowledge tree.
type KnowledgeNode struct {
	ID        string
	BankID    string
	ParentID  string
	Kind      KnowledgeKind
	Title     string
	Text      string
	Tags      []string
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

// KnowledgeHit is one knowledge search result.
type KnowledgeHit struct {
	Node  KnowledgeNode
	Score float64
}

// MemoryQuery pages and filters a memory listing.
type MemoryQuery struct {
	BankID string
	Type   string
	Limit  int
	Offset int
}

// MemoryPage is one page of memories with the pre-paging total.
type MemoryPage struct {
	Memories []MemoryUnit
	Total    int
	Limit    int
	Offset   int
}

// MemoryPatch carries a partial memory-unit update.
type MemoryPatch struct {
	Text     *string
	Tags     *[]string
	Metadata *map[string]any
}

// MemoryHistoryEntry is one recorded change to a memory unit.
type MemoryHistoryEntry struct {
	ChangedAt time.Time
	Reason    string
	Text      string
	Tags      []string
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

// WebhookPatch carries a partial webhook update.
type WebhookPatch struct {
	URL        *string
	Secret     *string
	EventTypes *[]string
	Enabled    *bool
}

// WebhookDelivery is one recorded delivery attempt.
type WebhookDelivery struct {
	ID          string
	WebhookID   string
	BankID      string
	EventType   string
	Status      string
	StatusCode  *int
	Error       string
	Attempt     int
	DeliveredAt time.Time
}

// WebhookDeliveryPage is one page of delivery records.
type WebhookDeliveryPage struct {
	Deliveries []WebhookDelivery
	Total      int
	Limit      int
	Offset     int
}

// WebhookEventTypes returns the event names a webhook may subscribe to.
func WebhookEventTypes() []string {
	return []string{"memory.created", "memory.updated", "memory.deleted", "bank.created", "consolidation.completed"}
}
