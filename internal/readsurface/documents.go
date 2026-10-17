package readsurface

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/entitylabels"
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

// requireDocumentWritable enforces the reference's `_require_writable` for a
// by-id document write. Outside the caller's read scope the document reads as
// the reference's own not-found — `Document '<id>' not found`, id echoed
// because that is the message `_require_writable` raises; a document the caller
// may read whose tags — or the tags the write would give it — fall outside the
// write scope is refused with the reference's 403. Without either scope it is a
// no-op, so an unextended deployment keeps its current behaviour. A nil newTags
// skips the write's own tag check, which a delete has none of.
func (h *Handlers) requireDocumentWritable(w http.ResponseWriter, r *http.Request, doc storage.Document, newTags []string) bool {
	readScope := memoryops.ScopeFromRequest(r)
	writeScope, confined := memoryops.WriteScopeFromRequest(r)
	if len(readScope) == 0 && !confined {
		return true
	}
	// A document hidden by the scope reads exactly as an absent one.
	if !storage.TagsVisibleUnderScope(doc.Tags, readScope) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound,
			fmt.Sprintf("Document '%s' not found", doc.ID)))
		return false
	}
	what := fmt.Sprintf("document '%s'", doc.ID)
	if !tagsWritableUnderScope(doc.Tags, writeScope, confined) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindForbidden,
			fmt.Sprintf("You can't write %s tagged %s", what, strings.Join(unwritableTags(doc.Tags, writeScope), ", "))))
		return false
	}
	if newTags != nil && !tagsWritableUnderScope(newTags, writeScope, confined) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindForbidden,
			fmt.Sprintf("You can't write an item tagged %s", strings.Join(unwritableTags(newTags, writeScope), ", "))))
		return false
	}
	return true
}

// tagsWritableUnderScope reports whether every tag is one the caller may write.
// With no write scope every tag is writable; an untagged item belongs to
// everyone, so a confined writer may neither produce nor change one — matching
// the reference's `tags_writable`.
func tagsWritableUnderScope(tags, writeScope []string, confined bool) bool {
	if !confined {
		return true
	}
	if len(tags) == 0 {
		return false
	}
	return httpx.TagsWritable(tags, writeScope, confined)
}

// unwritableTags names the tags the caller may not write, sorted, or
// `(untagged)` when the item carries none.
func unwritableTags(tags, writeScope []string) []string {
	outside := make([]string, 0, len(tags))
	for _, tag := range tags {
		if !httpx.TagsWritable([]string{tag}, writeScope, true) {
			outside = append(outside, tag)
		}
	}
	sort.Strings(outside)
	if len(outside) == 0 {
		return []string{"(untagged)"}
	}
	return outside
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
	if !literalParam(w, r, "time_field", []string{"created_at", "updated_at"}) {
		return
	}
	start, ok := parseDateParam(w, r, "start_date")
	if !ok {
		return
	}
	end, ok := parseDateParam(w, r, "end_date")
	if !ok {
		return
	}
	if start != nil && end != nil && !end.After(*start) {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest,
			"Invalid time window: end_date must be after start_date."))
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
	httpx.WriteJSON(w, http.StatusOK, DocumentListResponse{
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
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Document not found"))
		return
	}
	// The store counts the document's units in the same read that resolves the
	// row, so the count does not depend on the document appearing in a listing.
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
	scopes := presentObservationScopes(doc.RetainParams)
	httpx.WriteJSON(w, http.StatusOK, DocumentResponse{
		ID: doc.ID, BankID: doc.BankID, OriginalText: optionalString(doc.OriginalText),
		ContentHash: optionalString(doc.ContentHash), CreatedAt: httpx.FormatTimestamp(doc.CreatedAt),
		UpdatedAt: httpx.FormatTimestamp(doc.UpdatedAt), MemoryUnitCount: doc.MemoryUnitCount,
		Tags: tags, Metadata: doc.Metadata, RetainParams: presentRetainParams(doc.RetainParams),
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

// presentObservationScopes returns the retain-time scope spec when it carries
// content; an absent or empty value reads as null, the shape the reference
// renders for a falsy scope.
func presentObservationScopes(retainParams map[string]any) any {
	if retainParams == nil {
		return nil
	}
	value, ok := retainParams["observation_scopes"]
	if !ok || value == nil {
		return nil
	}
	if list, ok := value.([]any); ok && len(list) == 0 {
		return nil
	}
	return value
}

// UpdateDocument applies a partial update and reports the outcome.
func (h *Handlers) UpdateDocument(w http.ResponseWriter, r *http.Request) {
	bankID, docID, ok := h.documentMember(w, r)
	if !ok {
		return
	}
	// The body is validated before the document is looked up, the way the
	// framework validates a request model before the endpoint body runs: a
	// request that names no field is rejected regardless of the document.
	var body UpdateDocumentRequest
	if err := httpx.DecodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	if body.Tags == nil {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindUnprocessable,
			"At least one field (tags) must be provided"))
		return
	}
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// Reading a document does not make it the caller's to change: outside its
	// read scope the document reads as not-found, and a visible one whose tags —
	// or the tags the write would give it — the caller may not write is refused
	// with 403. A deployment that resolves no scope is left untouched.
	if !h.requireDocumentWritable(w, r, doc, *body.Tags) {
		return
	}
	oldTags := append([]string(nil), doc.Tags...)
	doc.Tags = append([]string(nil), (*body.Tags)...)
	if _, err := h.Store.PutDocument(r.Context(), bankID, docID, doc); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// A tag rename on the document must carry into its units and the observation
	// scopes frozen on them, and the observations built over them must be rebuilt
	// under the new tag — otherwise recall by the new tag no longer reaches them.
	// A tag set equal to the current one is a no-op (a bulk normalisation sweep
	// re-sends an identical array), so the cascade is skipped for it.
	if !sameTagSet(oldTags, doc.Tags) {
		deleted, err := h.Store.RetagDocumentMemories(r.Context(), bankID, docID, oldTags, doc.Tags,
			h.bankLabelTagKeys(r.Context(), bankID))
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		if deleted > 0 && h.autoConsolidationEnabled(r.Context(), bankID) {
			if _, _, err := h.Store.EnqueueConsolidation(r.Context(), bankID); err != nil {
				httpx.WriteErrorFor(w, r, asHTTPError(err))
				return
			}
		}
	}
	httpx.WriteJSON(w, http.StatusOK, UpdateDocumentResponse{Success: true})
}

// autoConsolidationEnabled reports whether a document retag that invalidated
// observations re-consolidates the bank. The reference submits the pass only
// when the resolved enable_auto_consolidation is on; the bank's own override
// wins over the deployment default the composition root supplies.
func (h *Handlers) autoConsolidationEnabled(ctx context.Context, bankID string) bool {
	auto := h.AutoConsolidationEnabled
	if bank, err := h.Store.GetBank(ctx, bankID); err == nil {
		if value, ok := bank.Config["enable_auto_consolidation"].(bool); ok {
			auto = value
		}
	}
	return auto
}

// bankLabelTagKeys returns the keys of the bank's entity_labels groups flagged
// tag:true — the tag keys a unit's tags may have been derived from, rather than
// supplied by the caller, at extraction. A retag replaces only the document's
// own tags, so it needs to tell the two apart to keep the label projection.
// Configuration that cannot be read yields no keys, which leaves every tag on a
// unit looking caller-supplied: the retag then behaves as it did before.
func (h *Handlers) bankLabelTagKeys(ctx context.Context, bankID string) map[string]bool {
	overrides, err := h.Store.BankConfig(ctx, bankID)
	if err != nil {
		return nil
	}
	vocabulary, err := entitylabels.Parse(config.ResolveLayers(overrides, nil, nil)["entity_labels"])
	if err != nil {
		return nil
	}
	return vocabulary.TagKeys()
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
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// A delete is a by-id write, so it carries the same read/write scope gate a
	// retag does: a document outside the caller's read scope reads as not-found,
	// and one whose tags the caller may not write is refused. A delete changes
	// no tags, so there is no new-tag set to check.
	if !h.requireDocumentWritable(w, r, doc, nil) {
		return
	}
	unitsDeleted, err := h.Store.DeleteDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, DeleteDocumentResponse{
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
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Document not found"))
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
	httpx.WriteJSON(w, http.StatusOK, ChunkListResponse{
		Items: items, Total: page.Total, Limit: limit, Offset: offset,
	})
}

// ReprocessDocument re-extracts one document from its stored text.
//
// The write is queued, not run in the request: the reply names the pending
// batch operation the caller polls, and the extraction runs later in a worker.
// The queued write runs through the same configuration path the write route
// uses, so a re-extraction is shaped by the bank's current settings and the
// document's own replayable parameters.
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
	httpx.WriteJSON(w, http.StatusOK, ReprocessDocumentResponse{
		Success: true, OperationID: outcome.OperationID, ItemsCount: 1,
	})
}

// GetChunk returns one chunk, addressed by its globally unique identifier. The
// identifier is the raw path remainder after the namespace, exactly as the
// reference captures it: a trailing or doubled slash is part of the id, so it
// resolves to no chunk rather than being trimmed away.
func (h *Handlers) GetChunk(w http.ResponseWriter, r *http.Request) {
	chunkID := strings.TrimPrefix(r.URL.Path, httpx.PrefixChunksV1)
	if chunkID == "" {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Chunk not found"))
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
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Chunk not found"))
		return
	}
	// A chunk addressed by its own id resolves its bank and document from the
	// chunk, so its attachments are looked up under that document.
	byChunk, err := h.Store.ChunkAttachments(r.Context(), chunk.BankID, chunk.DocumentID)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toChunkResponse(chunk, byChunk[chunk.ChunkID]))
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
