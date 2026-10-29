package readsurface

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/studyzy/wenyuan/internal/config"
	"github.com/studyzy/wenyuan/internal/entitylabels"
	"github.com/studyzy/wenyuan/internal/history"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/memoryops"
	"github.com/studyzy/wenyuan/internal/storage"
	"github.com/studyzy/wenyuan/internal/validation"
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

// documentWriteError renders a by-id document write's failed lookup. Under an
// active scope the reference's write gate answers before the document is read,
// echoing the identifier it was handed, so an absent document and one outside
// the caller's scope read the same; without a scope the write's own plain
// not-found stands.
func documentWriteError(r *http.Request, docID string, err error) *httpx.HTTPError {
	if errors.Is(err, storage.ErrDocumentNotFound) && scopeActive(r) {
		return httpx.NewHTTPError(httpx.KindNotFound, fmt.Sprintf("Document '%s' not found", docID))
	}
	return asHTTPError(err)
}

// scopeActive reports whether the caller's resolved scope can gate a by-id
// write at all, so an unextended deployment keeps its current behaviour.
func scopeActive(r *http.Request) bool {
	if len(memoryops.ScopeFromRequest(r)) > 0 {
		return true
	}
	_, confined := memoryops.WriteScopeFromRequest(r)
	return confined
}

// ListDocuments returns one page of the bank's documents.
func (h *Handlers) ListDocuments(w http.ResponseWriter, r *http.Request) {
	// The query parameters are validated before the bank is looked up: in the
	// reference they are `Query(...)` parameters the framework resolves ahead of
	// the handler body, so a bad one is the 422 even for an unknown bank. The
	// start/end dates are parsed inside the handler and so also precede the
	// bank check, but after every `Query` one — which is why they stay below.
	// Every `Query(...)` parameter is validated in one pass, because the
	// reference lists all of a request's failures in a single 422, in the order
	// the route declares them: tags_match, time_field, limit, offset.
	var query queryErrors
	tags := queryValues(r, "tags")
	tagsMatch := httpx.Query(r, "tags_match")
	if tagsMatch == "" {
		tagsMatch = storage.TagsMatchAnyStrict
	}
	// tags_match is a Literal in v1.1.0 (it was a plain string in v1.0.0), so an
	// unknown mode is now the framework's 422. storage already implements every
	// mode, including exact; only the acceptance check was missing.
	query.literal(r, "tags_match", validation.TagsMatchModes, storage.TagsMatchAnyStrict)
	query.literal(r, "time_field", []string{"created_at", "updated_at"}, "")
	limit := query.int(r, "limit", defaultPageLimit, 0, -1)
	offset := query.int(r, "offset", 0, 0, -1)
	if !query.empty() {
		query.flush(w)
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
	bankID, ok := h.requireBank(w, r)
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
		Query:     httpx.Query(r, "q"),
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
		TotalIsEstimate: page.TotalIsEstimate,
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
	// request that names no field is rejected regardless of the document, and so
	// is an observation_scopes value the request model does not admit.
	var body UpdateDocumentRequest
	if err := httpx.DecodeBody(r, &body); err != nil {
		httpx.WriteErrorFor(w, r, err)
		return
	}
	// An explicit null is the union's admitted None, so it decodes to no value
	// at all: a body naming only it names no field, and one naming it beside
	// tags updates the tags alone.
	scoped := observationScopesSent(body.ObservationScopes)
	if body.Tags == nil && !scoped {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindUnprocessable,
			"At least one field (tags, observation_scopes) must be provided"))
		return
	}
	if scoped {
		if items := documentScopesValidationItems(body.ObservationScopes); len(items) > 0 {
			httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: items})
			return
		}
	}
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, documentWriteError(r, docID, err))
		return
	}
	// Reading a document does not make it the caller's to change: outside its
	// read scope the document reads as not-found, and a visible one whose tags —
	// or the tags the write would give it — the caller may not write is refused
	// with 403. A deployment that resolves no scope is left untouched.
	var writeTags []string
	if body.Tags != nil {
		writeTags = *body.Tags
	}
	if !h.requireDocumentWritable(w, r, doc, writeTags) {
		return
	}
	// The scopes an update names are writes too — consolidation writes the
	// document's observations at them — so the reference checks them beside the
	// tags, after the document itself is writable. A confined caller sending
	// "shared", or literal scope tags outside its write scope, is refused.
	if scoped {
		writeScope, confined := memoryops.WriteScopeFromRequest(r)
		if err := memoryops.RefuseUnwritableScopes(body.ObservationScopes, writeScope, confined); err != nil {
			httpx.WriteErrorFor(w, r, err)
			return
		}
	}
	oldTags := append([]string(nil), doc.Tags...)
	retag := body.Tags != nil && !sameTagSet(oldTags, *body.Tags)
	if body.Tags != nil {
		doc.Tags = append([]string(nil), (*body.Tags)...)
	}
	if scoped {
		// Reprocess re-retains from these parameters, so a scoping left behind
		// here would come back the next time the document is reprocessed.
		params := make(map[string]any, len(doc.RetainParams)+1)
		for key, value := range doc.RetainParams {
			params[key] = value
		}
		params["observation_scopes"] = observationScopesValue(body.ObservationScopes)
		doc.RetainParams = params
	}
	if _, err := h.Store.PutDocument(r.Context(), bankID, docID, doc); err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	// A tag rename — or a change of observation scoping alone — must carry into
	// the document's units, and the observations of any unit whose RESOLVED
	// scopes move must be rebuilt: otherwise recall by the new tag no longer
	// reaches them, or the scoping they consolidated under is gone. A tag set
	// equal to the current one is a no-op (a bulk normalisation sweep re-sends an
	// identical array), so the cascade is skipped for it.
	if retag || scoped {
		change := storage.DocumentScopeChange{OldTags: oldTags, LabelKeys: h.bankLabelTagKeys(r.Context(), bankID)}
		if retag {
			change.NewTags = doc.Tags
		}
		if scoped {
			change.NewScopes = body.ObservationScopes
		}
		outcome, err := h.Store.RetagDocumentMemories(r.Context(), bankID, docID, change)
		if err != nil {
			httpx.WriteErrorFor(w, r, asHTTPError(err))
			return
		}
		// The consolidation that rebuilds what the update removed or moved is
		// recorded on the change history, so the removal shows next to its
		// rebuild. Best-effort: a history write never fails the update.
		if op, submitted := h.submitRetagConsolidation(r.Context(), bankID, outcome); submitted {
			history.AppendRetagEvent(r.Context(), h.Store, op.OperationID, docID, removedObservationIDs(outcome.Removed))
		}
	}
	httpx.WriteJSON(w, http.StatusOK, UpdateDocumentResponse{Success: true})
}

// submitRetagConsolidation queues the consolidation that rebuilds what a
// document update removed or requeued — the reference submits it when either an
// observation went or a memory moved to another scope, not only when one went.
// The change history is told about it by the caller of the update.
func (h *Handlers) submitRetagConsolidation(
	ctx context.Context, bankID string, outcome storage.RetagOutcome,
) (storage.Operation, bool) {
	if outcome.InvalidatedObservations == 0 && outcome.RequeuedUnits == 0 {
		return storage.Operation{}, false
	}
	if !h.autoConsolidationEnabled(ctx, bankID) {
		return storage.Operation{}, false
	}
	op, _, err := h.Store.EnqueueConsolidation(ctx, bankID)
	if err != nil {
		return storage.Operation{}, false
	}
	return op, true
}

// removedObservationIDs names the observations a storage sweep removed, the
// form the change history records.
func removedObservationIDs(removed []storage.RemovedObservation) []string {
	ids := make([]string, 0, len(removed))
	for _, observation := range removed {
		ids = append(ids, observation.ObservationID)
	}
	return ids
}

// observationScopesValue decodes a raw observation_scopes request value for
// storage. The decoded value is the one the reference's model keeps, so a
// document reads back the scoping it was given: a mode name stays a string and a
// list of tag groups stays a list of tag lists.
func observationScopesValue(raw json.RawMessage) any {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil
	}
	return value
}

// observationScopesSent reports whether the request carries an
// observation_scopes value. The reference's model admits null — the union's
// None member — so an explicit null decodes to no value at all, the way an
// omitted member does.
func observationScopesSent(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var value any
	return json.Unmarshal(raw, &value) == nil && value != nil
}

// documentScopesValidationItems renders the failures the reference's model
// reports for an observation_scopes value it does not admit, and none for one
// it takes.
func documentScopesValidationItems(raw json.RawMessage) []httpx.ValidationError {
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		item := httpx.ValidationItem("json_invalid", []string{"body", "observation_scopes"},
			"JSON decode error", nil, map[string]any{"error": err.Error()})
		return []httpx.ValidationError{item}
	}
	switch value := decoded.(type) {
	case string:
		if observationScopeMode(value) {
			return nil
		}
	case []any:
		if documentScopeGroupsValid(value) {
			return nil
		}
	}
	return documentScopesUnionItems(decoded)
}

// documentScopeGroupsValid reports whether an explicit scope list is one the
// model admits: every group a list, and every tag in a group a string.
func documentScopeGroupsValid(groups []any) bool {
	for _, scope := range groups {
		entries, ok := scope.([]any)
		if !ok {
			return false
		}
		for _, entry := range entries {
			if _, ok := entry.(string); !ok {
				return false
			}
		}
	}
	return true
}

// documentScopesUnionItems renders the multi-item failure a value that fits
// neither branch of the observation_scopes union is answered with — the same
// shape the retain route reports: the literal failure carries the whole value,
// and the list failure is reported once for the value itself when it is not a
// list, or once per element that is not a group, with a non-string tag inside a
// group named at its own position.
func documentScopesUnionItems(value any) []httpx.ValidationError {
	loc := []string{"body", "observation_scopes"}
	items := []httpx.ValidationError{
		httpx.Literal(append(loc, scopeLiteralBranch), value, memoryops.ObservationScopeModes),
	}
	groups, isList := value.([]any)
	if !isList {
		return append(items, httpx.TypeMismatch("list_type", append(loc, scopeListBranch), value))
	}
	for index, element := range groups {
		elementLoc := append(append(cloneLoc(loc), scopeListBranch), strconv.Itoa(index))
		tags, isGroup := element.([]any)
		if !isGroup {
			items = append(items, httpx.TypeMismatch("list_type", elementLoc, element))
			continue
		}
		for position, tag := range tags {
			if _, isText := tag.(string); !isText {
				items = append(items, httpx.TypeMismatch("string_type",
					append(cloneLoc(elementLoc), strconv.Itoa(position)), tag))
			}
		}
	}
	return items
}

// scopeLiteralBranch and scopeListBranch are the names the framework gives the
// observation_scopes union's two branches in a rejected value's location. The
// literal branch's name is built from the accepted modes, so it cannot drift
// from the set the field validates against.
var (
	scopeLiteralBranch = func() string {
		quoted := make([]string, 0, len(memoryops.ObservationScopeModes))
		for _, mode := range memoryops.ObservationScopeModes {
			quoted = append(quoted, "'"+mode+"'")
		}
		return "literal[" + strings.Join(quoted, ",") + "]"
	}()
	scopeListBranch = "list[list[str]]"
)

// observationScopeMode reports whether a mode name is one the field accepts.
func observationScopeMode(value string) bool {
	for _, mode := range memoryops.ObservationScopeModes {
		if mode == value {
			return true
		}
	}
	return false
}

// cloneLoc returns a copy of a location, so an item that extends it does not
// write through to the caller's slice.
func cloneLoc(loc []string) []string {
	return append([]string(nil), loc...)
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
// scopes a memory by its tag set, never by the order the array arrived in, and
// the reference compares sets (`set(current_tags) != set(tags)`), so a
// reordered — or differently duplicated — array changes nothing the retag
// cascade could observe.
func sameTagSet(a, b []string) bool {
	left := tagSetOf(a)
	right := tagSetOf(b)
	if len(left) != len(right) {
		return false
	}
	for tag := range left {
		if !right[tag] {
			return false
		}
	}
	return true
}

// tagSetOf returns the distinct tags of a list.
func tagSetOf(tags []string) map[string]bool {
	out := make(map[string]bool, len(tags))
	for _, tag := range tags {
		out[tag] = true
	}
	return out
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
		httpx.WriteErrorFor(w, r, documentWriteError(r, docID, err))
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
	// `limit`/`offset` are `Query(...)` parameters in the reference, so they are
	// validated before the document is looked up: a bad page size is the 422 even
	// when the document does not exist.
	limit, ok := intParam(w, r, "limit", defaultPageLimit, 1, 1000)
	if !ok {
		return
	}
	offset, ok := intParam(w, r, "offset", 0, 0, -1)
	if !ok {
		return
	}
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
	// A reprocess is a by-id write — it deletes the document's units and
	// re-extracts them — so it carries the same read/write scope gate a retag
	// and a delete do, applied before the document is read the way the
	// reference's write gate is: outside the caller's read scope the document
	// reads as not-found, and one whose tags the caller may not write is
	// refused. It changes no tags, so there is no new-tag set to check.
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	if err != nil {
		httpx.WriteErrorFor(w, r, documentWriteError(r, docID, err))
		return
	}
	if !h.requireDocumentWritable(w, r, doc, nil) {
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
	chunk, err := h.Store.FindChunk(r.Context(), chunkID)
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
