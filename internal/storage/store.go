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

// ErrAliasConflict is returned when an alias or bank identifier collides with
// an existing bank or alias.
var ErrAliasConflict = errors.New("alias conflicts with an existing bank or alias")

// ErrAliasNotFound is returned when an alias the bank does not own is targeted.
var ErrAliasNotFound = errors.New("alias not found")

// ErrOperationNotFound is returned when an operation identifier is unknown.
var ErrOperationNotFound = errors.New("operation not found")

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

// ErrStoredFileNotFound is returned when a stored-file key is unknown. A
// caller must not be able to distinguish an absent key from a key it is not
// allowed to see, so this single sentinel covers both cases.
var ErrStoredFileNotFound = errors.New("stored file not found")

// Identifier limits for bank identifiers and aliases. BankIDMaxBytes is a
// fixed UTF-8 byte budget chosen so storage keys remain within their limits.
const (
	BankIDMaxBytes = 255
)

// Store is the single public Go API through which every other area reaches
// memory-domain storage. No area outside this package may address the
// memory-domain tables directly.
type Store interface {
	// Migrate applies the ordered, idempotent migration series.
	Migrate(ctx context.Context) error
	// Ready reports whether the database is reachable for readiness probing.
	Ready(ctx context.Context) error

	// ListBanks returns one page of banks in most-recently-written-first order
	// together with the pre-paging match count.
	ListBanks(ctx context.Context, query BankQuery) (BankPage, error)
	// CreateBank inserts a bank and returns the stored representation.
	CreateBank(ctx context.Context, bank Bank) (Bank, error)
	// GetBank returns one bank by identifier.
	GetBank(ctx context.Context, bankID string) (Bank, error)
	// ReplaceBank overwrites a bank's configuration.
	ReplaceBank(ctx context.Context, bank Bank) (Bank, error)
	// UpdateBank applies a partial bank update.
	UpdateBank(ctx context.Context, bankID string, patch BankPatch) (Bank, error)
	// DeleteBank removes a bank and its owned rows, returning how many bank
	// rows were removed (0 for a bank that never existed).
	DeleteBank(ctx context.Context, bankID string) (int, error)

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
	// PromoteAlias displays the bank under the named alias, demoting any other.
	PromoteAlias(ctx context.Context, bankID, alias string) ([]BankAlias, error)
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
	// ResolveEntity resolves an entity name against the bank, honoring the
	// resolution switch, and returns the canonical stored identity.
	ResolveEntity(ctx context.Context, bankID, name string, kind EntityKind, resolve bool) (Entity, error)
	// UpsertUnit persists one memory unit and returns it with its identifier.
	UpsertUnit(ctx context.Context, unit MemoryUnit) (MemoryUnit, error)
	// UpsertUnitEntity links a memory unit to an entity identity.
	UpsertUnitEntity(ctx context.Context, unitID, entityID string) error
	// UpsertEntityCooccurrence records that two entities co-occur in a bank.
	UpsertEntityCooccurrence(ctx context.Context, bankID, entityA, entityB string) error
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

	// PutOperation records an asynchronous operation in its initial state.
	PutOperation(ctx context.Context, op Operation) (Operation, error)
	// GetOperation returns one operation by identifier.
	GetOperation(ctx context.Context, opID string) (Operation, error)
	// AdvanceOperation moves an operation forward in its status lifecycle.
	AdvanceOperation(ctx context.Context, opID string, next OperationStatus, errMsg string) (Operation, error)
	// ListOperations returns one page of operations matching the query.
	ListOperations(ctx context.Context, query OperationQuery) (OperationPage, error)
	// CancelOperation cancels a non-terminal operation, returning ErrOperationConflict
	// when the operation is already terminal.
	CancelOperation(ctx context.Context, opID string) (Operation, error)
	// RetryOperation starts a new run of a failed operation.
	RetryOperation(ctx context.Context, opID string) (Operation, error)

	// ClaimNextOperation atomically claims the oldest claimable operation for
	// the given worker and returns it. It reports ErrOperationNotFound when
	// nothing is claimable. A claim older than lease is reclaimable, so a
	// consumer that died mid-operation does not strand its work.
	ClaimNextOperation(ctx context.Context, workerID string, lease time.Duration) (Operation, error)
	// ReleaseOperationClaim returns a claimed operation to the claimable pool
	// without recording a failure. It is what a graceful shutdown uses to hand
	// its in-flight work back.
	ReleaseOperationClaim(ctx context.Context, opID, workerID string) error
	// ScheduleOperationRetry records a failed attempt and the moment the next
	// one becomes claimable, returning the operation to the claimable pool. It
	// is the retry half of the consumer lifecycle.
	ScheduleOperationRetry(ctx context.Context, opID string, retryCount int, nextRetryAt time.Time, errMsg string) error

	// --- Read surface ---------------------------------------------------
	// ListDocuments returns one page of a bank's documents plus the total.
	ListDocuments(ctx context.Context, query DocumentQuery) (DocumentPage, error)
	// DeleteDocument removes a document and the units extracted from it, so a
	// retrieval never returns evidence whose source no longer exists.
	DeleteDocument(ctx context.Context, bankID, docID string) error
	// ListChunks returns one page of a bank's chunks plus the total.
	ListChunks(ctx context.Context, query ChunkQuery) (ChunkPage, error)
	// GetChunk returns one stored chunk by identifier.
	GetChunk(ctx context.Context, bankID, chunkID string) (Chunk, error)
	// ListTags returns the bank's tags with their usage counts.
	ListTags(ctx context.Context, bankID string) ([]TagCount, error)
	// ListEntities returns one page of a bank's entities plus the total.
	ListEntities(ctx context.Context, query EntityQuery) (EntityPage, error)
	// Graph returns the bank's memory graph.
	Graph(ctx context.Context, bankID string, limit int) (GraphData, error)
	// EntityGraph returns the bank's entity graph.
	EntityGraph(ctx context.Context, bankID string, limit int) (EntityGraph, error)

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
}
