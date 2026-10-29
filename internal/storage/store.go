package storage

import (
	"context"
	"errors"
	"time"
)

// ErrBankNotFound is returned when a bank identifier is unknown.
var ErrBankNotFound = errors.New("bank not found")

// ErrOperationConflict is returned when a caller-supplied operation identifier
// is reused for a different write.
var ErrOperationConflict = errors.New("operation id already used")

// ErrWritesUnavailable is returned when the store cannot accept writes for a
// transient reason; callers map it to a retryable service-unavailable response.
var ErrWritesUnavailable = errors.New("writes temporarily unavailable")

// ErrReadTimeout is returned when a read exceeds its configured budget.
var ErrReadTimeout = errors.New("read timed out")

// ErrReadShed is returned when the store sheds a read for a transient reason;
// callers map it to a retryable service-unavailable response.
var ErrReadShed = errors.New("store is shedding reads")

// ErrInvalidBankID is returned when a bank identifier (or alias) fails the
// admission rules: empty, over the byte budget, or containing control
// characters.
var ErrInvalidBankID = errors.New("invalid bank identifier")

// ErrBankExists is returned when a rename or create targets an identifier that
// already names a bank.
var ErrBankExists = errors.New("bank already exists")

// ErrAliasConflict is returned when an alias or bank identifier collides with
// an existing bank or alias.
var ErrAliasConflict = errors.New("alias conflicts with an existing bank or alias")

// ErrAliasNotFound is returned when an alias the bank does not own is targeted.
var ErrAliasNotFound = errors.New("alias not found")

// ErrOperationNotFound is returned when an operation identifier is unknown.
var ErrOperationNotFound = errors.New("operation not found")

// ErrOperationNotTerminal is returned when an operation is still running and
// therefore cannot be deleted. Only failed, cancelled or completed operations
// are removable.
var ErrOperationNotTerminal = errors.New("operation is not in a terminal state")

// ErrBatchParentNoWork is returned when a retry targets a batch_retain parent
// whose sub-batches can no longer be re-run — every child has completed, or it
// has none. The parent is a payload-less status aggregator, so there is nothing
// to re-queue, and reviving it alone would strand it pending with no child left
// to promote it. The reference reports this as its own 409 with a message that
// points the caller at resubmitting the source documents.
var ErrBatchParentNoWork = errors.New("batch parent has no incomplete sub-batches to retry")

// ErrOperationAborted is returned by long-running work when the operation it is
// running stops existing — its bank was deleted — or was cancelled. It is the
// signal that the work must end without settling the operation: there is no row
// left to settle, or the row is already terminal.
var ErrOperationAborted = errors.New("operation was cancelled or removed")

// ErrDocumentConflict is returned when an append-mode write observes that the
// stored document moved since the request read it; the request is retryable
// rather than overwriting content it never saw.
var ErrDocumentConflict = errors.New("document changed since it was read")

// ErrDocumentNotFound is returned when a document identifier is unknown to a
// bank.
var ErrDocumentNotFound = errors.New("document not found")

// ErrInvalidOperationID is returned when an operation identifier is not a valid
// identifier shape.
var ErrInvalidOperationID = errors.New("invalid operation identifier")

// ErrInvalidMemoryID reports a memory unit offered without an identifier, which
// no row can be keyed by.
var ErrInvalidMemoryID = errors.New("invalid memory identifier")

// ErrStoredFileNotFound is returned when a stored-file key is unknown. A
// caller must not be able to distinguish an absent key from a key it is not
// allowed to see, so this single sentinel covers both cases.
var ErrStoredFileNotFound = errors.New("stored file not found")

// Identifier limits for bank identifiers and aliases. BankIDMaxBytes is the
// reference's 192-byte UTF-8 budget, sized so a 64-character CJK identifier
// fits and a storage key percent-encoding it stays within its limits.
const (
	BankIDMaxBytes = 192
)

// ArchivedMemoryStore is the optional capability of a backend that keeps a
// curation archive: units a caller invalidated but can still revert, held in a
// table of their own.
//
// Its rows carry columns the live unit model does not — a unit's entity ids, its
// causal links, when and why it was invalidated — so a transfer carries the rows
// themselves rather than a projection that would quietly drop them.
type ArchivedMemoryStore interface {
	// ListArchivedMemories returns a bank's curation archive as rows, oldest
	// first so a restore keeps the order a reader pages through.
	ListArchivedMemories(ctx context.Context, bankID string) ([]map[string]any, error)
	// PutArchivedMemory inserts one archived row, reporting whether it did. A
	// row whose id is already archived is left alone, so a repeated restore is
	// idempotent and the caller can report what actually landed.
	PutArchivedMemory(ctx context.Context, row map[string]any) (bool, error)
}

// SchemaMigrator is the optional capability of a backend whose migration can be
// run without installing or reconciling the extensions it depends on. A backend
// without it always installs what the series declares.
type SchemaMigrator interface {
	MigrateWithoutExtensions(ctx context.Context) error
}

// SourceMemoryRow is one source fact of an observation, as the memory-detail
// read reports it: the raw live-table columns, so an unset text or context stays
// null rather than being flattened to an empty string.
type SourceMemoryRow struct {
	ID            string
	Text          *string
	FactType      string
	Context       *string
	OccurredStart *time.Time
	MentionedAt   *time.Time
}

// SourceMemoryReader is the optional capability of reading an observation's
// source facts straight from the live memory_units table. A source that exists
// only in the invalidation archive is therefore dropped, exactly as the
// reference's memory_units-only query drops it, and a NULL text or context is
// reported as null.
type SourceMemoryReader interface {
	SourceMemories(ctx context.Context, bankID string, ids []string) ([]SourceMemoryRow, error)
}

// Store is the single public Go API through which every other area reaches
// memory-domain storage. No area outside this package may address the
// memory-domain tables directly.
type Store interface {
	// Migrate applies the ordered, idempotent migration series.
	Migrate(ctx context.Context) error

	// Ready reports whether the database is reachable for readiness probing.
	Ready(ctx context.Context) error

	// ListBanks returns one page of banks, together with the pre-paging match count.
	ListBanks(ctx context.Context, query BankQuery) (BankPage, error)
	// CreateBank inserts a bank and returns the stored representation.
	CreateBank(ctx context.Context, bank Bank) (Bank, error)
	// EnsureBank lazily creates the bank row when it is absent, so a first
	// write to a bank a caller has not declared yet lands on the row every
	// bank-scoped write needs as its foreign-key target. It is idempotent and
	// safe under concurrent callers.
	EnsureBank(ctx context.Context, bankID string) error
	// GetBank returns one bank by identifier.
	GetBank(ctx context.Context, bankID string) (Bank, error)
	// ReplaceBank overwrites a bank's configuration.
	ReplaceBank(ctx context.Context, bank Bank) (Bank, error)
	// UpdateBank applies a partial bank update.
	UpdateBank(ctx context.Context, bankID string, patch BankPatch) (Bank, error)
	// DeleteBank removes a bank together with its content — memory units,
	// documents, entities and the rows that hang off them — returning the
	// number of units, entities and documents removed (0 for a bank that never
	// existed).
	DeleteBank(ctx context.Context, bankID string) (int, error)
	// RenameBank moves every bank-scoped row from source to target in one
	// operation, so a rename preserves the bank's data rather than recreating
	// an empty bank. It fails with ErrBankNotFound for an unknown source and
	// ErrBankExists when target already names a bank.
	RenameBank(ctx context.Context, source, target string) error
	// ClearBankMemories removes a bank's memory units together with their
	// links and entity associations, returning how many units were removed.
	// The bank row, its entities and its documents survive: this is the
	// clear-memories operation, not a bank delete. An empty factType clears
	// every type.
	ClearBankMemories(ctx context.Context, bankID, factType string) (int, error)

	// ResolveBankID maps an alias to its canonical bank identifier. An
	// identifier that is itself a bank is returned unchanged; unknown
	// identifiers and aliases are returned unchanged so callers can then
	// observe a not-found. A name that is both a bank and an alias always
	// resolves to the bank.
	ResolveBankID(ctx context.Context, name string) (string, error)
	// ListAliases returns a bank's aliases, displayed alias first then oldest.
	ListAliases(ctx context.Context, bankID string) ([]BankAlias, error)
	// AddAlias attaches an alias to a bank, optionally displaying the bank
	// under it, and returns the whole alias list.
	AddAlias(ctx context.Context, bankID, alias string, primary bool) ([]BankAlias, error)
	// SetAliasPrimary displays the bank under the named alias when primary is
	// true (demoting the incumbent in one step), or clears the alias's own
	// display flag when false, leaving the bank's other aliases untouched.
	SetAliasPrimary(ctx context.Context, bankID, alias string, primary bool) ([]BankAlias, error)
	// RemoveAlias detaches the named alias from the bank.
	RemoveAlias(ctx context.Context, bankID, alias string) ([]BankAlias, error)

	// RecordWrite persists a memory write, honouring operation-id idempotency.
	RecordWrite(ctx context.Context, req WriteRequest) (WriteResult, error)

	// RetainWrite persists an ordered-item memory write, writing documents,
	// chunks, memory units, entities, and links, and returns the created unit
	// identifiers grouped per item in item order plus token usage.
	RetainWrite(ctx context.Context, req RetainRequest) (RetainOutcome, error)
	// GetDocument returns one stored document by identifier.
	GetDocument(ctx context.Context, bankID, docID string) (Document, error)
	// PutDocument stores a document's text, hash, metadata and parameters.
	PutDocument(ctx context.Context, bankID, docID string, doc Document) (Document, error)
	// AppendDocument concatenates text onto the stored document, failing with
	// ErrDocumentConflict when the stored hash no longer matches expectedHash.
	AppendDocument(ctx context.Context, bankID, docID, text, expectedHash string) (Document, error)
	// RetagDocumentMemories carries a document's renamed tags onto its units and
	// the observation scopes frozen on them, then rebuilds the observations built
	// over them and returns how many were deleted. oldTags is the document's tag
	// set before the retag, newTags the one being written.
	//
	// A retag replaces only the DOCUMENT-derived tags on each unit. The tag
	// column also carries the projection of the bank's `entity_labels` groups
	// flagged tag:true, derived per memory from that memory's own entities at
	// extraction; labelKeys names those groups' keys, so each unit keeps the
	// label tags it already carries even as the document's tags are replaced.
	RetagDocumentMemories(ctx context.Context, bankID, docID string, oldTags, newTags []string, labelKeys map[string]bool) (int, error)
	// ResolveEntity resolves an entity name against the bank, honoring the
	// resolution switch, and returns the canonical stored identity.
	ResolveEntity(ctx context.Context, bankID, name string, kind EntityKind, resolve bool) (Entity, error)
	// UpsertUnit persists one memory unit and returns it with its identifier.
	UpsertUnit(ctx context.Context, unit MemoryUnit) (MemoryUnit, error)
	// UpsertUnitEntity links a memory unit to an entity identity.
	UpsertUnitEntity(ctx context.Context, unitID, entityID string) error
	// SetUnitEntities replaces the entities one memory unit mentions. Each name
	// is resolved against the bank — created when nothing matches, and matched
	// by similarity rather than case when resolve is on — and a name that is
	// already linked keeps its link, so a rewrite of the set is not a second
	// mention for the entities the caller left in place.
	SetUnitEntities(ctx context.Context, bankID, unitID string, names []string, resolve bool) error
	// UnitEntityNames returns the canonical names of the entities each unit
	// mentions, keyed by unit id. An observation that carries no postings of its
	// own inherits the entities of the memories it was consolidated from, so a
	// listing shows what a reader would get from the unit's detail. Units with no
	// entities are absent from the map rather than mapped to an empty slice.
	UnitEntityNames(ctx context.Context, bankID string, unitIDs []string) (map[string][]string, error)
	// UpsertEntityCooccurrence records that two entities co-occur in a bank.
	// The reference orders the pair by identifier, so the caller supplies the
	// lower identifier first.
	UpsertEntityCooccurrence(ctx context.Context, bankID, entityID1, entityID2 string) error
	// UpsertUnitCooccurrences records one co-occurrence for every unordered pair
	// of the entities one memory unit mentions, which is the shape the entity
	// graph reads and the resolution weighting counts. Pairing is per unit, so
	// two entities a unit does not both mention are not neighbours; the caller
	// passes the unit's entities already de-duplicated by identity, so a unit
	// that names one entity twice counts its pairs once.
	//
	// seenAt is when the unit was seen, not when the row is written: a pair's
	// last-co-occurrence is that time, so a corpus written in one session — an
	// import, for instance — keeps its own timeline instead of collapsing onto
	// the write moment. A zero time stamps the write time instead.
	UpsertUnitCooccurrences(ctx context.Context, bankID string, entityIDs []string, seenAt time.Time) error
	// UpsertLink persists one directed memory link.
	UpsertLink(ctx context.Context, link MemoryLink) error

	// Search returns recall matches for the supplied query.
	Search(ctx context.Context, req RecallRequest) (RecallResult, error)
	// Recall runs the multi-arm retrieval and ranking pipeline for a query,
	// returning a single ranked list plus any requested side channels.
	Recall(ctx context.Context, query RecallQuery) (RecallResult, error)
	// Synthesis fetches the retrieval context used to produce a reflection.
	Synthesis(ctx context.Context, req ReflectRequest) (ReflectContext, error)
	// Reflect runs the bounded retrieval-and-synthesis loop for a reflection
	// and returns the answer with its cited evidence, usage and trace. It
	// returns a sentinel error for a no-answer outcome, an unavailable model,
	// a tool-driving failure, a shed retrieval or an over-budget run.
	Reflect(ctx context.Context, query ReflectQuery) (ReflectOutcome, error)

	// IsStale reports whether a write inside a model's refresh scope has
	// landed since it last looked, or whether evidence the model cites has since
	// gone. The second case is the retraction check: a removed row raises no
	// watermark, so a model whose only change is a retraction would otherwise
	// never refresh. This is the question the scheduled refresh asks before
	// deciding to spend a model call. A model whose scope has not moved is up to
	// date even if its clock is old.
	IsStale(ctx context.Context, bankID string, model MentalModel) bool
	// IsStaleBatch answers the same scope question for a listing, without the
	// retraction check: a caller reading many models at once must not pay a
	// grounding lookup per model, and the refresh that a retraction calls for
	// is scheduled by the single-model path anyway.
	IsStaleBatch(ctx context.Context, bankID string, model MentalModel) bool
	// BankConfig returns a bank's configuration overrides, read fresh rather
	// than through the process-local bank cache. A configuration change made by
	// another server must take effect on the next request, so the paths that
	// depend on configuration ask for it directly; the cached row keeps serving
	// existence and profile reads.
	BankConfig(ctx context.Context, bankID string) (map[string]any, error)
	// BankWriteWatermark returns the time of the newest write to a bank, or
	// the zero time when it has never been written. A scoped staleness check
	// asks this first, so a quiet bank costs no scan.
	BankWriteWatermark(ctx context.Context, bankID string) time.Time
	// MarkMentalModelSeen stamps the bank write watermark a refresh observed
	// and clears any failure park. It is called only after the new content is
	// stored, which is what makes a failed refresh retryable.
	MarkMentalModelSeen(ctx context.Context, bankID, id string, seenAt time.Time) error
	// RecordMentalModelRefreshFailure parks an automatic refresh after a
	// failure without advancing the watermark, so the window that failed is
	// re-read by the next attempt.
	RecordMentalModelRefreshFailure(ctx context.Context, bankID, id string, failedAt time.Time) error

	// PutOperation records an asynchronous operation in its initial state.
	PutOperation(ctx context.Context, op Operation) (Operation, error)
	// RestoreOperation records an operation keeping the timestamps it carries,
	// for an import replaying history: the reference's importer inserts the
	// exported row column for column (api/http.py transfer import), so a
	// restored operation keeps when it was created, not when it was restored.
	// PutOperation is the wrong tool there because it stamps the current time.
	RestoreOperation(ctx context.Context, op Operation) (Operation, error)
	// EnqueueOperation records a background operation, refusing to duplicate
	// work already queued or running for the same subject. It is separate from
	// PutOperation because the two are idempotent on different things: an
	// operation a caller polls by id, versus the subject that operation works
	// on. A leaderless fleet needs the latter, since every process reaches the
	// same decision at the same instant.
	EnqueueOperation(ctx context.Context, req EnqueueRequest) (EnqueueResult, error)
	// GetOperation returns one operation by identifier, scoped to the bank; an
	// identifier belonging to another bank reads as ErrOperationNotFound.
	GetOperation(ctx context.Context, bankID, opID string) (Operation, error)
	// AdvanceOperation moves an operation forward in its status lifecycle.
	AdvanceOperation(ctx context.Context, opID string, next OperationStatus, errMsg string) (Operation, error)
	// ListOperations returns one page of operations matching the query.
	ListOperations(ctx context.Context, query OperationQuery) (OperationPage, error)
	// CancelOperation cancels a non-terminal operation, returning ErrOperationConflict
	// when the operation is already terminal.
	CancelOperation(ctx context.Context, bankID, opID string) (Operation, error)
	// RetryOperation starts a new run of a failed operation.
	RetryOperation(ctx context.Context, bankID, opID string) (Operation, error)
	// GetOperationChildren returns the child operations of a parent batch
	// operation, ordered by their sub-batch index.
	GetOperationChildren(ctx context.Context, bankID, opID string) ([]ChildOperation, error)
	// FinalizeBatchParent promotes a parent batch operation to a terminal
	// status once every child has reached one, aggregating the children's
	// outcome metadata onto the parent. It reports the promoted operation and
	// whether this call is the one that promoted it; a parent still waiting on
	// a child, or already terminal, is returned unchanged with promoted false.
	FinalizeBatchParent(ctx context.Context, bankID, parentID string) (Operation, bool, error)
	// RepairPendingBatchParent records the corrected terminal status of a
	// parent batch operation a read found out of sync: the row is still
	// "pending" though every child has finished. The write is guarded on the
	// stored status still being "pending", so a concurrent child promotion
	// wins and this becomes a no-op rather than re-terminalising the row. It
	// is separate from FinalizeBatchParent because the read-path repair
	// promotes only a pending row, never touches the outcome metadata, and
	// leaves a cancelled child out of the roll-up — the reference applies a
	// narrower rule on read than on the worker path.
	RepairPendingBatchParent(ctx context.Context, bankID, parentID string, status OperationStatus) error
	// SubmitAsyncRetain queues an asynchronous write: one parent batch
	// operation that aggregates the submission and carries no work a worker
	// runs, plus one child operation per token-bounded sub-batch, each holding
	// the items and the settings resolved when the request was accepted. A
	// caller-supplied operation id that already names this bank's batch joins
	// that submission instead of queueing a second copy of the work. The
	// synchronous path is untouched by it.
	SubmitAsyncRetain(ctx context.Context, req RetainRequest, tokensPerBatch int) (AsyncRetainSubmission, error)
	// DeleteOperation permanently removes a terminal operation row and the
	// archive an export operation owns. It reports ErrOperationNotFound when
	// the identifier is unknown and ErrOperationNotTerminal when the row is
	// still pending or running.
	DeleteOperation(ctx context.Context, bankID, opID string) (Operation, error)

	// ClaimNextOperation atomically claims the oldest claimable operation for
	// the given worker and returns it. It reports ErrOperationNotFound when
	// nothing is claimable. A claim older than lease is reclaimable, so a
	// consumer that died mid-operation does not strand its work.
	ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration) (Operation, error)
	// ClaimNextOperationForTypes claims the oldest claimable operation whose
	// type is in types, so a reserved type's floor can be honoured. An empty
	// types list is equivalent to ClaimNextOperation.
	ClaimNextOperationForTypes(ctx context.Context, workerID string, lease time.Duration, types []string) (Operation, error)
	// ReleaseOperationClaim returns a claimed operation to the claimable pool
	// without recording a failure. It is what a graceful shutdown uses to hand
	// its in-flight work back.
	ReleaseOperationClaim(ctx context.Context, opID, workerID string) error
	// ScheduleOperationRetry records a failed attempt and the moment the next
	// one becomes claimable, returning the operation to the claimable pool. It
	// is the retry half of the consumer lifecycle.
	ScheduleOperationRetry(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string) error

	// --- Webhooks -------------------------------------------------------
	// ListWebhooks returns a bank's webhook registrations, newest first.
	ListWebhooks(ctx context.Context, bankID string) ([]Webhook, error)
	// GetWebhook returns one webhook by identifier.
	GetWebhook(ctx context.Context, bankID, webhookID string) (Webhook, error)
	// PutWebhook inserts or replaces a webhook.
	PutWebhook(ctx context.Context, webhook Webhook) (Webhook, error)
	// UpdateWebhook applies a partial update.
	UpdateWebhook(ctx context.Context, bankID, webhookID string, patch WebhookPatch) (Webhook, error)
	// DeleteWebhook removes a webhook and its delivery records.
	DeleteWebhook(ctx context.Context, bankID, webhookID string) error
	// ListWebhookDeliveries returns one page of a webhook's delivery records,
	// newest first. cursor is the created_at of the last record of the previous
	// page; empty starts at the newest.
	ListWebhookDeliveries(ctx context.Context, bankID, webhookID string, limit int, cursor string) (WebhookDeliveryPage, error)

	// --- Knowledge base -------------------------------------------------
	// ListKnowledgeNodes returns every knowledge node of one bank.
	ListKnowledgeNodes(ctx context.Context, bankID string) ([]KnowledgeNode, error)
	// GetKnowledgeNode returns one knowledge node by identifier.
	GetKnowledgeNode(ctx context.Context, bankID, nodeID string) (KnowledgeNode, error)
	// PutKnowledgeNode inserts or replaces a knowledge node.
	PutKnowledgeNode(ctx context.Context, node KnowledgeNode) (KnowledgeNode, error)
	// UpdateKnowledgeNode applies a partial update.
	UpdateKnowledgeNode(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch) (KnowledgeNode, error)
	// UpdateKnowledgeNodeWithModel applies a node patch and a patch to the model
	// backing it in one transaction, so a patch that reaches both rows commits as
	// a unit: a rename cannot survive the option update that failed after it, and
	// no reader sees the pair disagree. modelPatch is nil for a patch that touches
	// no page option.
	UpdateKnowledgeNodeWithModel(ctx context.Context, bankID, nodeID string, patch KnowledgeNodePatch, modelPatch *MentalModelPatch) (KnowledgeNode, error)
	// DeleteKnowledgeNode removes a node and its descendants.
	DeleteKnowledgeNode(ctx context.Context, bankID, nodeID string) error
	// SearchKnowledgeNodes searches a bank's pages by a hybrid of the text
	// query and, when the caller supplies a query embedding, vector
	// similarity, fused by the store.
	SearchKnowledgeNodes(ctx context.Context, query KnowledgeSearchQuery) ([]KnowledgeHit, error)

	// --- Memory sub-routes ----------------------------------------------
	// ListMemories returns one page of a bank's memory units.
	ListMemories(ctx context.Context, query MemoryQuery) (MemoryPage, error)
	// GetMemory returns one memory unit by identifier.
	GetMemory(ctx context.Context, bankID, memoryID string) (MemoryUnit, error)
	// UpdateMemory applies a partial update to one memory unit.
	UpdateMemory(ctx context.Context, bankID, memoryID string, patch MemoryPatch) (MemoryUnit, error)
	// MemoryHistory returns the recorded changes of one memory unit.
	MemoryHistory(ctx context.Context, bankID, memoryID string) ([]MemoryHistoryEntry, error)

	// --- Observations and consolidation ---------------------------------
	// DeleteObservations removes the observations of a bank. When memoryID is
	// non-empty only the observations derived from that memory go.
	DeleteObservations(ctx context.Context, bankID, memoryID string) (int, error)
	// EnqueueConsolidation records a consolidation pass for a bank, reporting
	// whether an equivalent pending operation already existed. When one or more
	// tag scopes are supplied the pass is scoped to those scopes: a scoped
	// submit never deduplicates against a pending full-bank pass, matching the
	// reference.
	EnqueueConsolidation(ctx context.Context, bankID string, observationScopes ...[]string) (Operation, bool, error)
	// EnqueueGraphMaintenance records a graph-maintenance pass for a bank: the
	// sweep that relinks the units a curation left behind and prunes the
	// entities it orphaned. It deduplicates against a pass already pending or
	// running for the bank, because a sweep drains the queues to empty and so a
	// running one already covers work queued after it started. It reports
	// whether an equivalent operation already existed.
	EnqueueGraphMaintenance(ctx context.Context, bankID string) (Operation, bool, error)
	// RecoverConsolidations retries a bank's failed consolidations.
	RecoverConsolidations(ctx context.Context, bankID string) (int, error)

	// --- Observability --------------------------------------------------
	// BankStats summarises one bank's operational state.
	BankStats(ctx context.Context, bankID string) (BankStats, error)
	// MemoriesTimeseries buckets a bank's memory counts over a period.
	MemoriesTimeseries(ctx context.Context, bankID, period, timeField string) (Timeseries, error)
	// ListAuditLog returns one page of the audit trail.
	ListAuditLog(ctx context.Context, query AuditQuery) (AuditPage, error)
	// RecordAuditLog appends one audit entry. A write failure is the caller's
	// to ignore: recording an operation must never fail the operation it
	// describes.
	RecordAuditLog(ctx context.Context, entry AuditEntry) error
	// AuditLogStats buckets the audit trail over a period, narrowed to one
	// action when action is not empty.
	AuditLogStats(ctx context.Context, bankID, period, action string) (AuditStats, error)
	// ListLLMRequests returns one page of the model-request trace.
	ListLLMRequests(ctx context.Context, query LLMRequestQuery) (LLMRequestPage, error)
	// RecordLLMRequest appends one model-request trace row, so the trace the
	// read endpoints serve is populated by the calls that actually happened.
	RecordLLMRequest(ctx context.Context, request LLMRequest) error
	// LLMRequestStats buckets the model-request trace over a period, narrowed to
	// one operation when operation is not empty.
	LLMRequestStats(ctx context.Context, bankID, period, operation string) (LLMRequestStats, error)
	// ListObservationScopes returns the distinct observation scopes of a bank.
	ListObservationScopes(ctx context.Context, bankID string, limit, offset int) (ObservationScopePage, error)

	// --- Read surface ---------------------------------------------------
	// ListDocuments returns one page of a bank's documents plus the total.
	ListDocuments(ctx context.Context, query DocumentQuery) (DocumentPage, error)
	// DeleteDocument removes a document and the units extracted from it,
	// returning how many memory units were removed so a caller can report the
	// cascade size.
	DeleteDocument(ctx context.Context, bankID, docID string) (int, error)
	// DocumentFactCounts returns the document's memory units grouped by fact
	// type, so a document read can report its node breakdown.
	DocumentFactCounts(ctx context.Context, bankID, docID string) (map[string]int, error)
	// ListChunks returns one page of a bank's chunks plus the total.
	ListChunks(ctx context.Context, query ChunkQuery) (ChunkPage, error)
	// GetChunk returns one stored chunk by identifier.
	GetChunk(ctx context.Context, bankID, chunkID string) (Chunk, error)
	// FindChunk returns one stored chunk by identifier alone, wherever it is
	// stored. A chunk identifier is the chunks table's key and embeds the bank
	// and document it was derived from, so the owner is resolved from the
	// chunk rather than supplied by the caller — the addressed chunk route
	// carries no bank in its path. It returns ErrChunkNotFound when no bank
	// holds that identifier.
	FindChunk(ctx context.Context, chunkID string) (Chunk, error)
	// PutDocumentChunks upserts one document's chunks and reports the chunk
	// identifier each index resolves to, which is what a fact's chunk reference
	// and the addressed chunk route both address.
	//
	// The identities are derived from the (bank, document, index) triple, so a
	// replay of the same document overwrites its own rows rather than
	// duplicating them, and a row that belongs to another bank is refused
	// instead of being overwritten.
	//
	// storeText keeps the chunk text itself: a deployment that does not keep
	// source text stores an empty text and still records the content hash, so a
	// delta retain recognises a chunk whose text it does not hold.
	PutDocumentChunks(ctx context.Context, bankID, documentID string, chunks []DocumentChunk, storeText bool) (map[int]string, error)
	// ListTags returns the bank's tags with their usage counts.
	ListTags(ctx context.Context, bankID string) ([]TagCount, error)
	// ListEntities returns one page of a bank's entities plus the total.
	ListEntities(ctx context.Context, query EntityQuery) (EntityPage, error)
	// EntityMentions returns the memory texts that mention an entity, so the
	// entity-detail read can report its observations.
	EntityMentions(ctx context.Context, bankID, entityID string, limit int) ([]EntityMention, error)
	// UnitEntities returns the canonical names of the entities one memory unit
	// mentions, in name order.
	UnitEntities(ctx context.Context, bankID, unitID string) ([]string, error)
	// DrainGraphMaintenance claims up to limit queued graph-relink targets and
	// removes them from the queue, so a deleted unit's neighbours are relinked.
	DrainGraphMaintenance(ctx context.Context, limit int) ([]MaintenanceTarget, error)
	// RelinkUnit recomputes one unit's semantic links from its embedding.
	RelinkUnit(ctx context.Context, bankID, unitID string) error
	// DrainEntityMaintenance claims up to limit queued entity-prune targets and
	// removes them from the queue.
	DrainEntityMaintenance(ctx context.Context, limit int) ([]MaintenanceTarget, error)
	// PruneEntity removes an entity no memory unit mentions any more.
	PruneEntity(ctx context.Context, bankID, entityID string) error
	// Graph returns the bank's memory graph for the query's filtered unit set.
	Graph(ctx context.Context, query GraphQuery) (GraphData, error)
	// EntityGraph returns the bank's entity graph for the query.
	EntityGraph(ctx context.Context, query EntityGraphQuery) (EntityGraph, error)

	// --- Mental models -------------------------------------------------
	// ListMentalModels returns the mental models of one bank, optionally
	// narrowed to a subtype.
	ListMentalModels(ctx context.Context, bankID string, subtype MentalModelSubtype) ([]MentalModel, error)
	// GetMentalModel returns one mental model by identifier within a bank.
	GetMentalModel(ctx context.Context, bankID, id string) (MentalModel, error)
	// PutMentalModel inserts or replaces a mental model.
	PutMentalModel(ctx context.Context, model MentalModel) (MentalModel, error)
	// UpdateMentalModel applies a partial update and returns the stored value.
	UpdateMentalModel(ctx context.Context, bankID, id string, patch MentalModelPatch) (MentalModel, error)
	// DeleteMentalModel removes a mental model. It reports not-found when the
	// identifier is unknown to the bank.
	DeleteMentalModel(ctx context.Context, bankID, id string) error
	// ClearMentalModel empties a mental model's content and history while
	// keeping the record, which is what a clear request means.
	ClearMentalModel(ctx context.Context, bankID, id string) (MentalModel, error)
	// MentalModelHistory returns the recorded refreshes of one mental model.
	MentalModelHistory(ctx context.Context, bankID, id string) ([]MentalModelHistoryEntry, error)
	// RecordMentalModelHistory appends one refresh to a mental model's history.
	RecordMentalModelHistory(ctx context.Context, bankID, id string, entry MentalModelHistoryEntry) error

	// --- Directives ----------------------------------------------------
	// ListDirectives returns the directives of one bank, newest first.
	ListDirectives(ctx context.Context, bankID string) ([]Directive, error)
	// GetDirective returns one directive by identifier within a bank.
	GetDirective(ctx context.Context, bankID, id string) (Directive, error)
	// PutDirective inserts or replaces a directive.
	PutDirective(ctx context.Context, directive Directive) (Directive, error)
	// UpdateDirective applies a partial update and returns the stored value.
	UpdateDirective(ctx context.Context, bankID, id string, patch DirectivePatch) (Directive, error)
	// DeleteDirective removes a directive.
	DeleteDirective(ctx context.Context, bankID, id string) error

	// PutStoredFile persists an opaque byte payload under a caller-supplied
	// key. The transfer family stashes an uploaded archive under a
	// bank-scoped key and carries only that key in the queued operation, so
	// the payload and the operation queue are decoupled.
	PutStoredFile(ctx context.Context, file StoredFile) error
	// GetStoredFile returns one stored payload by key. It returns
	// ErrStoredFileNotFound when the key is unknown.
	GetStoredFile(ctx context.Context, key string) (StoredFile, error)
	// DeleteStoredFile removes the payload a key addresses. A key that names
	// no payload is a no-op rather than an error, so removing an export
	// operation's archive cannot fail on a blob that is already gone.
	DeleteStoredFile(ctx context.Context, key string) error

	// PutAttachment records one document's copy of an attachment. The write is
	// idempotent on (bank, document, content hash) so re-retaining an
	// unchanged document rewrites the same row instead of duplicating it.
	PutAttachment(ctx context.Context, attachment Attachment) error
	// ListAttachments returns the attachments a document carries, ordered by
	// creation, so provenance resolves deterministically.
	ListAttachments(ctx context.Context, bankID, documentID string) ([]Attachment, error)
	// GetAttachment returns one attachment by its short id, which is the handle
	// a placeholder token and an attachment URL carry. It reports
	// ErrAttachmentNotFound when the id is unknown to the bank.
	GetAttachment(ctx context.Context, bankID, attachmentID string) (Attachment, error)
	// ChunkAttachments returns each chunk's attachments, keyed by chunk id, so
	// a chunk read can show the attachments its units were drawn from.
	ChunkAttachments(ctx context.Context, bankID, documentID string) (map[string][]Attachment, error)

	// ConsolidationFacts returns the newest raw facts of a bank that have not
	// yet been folded into an observation.
	ConsolidationFacts(ctx context.Context, bankID string, limit int) ([]ConsolidationFact, error)
	// ConsolidationObservations returns a bank's existing observations, newest
	// first, as merge candidates for a consolidation pass.
	ConsolidationObservations(ctx context.Context, bankID string, limit int) ([]ConsolidationObservation, error)
	// CreateObservation records a new observation resting on the given facts.
	CreateObservation(ctx context.Context, bankID, text string, sourceIDs []string) (string, error)
	// UpdateObservation rewrites an observation and attaches more evidence.
	UpdateObservation(ctx context.Context, bankID, observationID, text string, sourceIDs []string) error
	// DeleteObservation removes one observation and its evidence links.
	DeleteObservation(ctx context.Context, bankID, observationID string) error
	// MarkConsolidated records that the given facts have been folded, so the
	// next pass does not reconsider them.
	MarkConsolidated(ctx context.Context, bankID string, factIDs []string) error
	// MarkConsolidationFailed records that a pass gave up on the given facts,
	// so later passes skip them until recovery requeues them. It is the
	// backlog gauge's source and the exclusion the candidates read applies.
	MarkConsolidationFailed(ctx context.Context, bankID string, factIDs []string) error
	// DeleteStaleObservations removes the observations that rest on any of the
	// given facts and requeues their surviving co-sources. It is called before
	// facts are deleted, so an observation never outlives its whole evidence
	// base and keeps answering as though it could be justified.
	DeleteStaleObservations(ctx context.Context, bankID string, factIDs []string) (int, error)
}

// Backend selects a PostgreSQL driver implementation. The only backend in
// scope for this unit is PostgreSQL; the interface keeps the choice explicit
// for the composition root.
type Backend interface {
	// Open returns a Store bound to the configured data source.
	Open(ctx context.Context, cfg BackendConfig) (Store, error)
}

// BackendConfig carries everything a backend needs to connect.
type BackendConfig struct {
	DatabaseURL         string
	Schema              string
	VectorExtension     string
	TextSearchExtension string
	RunMigrations       bool
	// RequireTenantSchema makes every statement resolve its schema from the
	// request context instead of the process schema. Multi-tenant mode sets it.
	RequireTenantSchema bool
	// MigrationDatabaseURL, when set, is the connection the migration series
	// runs over: a deployment whose serving role cannot run DDL points this at a
	// role that can, and the server itself keeps using DatabaseURL.
	MigrationDatabaseURL string
	// ReadDatabaseURL, when set, is a read-only connection — a replica or a
	// read-only role — that the store's read paths run over. Writes, migrations
	// and advisory-locked work always stay on DatabaseURL. Empty (the zero
	// value) means every statement runs on the primary, which is the reference's
	// own behaviour when no read database is configured.
	ReadDatabaseURL string
	// ReadPoolMin and ReadPoolMax bound the read pool, the way MaxIdleConns and
	// MaxOpenConns bound the primary one. Zero leaves the driver's default.
	ReadPoolMin int
	ReadPoolMax int
	// PGFunctionSchema and PGTokenizer configure the ParadeDB pg_search BM25
	// backend (the reference's WENYUAN_API_TEXT_SEARCH_EXTENSION_PG_SEARCH_*):
	// the schema its score/match functions live in and the tokenizer its index
	// is built with. They are read only when TextSearchExtension selects
	// pg_search.
	PGFunctionSchema string
	PGTokenizer      string
	// TextSearchNativeLanguage is the tsvector dictionary the native backend
	// tokenizes with (the reference's
	// WENYUAN_API_TEXT_SEARCH_EXTENSION_NATIVE_LANGUAGE). It is embedded in the
	// stored search_vector expression and in every keyword query, so it is a
	// deployment-level choice. Empty means the reference's default, "english".
	TextSearchNativeLanguage string
	// MaxOpenConns and MaxIdleConns bound the connection pool. Zero leaves the
	// driver's own default, which is unbounded for open connections.
	MaxOpenConns int
	MaxIdleConns int
	// BankInfoCacheTTL bounds the per-process bank profile cache and
	// BankStatsCacheTTL the cross-process bank-statistics summary. Zero (the
	// zero value) disables the corresponding cache, matching the reference's
	// "0 disables" convention.
	BankInfoCacheTTL  time.Duration
	BankStatsCacheTTL time.Duration
	// BankInfoCacheMaxEntries bounds how many bank rows one process caches.
	// Zero leaves it unbounded.
	BankInfoCacheMaxEntries int
	// BankStatsCacheMaxEntries bounds how many bank-statistics summaries one
	// process caches. Zero leaves it unbounded.
	BankStatsCacheMaxEntries int
	// Session holds the settings every connection of the pool carries. The
	// reference applies them per connection, so a deployment's statement
	// timeout and planner limits reach every statement rather than one.
	Session SessionSettings
	// AcquireTimeout bounds how long a statement waits for a pooled connection
	// before failing. Zero leaves the wait unbounded.
	AcquireTimeout time.Duration
	// PoolSlowAcquireThreshold and AcquireWarnThreshold are the waits at which a
	// connection acquire is reported as slow; each is disabled when zero.
	PoolSlowAcquireThreshold time.Duration
	AcquireWarnThreshold     time.Duration
}
