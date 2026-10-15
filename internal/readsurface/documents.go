package readsurface

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
)

// documentVisible reports whether a document's tags satisfy the caller's
// resolved read scope. A document that fails is answered as not-found, so a
// scoped reader cannot tell an out-of-scope document from an absent one.
func documentVisible(r *http.Request, doc storage.Document) bool {
	return storage.TagsVisibleUnderScope(doc.Tags, memoryops.ScopeFromRequest(r))
}

// ListDocuments returns one page of the bank's documents.
func (h *Handlers) ListDocuments(w http.ResponseWriter, r *http.Request) {
	bankID, ok := h.requireBank(w, r)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", defaultPageLimit, 0, -1)
	if !ok {
		return
	}
	offset, ok := intParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
	tags := queryValues(r, "tags")
	tagsMatch := strings.TrimSpace(httpx.Query(r, "tags_match"))
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAnyStrict
	}
	start, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	end, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}
	// The caller's resolved read scope narrows the listing, so a scoped reader
	// pages over the documents it may see (and the total agrees).
	scopedGroups, tags, tagsMatch := scopedTagFilter(r, nil, tags, tagsMatch)
	page, err := h.Store.ListDocuments(r.Context(), storage.DocumentQuery{
		BankID:    bankID,
		Tags:      tags,
		TagsMatch: tagsMatch,
		TagGroups: scopedGroups,
		Query:     strings.TrimSpace(httpx.Query(r, "q")),
		TimeField: strings.TrimSpace(httpx.Query(r, "time_field")),
		TimeStart: start,
		TimeEnd:   end,
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]DocumentListItem, 0, len(page.Documents))
	for _, doc := range page.Documents {
		items = append(items, toDocumentListItem(doc))
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, DocumentListResponse{
		Items: items, Total: page.Total, Limit: limit, Offset: offset,
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
	// A scoped reader may only open a document whose tags its scope admits; one
	// that does not reads as not-found rather than leaking that it exists.
	if !documentVisible(r, doc) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Document "+docID+" not found"))
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
	// The node breakdown and the attachment list are the detail extras the
	// reference exposes on a single-document read.
	factCounts, err := h.Store.DocumentFactCounts(r.Context(), bankID, doc.ID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	attachments, err := h.Store.ListAttachments(r.Context(), bankID, doc.ID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	var scopes any
	if doc.RetainParams != nil {
		scopes = doc.RetainParams["observation_scopes"]
	}
	httpx.WriteJSONOpenRow(w, r, http.StatusOK, DocumentResponse{
		ID: doc.ID, BankID: doc.BankID, OriginalText: optionalString(doc.OriginalText),
		ContentHash: optionalString(doc.ContentHash), CreatedAt: formatTime(doc.CreatedAt),
		UpdatedAt: formatTime(doc.UpdatedAt), MemoryUnitCount: doc.MemoryUnitCount,
		Tags: tags, Metadata: doc.Metadata, RetainParams: doc.RetainParams,
		NodesByFactType: factTypeCounts(factCounts), ObservationScopes: scopes,
		Attachments: toChunkAttachments(bankID, attachments),
	})
}

// factTypeCounts renders a document's per-type counts in the fixed breakdown
// the detail read reports. All three perspectives are always present, zero when
// the document has none, so a caller can read the key unconditionally.
func factTypeCounts(counts map[string]int) map[string]int {
	return map[string]int{
		"world":       counts["world"],
		"experience":  counts["experience"],
		"observation": counts["observation"],
	}
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
	if err := httpx.DecodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	oldTags := append([]string(nil), doc.Tags...)
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
	// A tag rename on the document must carry into its units and the observation
	// scopes frozen on them, and the observations built over them must be rebuilt
	// under the new tag — otherwise recall by the new tag no longer reaches them.
	// A tag set equal to the current one is a no-op (a bulk normalisation sweep
	// re-sends an identical array), so the cascade is skipped for it.
	if body.Tags != nil && !sameTagSet(oldTags, doc.Tags) {
		deleted, err := h.Store.RetagDocumentMemories(r.Context(), bankID, docID, oldTags, doc.Tags)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		if deleted > 0 {
			if _, _, err := h.Store.EnqueueConsolidation(r.Context(), bankID); err != nil {
				httpx.WriteErrorFor(w, r, asHTTPError(err))
				return
			}
		}
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, UpdateDocumentResponse{Success: true})
}

// sameTagSet reports whether two tag lists name the same set. Consolidation
// scopes a memory by its tag set, never by the order the array arrived in, so a
// reordered array changes nothing the retag cascade could observe.
func sameTagSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, tag := range a {
		seen[tag]++
	}
	for _, tag := range b {
		if seen[tag] == 0 {
			return false
		}
		seen[tag]--
	}
	return true
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
	unitsDeleted, err := h.Store.DeleteDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, DeleteDocumentResponse{
		Success:            true,
		Message:            fmt.Sprintf("Document '%s' and %d associated memory units deleted successfully", docID, unitsDeleted),
		DocumentID:         docID,
		MemoryUnitsDeleted: unitsDeleted,
	})
}

// ListDocumentChunks returns one page of one document's chunks.
func (h *Handlers) ListDocumentChunks(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// The document's chunks are its own source text, so a scoped reader that may
	// not open the document may not page its chunks either.
	if !documentVisible(r, doc) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Document "+docID+" not found"))
		return
	}
	limit, ok := intParam(w, r, "limit", defaultPageLimit, 1, 1000)
	if !ok {
		return
	}
	offset, ok := intParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
	page, err := h.Store.ListChunks(r.Context(), storage.ChunkQuery{
		BankID: bankID, DocumentID: docID, Limit: limit, Offset: offset,
	})
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	byChunk, err := h.Store.ChunkAttachments(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	items := make([]ChunkResponse, 0, len(page.Chunks))
	for _, chunk := range page.Chunks {
		items = append(items, toChunkResponse(chunk, byChunk[chunk.ChunkID]))
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ChunkListResponse{
		Items: items, Total: page.Total, Limit: limit, Offset: offset,
	})
}

// ReprocessDocument re-extracts one document from its stored text.
//
// The pipeline runs through the same configuration path the write route uses,
// so a re-extraction is shaped by the bank's current settings and the document's
// own replayable parameters. The response hands back the operation the write
// recorded, which is the handle a caller follows to completion.
func (h *Handlers) ReprocessDocument(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	if h.Reprocess == nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal))
		return
	}
	outcome, err := h.Reprocess.ReprocessDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, ReprocessDocumentResponse{
		Success: true, OperationID: outcome.OperationID, ItemsCount: 1,
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
	// A chunk is its document's source text, so the caller's resolved read
	// scope must admit the document; otherwise the chunk reads as not-found.
	doc, err := h.Store.GetDocument(r.Context(), chunk.BankID, chunk.DocumentID)
	if err != nil || !documentVisible(r, doc) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "chunk not found"))
		return
	}
	// A chunk addressed by its own id resolves its bank and document from the
	// chunk, so its attachments are looked up under that document.
	byChunk, err := h.Store.ChunkAttachments(r.Context(), chunk.BankID, chunk.DocumentID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSONFor(w, r, http.StatusOK, toChunkResponse(chunk, byChunk[chunk.ChunkID]))
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
