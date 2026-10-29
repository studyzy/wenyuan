package readsurface

import (
	"errors"
	"net/http"

	"github.com/studyzy/wenyuan/internal/history"
	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// storedHistoryPage converts a storage page into the history package's own read
// model. The two carry the same members; the conversion keeps the history
// package free of a storage import (the storage layer records history, so the
// other direction would cycle).
func storedHistoryPage(page storage.DocumentEventPage) history.StoredPage {
	events := make([]history.StoredEvent, 0, len(page.Events))
	for _, event := range page.Events {
		events = append(events, history.StoredEvent{OperationID: event.OperationID, Members: event.Members})
	}
	return history.StoredPage{
		DocumentID: page.DocumentID, Total: page.Total, Limit: page.Limit, Offset: page.Offset, Events: events,
	}
}

// documentHistoryPage is the default number of operations one history page
// carries, and the bound the reference declares (api/http.py: get_document_history
// reads `limit: int = Query(default=100, ge=1, le=1000)`).
const (
	defaultDocumentHistoryLimit = 100
	maxDocumentHistoryLimit     = 1000
)

// GetDocumentHistory serves a document's change history, newest first.
//
// The reference registers this route before the greedy
// `GET /documents/{document_id:path}` route, so a remainder ending in `/history`
// resolves to a document's history rather than to a document whose id happens to
// end in `/history` — a document so named is unreachable through this path in
// the reference too. The dispatch in handlers.go splits the suffix off and sets
// the document id, so this handler sees the same path value the other member
// reads do.
func (h *Handlers) GetDocumentHistory(w http.ResponseWriter, r *http.Request) {
	// `limit` and `offset` are `Query(...)` parameters in the reference, so they
	// are validated before the document is read: a bad page size is the 422 even
	// when the document does not exist.
	limit, ok := intParam(w, r, "limit", defaultDocumentHistoryLimit, 1, maxDocumentHistoryLimit)
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
	// An absent document is not an error here: the reference reads the history
	// out of the bank's operations and answers an empty page, because a change
	// whose operation outlived the document row still belongs to the timeline
	// (memory_engine.py:14020-14038). Only a document outside the caller's tag
	// scope reads as the reference's not-found.
	doc, err := h.Store.GetDocument(r.Context(), bankID, docID)
	switch {
	case errors.Is(err, storage.ErrDocumentNotFound):
	case err != nil:
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	case !documentVisible(r, doc):
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindNotFound, "Document not found"))
		return
	}
	page, err := h.Store.ListDocumentEvents(r.Context(), bankID, docID, limit, offset)
	if err != nil {
		httpx.WriteErrorFor(w, r, asHTTPError(err))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, history.Page(storedHistoryPage(page)))
}
