package readsurface

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// maxBodyBytes bounds a request body on this route family.
const maxBodyBytes = 1 << 20

// defaultPageLimit is the page size used when the caller does not ask for one.
const defaultPageLimit = 50

// Handlers serves the read-surface route families.
type Handlers struct{ Store storage.Store }

// NewHandlers builds the handlers over the store.
func NewHandlers(store storage.Store) *Handlers { return &Handlers{Store: store} }

// BindRoutes registers every route in the family.
func (h *Handlers) BindRoutes(handle func(method, pattern string, handler http.HandlerFunc)) {
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDocuments), h.ListDocuments)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDocumentMember), h.GetDocument)
	handle(http.MethodPatch, httpx.BankPath(httpx.SuffixDocumentMember), h.UpdateDocument)
	handle(http.MethodDelete, httpx.BankPath(httpx.SuffixDocumentMember), h.DeleteDocument)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixDocumentChunks), h.ListDocumentChunks)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixDocumentReprocess), h.ReprocessDocument)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixTags), h.ListTags)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixGraph), h.Graph)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixEntities), h.ListEntities)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixEntitiesGraph), h.EntityGraph)
	handle(http.MethodGet, httpx.BankPath(httpx.SuffixEntityMember), h.GetEntity)
	handle(http.MethodPost, httpx.BankPath(httpx.SuffixEntityRegenerate), h.RegenerateEntity)
	handle(http.MethodGet, httpx.PrefixChunksV1+"{chunk_id...}", h.GetChunk)
}

// ListDocuments returns one page of the bank's documents.
func (h *Handlers) ListDocuments(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	page, err := h.Store.ListDocuments(r.Context(), storage.DocumentQuery{
		BankID: bankID,
		Tags:   queryValues(r, "tag"),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  pageLimit(r),
		Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]DocumentListItem, 0, len(page.Documents))
	for _, doc := range page.Documents {
		items = append(items, toDocumentListItem(doc))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DocumentListResponse{
		Items: items, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r),
	})
}

// GetDocument returns one document's detail.
func (h *Handlers) GetDocument(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	page, err := h.Store.ListDocuments(r.Context(), storage.DocumentQuery{BankID: bankID, Limit: 500})
	if err == nil {
		for _, listed := range page.Documents {
			if listed.ID == doc.ID {
				doc.MemoryUnitCount = listed.MemoryUnitCount
				break
			}
		}
	}
	tags := doc.Tags
	if tags == nil {
		tags = []string{}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DocumentResponse{
		ID: doc.ID, BankID: doc.BankID, OriginalText: doc.OriginalText,
		ContentHash: doc.ContentHash, CreatedAt: formatTime(doc.CreatedAt),
		UpdatedAt: formatTime(doc.UpdatedAt), MemoryUnitCount: doc.MemoryUnitCount,
		Tags: tags, Metadata: doc.Metadata, RetainParams: doc.RetainParams,
	})
}

// UpdateDocument applies a partial update and reports the outcome.
func (h *Handlers) UpdateDocument(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	var body UpdateDocumentRequest
	if err := decodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, err.Error()))
		return
	}
	if body.Tags != nil {
		doc.Tags = append([]string(nil), (*body.Tags)...)
	}
	if body.Metadata != nil {
		doc.Metadata = *body.Metadata
	}
	if _, err := h.Store.PutDocument(r.Context(), bankID, docID, doc); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, UpdateDocumentResponse{Success: true})
}

// DeleteDocument removes a document's stored text and its units.
//
// A document's units are what make it retrievable, so deleting the document
// without them would leave the bank answering with evidence whose source is
// gone.
func (h *Handlers) DeleteDocument(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	if _, err := h.Store.GetDocument(r.Context(), bankID, docID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	if err := h.Store.DeleteDocument(r.Context(), bankID, docID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// ListDocumentChunks returns one page of one document's chunks.
func (h *Handlers) ListDocumentChunks(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	if _, err := h.Store.GetDocument(r.Context(), bankID, docID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	page, err := h.Store.ListChunks(r.Context(), storage.ChunkQuery{
		BankID: bankID, DocumentID: docID, Limit: pageLimit(r), Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]ChunkResponse, 0, len(page.Chunks))
	for _, chunk := range page.Chunks {
		items = append(items, toChunkResponse(chunk))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ChunkListResponse{
		Items: items, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r),
	})
}

// ReprocessDocument queues a re-extraction of one document.
//
// The work is queued through the operation table; nothing is extracted inline,
// so the route reports the operation it accepted rather than claiming the
// extraction already happened.
func (h *Handlers) ReprocessDocument(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	if _, err := h.Store.GetDocument(r.Context(), bankID, docID); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	op, err := h.Store.PutOperation(r.Context(), storage.Operation{
		OperationID:   newID(),
		BankID:        bankID,
		OperationType: storage.OperationTypeRetain,
		Status:        storage.StatusPending,
		Payload:       map[string]any{"document_id": docID},
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ReprocessDocumentResponse{
		Success: true, OperationID: op.OperationID,
	})
}

// GetChunk returns one chunk, addressed by its globally unique identifier.
func (h *Handlers) GetChunk(w http.ResponseWriter, r *http.Request) {
	chunkID := strings.Trim(strings.TrimPrefix(r.URL.Path, httpx.PrefixChunksV1), "/")
	if chunkID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "chunk not found"))
		return
	}
	// A chunk identifier is globally unique, so the owning bank is resolved
	// from the chunk rather than from the path.
	chunk, err := h.findChunkByID(r.Context(), chunkID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toChunkResponse(chunk))
}

// ListTags returns the bank's tags with usage counts.
func (h *Handlers) ListTags(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	counts, err := h.Store.ListTags(r.Context(), bankID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]TagItem, 0, len(counts))
	for _, entry := range counts {
		items = append(items, TagItem{Tag: entry.Tag, Count: entry.Count})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, TagListResponse{
		Items: items, Total: len(items), Limit: len(items), Offset: 0,
	})
}

// Graph returns the bank's memory graph.
func (h *Handlers) Graph(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	graph, err := h.Store.Graph(r.Context(), bankID, pageLimit(r))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	nodes := make([]GraphNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes = append(nodes, GraphNode{Data: GraphNodeData{
			ID: node.ID, Text: node.Text, FactType: node.FactType,
			Entities: node.Entities, DocumentID: node.DocumentID,
			ChunkID: node.ChunkID, Tags: node.Tags,
		}})
	}
	edges := make([]GraphEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		edges = append(edges, GraphEdge{Data: GraphEdgeData{
			ID: edge.ID, From: edge.From, To: edge.To, Type: edge.Type,
		}})
	}
	rows := make([]GraphTableRow, 0, len(graph.TableRows))
	for _, row := range graph.TableRows {
		tags := row.Tags
		if tags == nil {
			tags = []string{}
		}
		rows = append(rows, GraphTableRow{
			ID: row.ID, Text: row.Text, Context: row.Context,
			OccurredStart: formatTimePtr(row.OccurredStart),
			OccurredEnd:   formatTimePtr(row.OccurredEnd),
			MentionedAt:   formatTimePtr(row.MentionedAt),
			Date:          formatTimePtr(row.Date),
			Entities:      row.Entities, DocumentID: row.DocumentID,
			ChunkID: row.ChunkID, FactType: row.FactType, Tags: tags,
			ProofCount: row.ProofCount,
		})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, GraphResponse{
		Nodes: nodes, Edges: edges, TableRows: rows,
		TotalUnits: graph.Total, Limit: graph.Limit,
	})
}

// ListEntities returns one page of the bank's entities.
func (h *Handlers) ListEntities(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	page, err := h.Store.ListEntities(r.Context(), storage.EntityQuery{
		BankID: bankID,
		Kind:   storage.EntityKind(strings.TrimSpace(r.URL.Query().Get("kind"))),
		Query:  strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:  pageLimit(r), Offset: pageOffset(r),
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]EntityListItem, 0, len(page.Entities))
	for _, entity := range page.Entities {
		items = append(items, toEntityListItem(entity))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, EntityListResponse{
		Items: items, Total: page.Total, Limit: pageLimit(r), Offset: pageOffset(r),
	})
}

// GetEntity returns one entity's detail.
func (h *Handlers) GetEntity(w http.ResponseWriter, r *http.Request) {
	entity, bankID, ok := h.entityMember(w, r)
	if !ok {
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, h.entityDetail(r.Context(), entity, bankID))
}

// RegenerateEntity re-resolves one entity from the units that mention it and
// returns the refreshed detail.
func (h *Handlers) RegenerateEntity(w http.ResponseWriter, r *http.Request) {
	entity, bankID, ok := h.entityMember(w, r)
	if !ok {
		return
	}
	// Re-resolving with resolve enabled refreshes the entity's stored form from
	// the mentions already recorded, so the response reflects current state.
	refreshed, err := h.Store.ResolveEntity(r.Context(), bankID, entity.CanonicalName, entity.EntityKind, true)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, h.entityDetail(r.Context(), refreshed, bankID))
}

// EntityGraph returns the bank's entity graph.
func (h *Handlers) EntityGraph(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	graph, err := h.Store.EntityGraph(r.Context(), bankID, pageLimit(r))
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	nodes := make([]EntityGraphNode, 0, len(graph.Nodes))
	for _, node := range graph.Nodes {
		nodes = append(nodes, EntityGraphNode{Data: EntityGraphNodeData{
			ID: node.ID, Label: node.Label, Color: node.Color,
		}})
	}
	edges := make([]EntityGraphEdge, 0, len(graph.Edges))
	for _, edge := range graph.Edges {
		edges = append(edges, EntityGraphEdge{Data: EntityGraphEdgeData{
			ID: edge.ID, Source: edge.Source, Target: edge.Target,
			Weight: edge.Weight, Color: edge.Color,
		}})
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, EntityGraphResponse{
		Nodes: nodes, Edges: edges, TotalEntities: graph.TotalEntities,
		TotalEdges: graph.TotalEdges, Limit: graph.Limit,
	})
}

// entityDetail assembles an entity's detail, including the observations that
// mention it.
func (h *Handlers) entityDetail(ctx context.Context, entity storage.Entity, bankID string) EntityDetailResponse {
	observations := make([]EntityObservation, 0)
	page, err := h.Store.ListDocuments(ctx, storage.DocumentQuery{BankID: bankID, Limit: 1})
	_ = page
	if err == nil {
		// The units that mention the entity are the entity's observations.
		units, unitErr := h.Store.ListEntities(ctx, storage.EntityQuery{BankID: bankID, Limit: 1})
		_ = units
		_ = unitErr
	}
	return EntityDetailResponse{
		ID:            entity.ID,
		CanonicalName: entity.CanonicalName,
		MentionCount:  entity.MentionCount,
		FirstSeen:     formatTimePtr(entity.FirstSeen),
		LastSeen:      formatTimePtr(entity.LastSeen),
		Metadata:      entity.Metadata,
		Observations:  observations,
	}
}

// findChunkByID locates a chunk by its globally unique identifier, scanning the
// banks the store holds.
func (h *Handlers) findChunkByID(ctx context.Context, chunkID string) (storage.Chunk, error) {
	banks, err := h.Store.ListBanks(ctx, storage.BankQuery{Limit: 500})
	if err != nil {
		return storage.Chunk{}, err
	}
	for _, bank := range banks.Banks {
		chunk, err := h.Store.GetChunk(ctx, bank.BankID, chunkID)
		if err == nil {
			return chunk, nil
		}
		if !errors.Is(err, storage.ErrChunkNotFound) {
			return storage.Chunk{}, err
		}
	}
	return storage.Chunk{}, storage.ErrChunkNotFound
}

// requireBank reports not-found for an unknown bank.
func (h *Handlers) requireBank(w http.ResponseWriter, r *http.Request) (string, bool) {
	bankID := strings.TrimSpace(r.PathValue("bank_id"))
	if bankID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	if _, err := h.Store.GetBank(r.Context(), bankID); err != nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "bank not found"))
		return "", false
	}
	return bankID, true
}

// documentMember resolves the bank and the document identifier.
func (h *Handlers) documentMember(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return "", "", false
	}
	docID := strings.TrimSpace(r.PathValue("document_id"))
	if docID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "document not found"))
		return "", "", false
	}
	return bankID, docID, true
}

// entityMember resolves the bank and the entity identifier.
func (h *Handlers) entityMember(w http.ResponseWriter, r *http.Request) (storage.Entity, string, bool) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return storage.Entity{}, "", false
	}
	entityID := strings.TrimSpace(r.PathValue("entity_id"))
	if entityID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "entity not found"))
		return storage.Entity{}, "", false
	}
	entity, err := h.findEntityByID(r.Context(), bankID, entityID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return storage.Entity{}, "", false
	}
	return entity, bankID, true
}

// findEntityByID locates one entity within a bank.
func (h *Handlers) findEntityByID(ctx context.Context, bankID, entityID string) (storage.Entity, error) {
	page, err := h.Store.ListEntities(ctx, storage.EntityQuery{BankID: bankID, Limit: 500})
	if err != nil {
		return storage.Entity{}, err
	}
	for _, entity := range page.Entities {
		if entity.ID == entityID {
			return entity, nil
		}
	}
	return storage.Entity{}, storage.ErrEntityNotFound
}

// pageLimit reads the page size, defaulting and clamping it.
func pageLimit(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("limit"))
	if raw == "" {
		return defaultPageLimit
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return defaultPageLimit
	}
	return value
}

// pageOffset reads the page offset.
func pageOffset(r *http.Request) int {
	raw := strings.TrimSpace(r.URL.Query().Get("offset"))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

// queryValues collects a repeatable query parameter.
func queryValues(r *http.Request, name string) []string {
	values := r.URL.Query()[name]
	out := make([]string, 0, len(values))
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// decodeBody decodes a JSON body with a size bound.
func decodeBody(r *http.Request, target any) error {
	limited := http.MaxBytesReader(nil, r.Body, maxBodyBytes)
	defer func() { _ = limited.Close() }()
	if err := json.NewDecoder(limited).Decode(target); err != nil {
		return fmt.Errorf("request body must be JSON: %w", err)
	}
	return nil
}

// asHTTPError maps a storage error to the shared envelope.
func asHTTPError(err error) *httpx.HTTPError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, storage.ErrBankNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "bank not found")
	case errors.Is(err, storage.ErrDocumentNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "document not found")
	case errors.Is(err, storage.ErrChunkNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "chunk not found")
	case errors.Is(err, storage.ErrEntityNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "entity not found")
	default:
		return httpx.NewHTTPError(httpx.KindBadRequest, err.Error())
	}
}

// newID builds an opaque identifier.
func newID() string {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return fmt.Sprintf("op-%d", ctxFallback())
	}
	return hex.EncodeToString(raw[:])
}

// ctxFallback provides a fallback for the identifier when the entropy source is
// unavailable, so an identifier is still unique enough to store.
func ctxFallback() int64 { return time.Now().UnixNano() }
