package memoryops

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// queueFileRetain queues one uploaded file as a single file_convert_retain
// operation and returns that operation's identifier.
//
// The conversion itself already ran in the request — the bytes are text by the
// time this is called — but the conversion and the retain are one unit of work
// to the caller: they were told one identifier and poll it for the outcome. A
// completed conversion row beside a separate pending retain row split that unit
// in two, so the identifier handed back always read as finished work while the
// retain had not started.
//
// The row is therefore queued pending, carries the retain's task payload — the
// worker executes it — and records the document and the upload's original
// filename in its result metadata from the start, so a task whose conversion
// half fails never runs still reads as a file-convert retain for that document,
// and its error text carries the reason.
func (h *Handlers) queueFileRetain(ctx context.Context, bankID string, overrides map[string]any, write storage.RetainRequest, documentID, filename string) (string, error) {
	prepared, _, err := h.prepareRetain(ctx, bankID, overrides, write)
	if err != nil {
		return "", err
	}
	prepared.Async = true
	assignDocumentIDs(&prepared)
	operationID := storage.NewDocumentID()
	metadata := map[string]any{"document_id": documentID}
	if filename != "" {
		metadata["original_filename"] = filename
	}
	op := storage.Operation{
		OperationID:    operationID,
		BankID:         bankID,
		OperationType:  storage.OperationTypeFileConvertRetain,
		Status:         storage.StatusPending,
		ResultMetadata: metadata,
		Payload:        storage.AsyncRetainPayload(bankID, operationID, prepared),
	}
	if _, err := h.Store.PutOperation(ctx, op); err != nil {
		return "", err
	}
	return operationID, nil
}
