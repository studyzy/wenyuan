package memoryops

import (
	"context"

	"github.com/studyzy/wenyuan/internal/storage"
)

// recordFileConversion records the completed conversion half of a file upload:
// one file_convert_retain operation naming the document and the upload's
// original filename. The conversion itself ran in the request; the operation
// row is what lets a caller watching the upload tell that half finished before
// the queued retain did.
func (h *Handlers) recordFileConversion(ctx context.Context, bankID, documentID, filename string) (string, error) {
	operationID := storage.NewDocumentID()
	op := storage.Operation{
		OperationID:   operationID,
		BankID:        bankID,
		OperationType: storage.OperationTypeFileConvertRetain,
		Status:        storage.StatusCompleted,
		ResultMetadata: map[string]any{
			"document_id":       documentID,
			"original_filename": filename,
		},
		Payload: map[string]any{
			"document_id":       documentID,
			"original_filename": filename,
		},
	}
	if _, err := h.Store.PutOperation(ctx, op); err != nil {
		return "", err
	}
	return operationID, nil
}

// queueFileRetain queues the retain half of a file upload as a single retain
// operation, not a batch parent: one uploaded file is one document, and the
// reference submits it as the conversion task's follow-up. The operation
// carries the document and filename in its result metadata so the retain a
// caller sees in the operations list is traceable to the upload that produced
// it. Extraction happens in the worker, so a model failure surfaces on the
// operation instead of turning the upload request into a 500.
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
		OperationType:  storage.OperationTypeRetain,
		Status:         storage.StatusPending,
		ResultMetadata: metadata,
		Payload:        storage.AsyncRetainPayload(bankID, operationID, prepared),
	}
	if _, err := h.Store.PutOperation(ctx, op); err != nil {
		return "", err
	}
	return operationID, nil
}
