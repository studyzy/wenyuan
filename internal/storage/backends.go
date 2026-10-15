package storage

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// Backend names selectable from configuration.
const (
	BackendMemory     = "memory"
	BackendPostgreSQL = "postgres"
)

// ErrUnknownBackend is returned when configuration names an unsupported backend.
var ErrUnknownBackend = errors.New("unknown storage backend")

// SelectBackend resolves the configured backend name to a Backend. The only
// production backend is PostgreSQL; the in-process backend exists so the
// service and its tests can run without a live database.
func SelectBackend(name string) (Backend, error) {
	switch name {
	case "", BackendPostgreSQL, "postgresql", "pgx":
		return &PostgresBackend{}, nil
	case BackendMemory, "in-memory", "inprocess":
		return &MemoryBackend{}, nil
	default:
		return nil, fmt.Errorf("%w: %q", ErrUnknownBackend, name)
	}
}

// MemoryBackend returns an in-process Store. It implements the same API as the
// PostgreSQL backend so higher layers stay backend-agnostic.
type MemoryBackend struct{}

// Open returns a fresh in-process store.
func (b *MemoryBackend) Open(ctx context.Context, cfg BackendConfig) (Store, error) {
	store := NewMemoryStore(cfg.Schema)
	store.setBankCacheTTL(cfg.BankInfoCacheTTL)
	return store, nil
}

// setBankCacheTTL rebinds the per-process bank cache to the configured
// lifetime. A zero lifetime disables the cache, so every lookup reads the map.
func (s *MemoryStore) setBankCacheTTL(ttl time.Duration) {
	s.cache = newBankCache(ttl)
}

// MemoryStore is a mutex-guarded in-process implementation of Store. It is the
// only store the offline test suite exercises.
type MemoryStore struct {
	mu sync.RWMutex
	// diagMu guards the diagnostic buffers below (the audit trail and the
	// model-request trace). They are deliberately not under mu: a store read
	// holds mu across its work, and a diagnostic write happens *inside* that
	// work — a provider call the read made traces itself — so sharing the lock
	// would deadlock the reader against its own trace write.
	diagMu        sync.Mutex
	schema        string
	migrated      bool
	banks         map[string]Bank
	order         []string
	aliasesByBank map[string][]BankAlias
	aliasOwners   map[string]string
	cache         *bankCache
	operations    map[string]Operation
	writes        map[string]WriteResult
	memories      map[string][]RecallMatch
	mentalModels  map[string][]ReflectMentalModel
	// retentionRows is the in-memory stand-in for the retention tables, so the
	// sweep jobs are exercised on the default offline path.
	retentionRows map[string][]retentionRow
	directives    map[string][]ReflectDirective
	documents     map[string]Document
	chunks        []Chunk
	units         map[string]MemoryUnit
	// invalidated holds units moved out of the live view by a curation
	// invalidation, so recall/consolidation need no state predicate.
	invalidated   map[string]MemoryUnit
	entities      map[string]Entity
	unitEntities  map[string][]string
	links         []MemoryLink
	cooccurrences map[string]int
	storedFiles   map[string]StoredFile
	// attachments holds one document's copy of an inline retain attachment,
	// keyed by bank, document and content hash.
	attachments map[string]Attachment
	// consolidated records the facts a consolidation pass has already folded,
	// so the next pass does not reconsider them.
	consolidated map[string]struct{}
	// mentalModelRecords holds mental models keyed by bank and identifier,
	// because an identifier is unique within its bank rather than globally.
	mentalModelRecords map[string]MentalModel
	mentalModelHistory map[string][]MentalModelHistoryEntry
	directiveRecords   map[string]Directive
	auditEntries       []AuditEntry
	llmRequests        []LLMRequest
	knowledgeNodes     map[string]KnowledgeNode
	memoryHistory      map[string][]MemoryHistoryEntry
	observationSources map[string][]string
	webhooks           map[string]Webhook
	writeErr           error
	readErr            error
	readyErr           error
	// observationEmbedder, when set, embeds the text of an observation the
	// consolidation pass writes, so the vector recall arm can rank it.
	observationEmbedder EmbeddingGenerator
}

// SetObservationEmbedder gives the store the generator the consolidation write
// path embeds observations with. A nil generator leaves observations
// unembedded, as a deployment with no embedding model does.
func (s *MemoryStore) SetObservationEmbedder(generator EmbeddingGenerator) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.observationEmbedder = generator
}

// observationEmbedderRef reads the configured observation embedder under the
// read lock, so it is safe against the startup setter.
func (s *MemoryStore) observationEmbedderRef() EmbeddingGenerator {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.observationEmbedder
}

// NewMemoryStore builds an empty in-process store bound to a schema name.
func NewMemoryStore(schema string) *MemoryStore {
	return &MemoryStore{
		schema:             schema,
		banks:              map[string]Bank{},
		aliasesByBank:      map[string][]BankAlias{},
		aliasOwners:        map[string]string{},
		cache:              newBankCache(BankCacheTTL),
		operations:         map[string]Operation{},
		writes:             map[string]WriteResult{},
		memories:           map[string][]RecallMatch{},
		mentalModels:       map[string][]ReflectMentalModel{},
		directives:         map[string][]ReflectDirective{},
		documents:          map[string]Document{},
		units:              map[string]MemoryUnit{},
		invalidated:        map[string]MemoryUnit{},
		entities:           map[string]Entity{},
		unitEntities:       map[string][]string{},
		cooccurrences:      map[string]int{},
		storedFiles:        map[string]StoredFile{},
		attachments:        map[string]Attachment{},
		consolidated:       map[string]struct{}{},
		mentalModelRecords: map[string]MentalModel{},
		mentalModelHistory: map[string][]MentalModelHistoryEntry{},
		directiveRecords:   map[string]Directive{},
		knowledgeNodes:     map[string]KnowledgeNode{},
		memoryHistory:      map[string][]MemoryHistoryEntry{},
		observationSources: map[string][]string{},
		webhooks:           map[string]Webhook{},
	}
}

// Cache exposes the per-process bank cache for read-through behaviour and
// tests. It is never consulted for authorisation or routing.
func (s *MemoryStore) Cache() *bankCache { return s.cache }

// Schema returns the configured schema target.
func (s *MemoryStore) Schema() string { return s.schema }

// SetWriteError injects a write-unavailable condition for tests.
func (s *MemoryStore) SetWriteError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.writeErr = err
}

// SetReadError injects a read-timeout condition for tests.
func (s *MemoryStore) SetReadError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readErr = err
}

// SetReadyError injects a readiness failure for tests.
func (s *MemoryStore) SetReadyError(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.readyErr = err
}

// SeedMemory injects a recall match for tests.
func (s *MemoryStore) SeedMemory(bankID string, match RecallMatch) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.memories[bankID] = append(s.memories[bankID], match)
}

// AgeOperation backdates one stored operation, so a retention window can be
// exercised without waiting for real time to pass. PutOperation stamps the
// current time on every write, so this is the only way to age a row.
func (s *MemoryStore) AgeOperation(id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	op, ok := s.operations[id]
	if !ok {
		return ErrOperationNotFound
	}
	op.CreatedAt = at
	op.UpdatedAt = at
	s.operations[id] = op
	return nil
}

// SeedUnit records a stored memory unit directly, for tests that need the
// authoritative unit store rather than the derived recall projection.
func (s *MemoryStore) SeedUnit(unit MemoryUnit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.upsertUnitLocked(unit)
}
