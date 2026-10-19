package memoryops

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// decodeJSON reads a JSON body into the target value and reports any top-level
// field the target model does not accept, so the ignored-parameter header
// names a typo instead of silently dropping it. It returns the bytes it read,
// so a caller that reports a rejected body can name the value the decode
// failed on the way the shared decoder does. An absent body is reported as
// httpx.ErrEmptyBody, which the shared renderer answers with the item for a
// missing required field.
func decodeJSON(r *http.Request, target any) ([]byte, error) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	httpx.RecordUnknownBodyFields(r, body, target)
	if len(strings.TrimSpace(string(body))) == 0 {
		return body, httpx.ErrEmptyBody
	}
	return httpx.DecodeLax(body, target)
}

// OperationIDPathParam is the path parameter naming the async operation.
const OperationIDPathParam = "operation_id"

// rejectMalformedOperationID writes the reference's bad-request envelope for an
// identifier that is not a UUID and reports whether it did. Every operation
// member route shares it, so the same malformed identifier gets the same 400 and
// a plain detail string rather than any route's structured validation body.
func rejectMalformedOperationID(w http.ResponseWriter, r *http.Request, opID string) bool {
	if _, ok := canonicalOperationID(opID); !ok {
		httpx.WriteErrorFor(w, r, httpx.NewHTTPError(httpx.KindBadRequest, "Invalid operation_id format: "+opID))
		return true
	}
	return false
}

// canonicalOperationID returns the canonical dashed form of a UUID the
// reference's parser accepts. Python's uuid.UUID takes the 32-hex form, the
// dashed 8-4-4-4-12 form, and the brace- and urn-wrapped spellings as the same
// identifier; each is folded to the dashed form the column stores so a lookup
// addresses the row regardless of which spelling the caller used.
func canonicalOperationID(raw string) (string, bool) {
	value := raw
	lower := strings.ToLower(value)
	if strings.HasPrefix(lower, "urn:uuid:") {
		value = value[len("urn:uuid:"):]
	}
	if len(value) >= 2 && value[0] == '{' && value[len(value)-1] == '}' {
		value = value[1 : len(value)-1]
	}
	switch len(value) {
	case 32:
		if !isHexString(value) {
			return "", false
		}
	case 36:
		if !IsValidUUIDShape(value) {
			return "", false
		}
		value = strings.ReplaceAll(value, "-", "")
	default:
		return "", false
	}
	hexed := strings.ToLower(value)
	return hexed[0:8] + "-" + hexed[8:12] + "-" + hexed[12:16] + "-" + hexed[16:20] + "-" + hexed[20:32], true
}

// isHexString reports whether every byte is a hexadecimal digit.
func isHexString(value string) bool {
	if value == "" {
		return false
	}
	for i := range value {
		if !isHexDigit(value[i]) {
			return false
		}
	}
	return true
}

// GetOperation serves the asynchronous operation status route. The lookup is
// scoped to the path bank, so an identifier belonging to another bank reads as
// not-found rather than exposing that bank's operation. A missing operation is
// a 200 with status "not_found", matching the reference rather than a 404.
func (h *Handlers) GetOperation(w http.ResponseWriter, r *http.Request) {
	opID := r.PathValue(OperationIDPathParam)
	canonical, ok := canonicalOperationID(opID)
	if !ok {
		rejectMalformedOperationID(w, r, opID)
		return
	}
	bankID := r.PathValue("bank_id")
	includePayload, boolFailure := queryBool(r, "include_payload")
	if boolFailure != nil {
		httpx.WriteValidationFailure(w, &httpx.ValidationFailure{Errors: []httpx.ValidationError{*boolFailure}})
		return
	}
	op, err := h.Store.GetOperation(r.Context(), bankID, canonical)
	if err != nil {
		if errors.Is(err, storage.ErrOperationNotFound) {
			httpx.WriteJSON(w, http.StatusOK, notFoundOperationStatus(opID))
			return
		}
		httpx.WriteErrorFor(w, r, h.mapOperationError(err))
		return
	}
	var children []storage.ChildOperation
	if op.IsParent {
		children, err = h.Store.GetOperationChildren(r.Context(), bankID, canonical)
		if err != nil {
			httpx.WriteErrorFor(w, r, h.mapOperationError(err))
			return
		}
		// Self-heal: a parent left "pending" though every child has finished —
		// a crash between a child's terminal write and its promotion — reads as
		// its corrected terminal status, and the correction is persisted. Only
		// completed/failed children count as done here, mirroring the
		// reference's read-path repair; a cancelled child leaves the parent
		// untouched. The response keeps the row's pre-repair timestamps, so its
		// completed_at is still null on this first read, exactly as the
		// reference returns it.
		if op.Status == storage.StatusPending && batchChildrenFinished(children) {
			corrected := storage.StatusCompleted
			for _, child := range children {
				if child.Status == storage.StatusFailed {
					corrected = storage.StatusFailed
					break
				}
			}
			if err := h.Store.RepairPendingBatchParent(r.Context(), bankID, canonical, corrected); err != nil {
				httpx.WriteErrorFor(w, r, h.mapOperationError(err))
				return
			}
			op.Status = corrected
		}
	}
	httpx.WriteJSON(w, http.StatusOK, ToOperationStatusDTO(op, opID, children, includePayload))
}

// batchChildrenFinished reports whether every child of a parent batch has
// reached a status the read-path repair treats as finished: completed or
// failed. An empty child set is finished, as it is in the reference, whose
// loop over no children leaves its all-done flag set. A cancelled child is not
// finished: the read-path repair does not roll a cancelled child up, unlike the
// worker-path promotion.
func batchChildrenFinished(children []storage.ChildOperation) bool {
	for _, child := range children {
		if child.Status != storage.StatusCompleted && child.Status != storage.StatusFailed {
			return false
		}
	}
	return true
}

// queryBool reads a boolean query parameter the way the reference's framework
// coerces one: an absent parameter takes the default, and the lax spelling set
// ("true"/"false"/"1"/"0"/"yes"/"no"/"on"/"off"/"t"/"f"/"y"/"n", case
// insensitive) is accepted. Any other value — including an empty one — is the
// framework's bool_parsing failure.
func queryBool(r *http.Request, key string) (bool, *httpx.ValidationError) {
	values, present := r.URL.Query()[key]
	if !present || len(values) == 0 {
		return false, nil
	}
	// The reference binds the LAST occurrence of a repeated scalar parameter.
	raw := values[len(values)-1]
	switch strings.ToLower(raw) {
	case "true", "1", "yes", "on", "t", "y":
		return true, nil
	case "false", "0", "no", "off", "f", "n":
		return false, nil
	default:
		item := httpx.TypeMismatch("bool_parsing", []string{"query", key}, raw)
		return false, &item
	}
}

// CancelOperation serves the operation-cancellation route.
func (h *Handlers) CancelOperation(w http.ResponseWriter, r *http.Request) {
	// Operations are bank-wide, so a tag-scoped caller may not touch them.
	if httpx.RefuseBankWideIfScoped(w, r, "cancel the bank's operations") {
		return
	}
	opID := r.PathValue(OperationIDPathParam)
	canonical, ok := canonicalOperationID(opID)
	if !ok {
		rejectMalformedOperationID(w, r, opID)
		return
	}
	bankID := r.PathValue("bank_id")
	op, err := h.Store.CancelOperation(r.Context(), bankID, canonical)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapCancelError(err, op, opID, bankID))
		return
	}
	// If the cancelled row was the last outstanding child of a batch, its parent
	// is terminalised now — nothing else will, and a parent left in 'processing'
	// is the wedge this route exists to clear. Best-effort, matching the
	// reference: the cancellation itself already stands.
	h.rollUpBatchParent(r, bankID, op)
	httpx.WriteJSON(w, http.StatusOK, OperationActionResponse{
		Success: true, Message: "Operation " + opID + " cancelled", OperationID: opID,
	})
}

// RetryOperation starts a new run of a failed or cancelled operation.
func (h *Handlers) RetryOperation(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "retry the bank's operations") {
		return
	}
	opID := r.PathValue(OperationIDPathParam)
	canonical, ok := canonicalOperationID(opID)
	if !ok {
		rejectMalformedOperationID(w, r, opID)
		return
	}
	bankID := r.PathValue("bank_id")
	op, err := h.Store.RetryOperation(r.Context(), bankID, canonical)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapRetryError(err, op, opID, bankID))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, OperationActionResponse{
		Success: true, Message: "Operation " + opID + " queued for retry", OperationID: opID,
	})
}

// rollUpBatchParent terminalises the parent of a batch child that just reached a
// terminal status, when the child's metadata names one.
func (h *Handlers) rollUpBatchParent(r *http.Request, bankID string, op storage.Operation) {
	parentID, _ := op.ResultMetadata["parent_operation_id"].(string)
	if parentID == "" {
		return
	}
	_, _, _ = h.Store.FinalizeBatchParent(r.Context(), bankID, parentID)
}

// DeleteOperation permanently removes a terminal operation. A malformed
// identifier is rejected before any lookup, and only failed, cancelled or
// completed operations are removable.
func (h *Handlers) DeleteOperation(w http.ResponseWriter, r *http.Request) {
	if httpx.RefuseBankWideIfScoped(w, r, "delete the bank's operations") {
		return
	}
	opID := r.PathValue(OperationIDPathParam)
	// The reference rejects a non-UUID identifier with a 400 and a plain
	// detail string, not the structured validation envelope.
	canonical, ok := canonicalOperationID(opID)
	if !ok {
		rejectMalformedOperationID(w, r, opID)
		return
	}
	bankID := r.PathValue("bank_id")
	observed, err := h.Store.DeleteOperation(r.Context(), bankID, canonical)
	if err != nil {
		httpx.WriteErrorFor(w, r, h.mapDeleteOperationError(err, observed, opID, bankID))
		return
	}
	httpx.WriteJSON(w, http.StatusOK, OperationActionResponse{
		Success: true, Message: "Operation " + opID + " deleted", OperationID: opID,
	})
}

// mapDeleteOperationError renders the delete-specific failures. A live
// operation is a conflict whose message names the observed status.
func (h *Handlers) mapDeleteOperationError(err error, op storage.Operation, opID, bankID string) error {
	switch {
	case errors.Is(err, storage.ErrOperationNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "Operation "+opID+" not found for bank "+bankID)
	case errors.Is(err, storage.ErrOperationNotTerminal):
		// The store hands back the row it refused to delete, so the message can
		// name the status the caller actually observed, as the reference does.
		return httpx.NewHTTPError(httpx.KindConflict,
			"Operation "+opID+" cannot be deleted: status is '"+string(op.Status)+
				"', expected 'failed', 'cancelled' or 'completed'")
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

// mapCancelError renders the not-found and conflict failures the cancel route
// reports. The reference's conflict message names the identifier and the status
// the row actually held.
func (h *Handlers) mapCancelError(err error, op storage.Operation, opID, bankID string) error {
	switch {
	case errors.Is(err, storage.ErrOperationNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "Operation "+opID+" not found for bank "+bankID)
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict,
			"Operation "+opID+" cannot be cancelled: status is '"+string(op.Status)+
				"', only 'pending' and 'processing' operations can be cancelled")
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

// mapRetryError renders the not-found and conflict failures the retry route
// reports. The reference's conflict message names the identifier and the status
// the row actually held.
func (h *Handlers) mapRetryError(err error, op storage.Operation, opID, bankID string) error {
	switch {
	case errors.Is(err, storage.ErrOperationNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "Operation "+opID+" not found for bank "+bankID)
	case errors.Is(err, storage.ErrBatchParentNoWork):
		// A batch_retain parent is a payload-less status aggregator; retrying it
		// means re-running the sub-batches beneath it, and with none left to
		// re-run the reference answers its own 409 pointing at resubmission.
		return httpx.NewHTTPError(httpx.KindConflict,
			"Operation "+opID+" is a batch_retain parent with no incomplete sub-batches to retry "+
				"(its children have all completed, or it has none). Resubmit the source documents "+
				"to re-ingest them, then delete this record via DELETE /operations/"+opID+"/delete.")
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict,
			"Operation "+opID+" cannot be retried: status is '"+string(op.Status)+"', expected 'failed' or 'cancelled'")
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

func (h *Handlers) mapOperationError(err error) error {
	switch {
	case errors.Is(err, storage.ErrOperationNotFound):
		return httpx.NewHTTPError(httpx.KindNotFound, "operation not found")
	case errors.Is(err, storage.ErrOperationConflict):
		return httpx.NewHTTPError(httpx.KindConflict, "operation is already in a terminal state")
	default:
		return httpx.NewHTTPError(httpx.KindInternal, httpx.DetailInternal)
	}
}

// ToOperationListDTO maps a stored operation into the list entry the reference
// calls OperationResponse. The subject and outcome fields come from the result
// metadata, which is the only per-operation record the list carries.
func ToOperationListDTO(op storage.Operation) Operation {
	dto := Operation{
		ID:          op.OperationID,
		TaskType:    op.OperationType,
		ItemsCount:  itemsCountOf(op.ResultMetadata),
		Status:      string(op.Status),
		CreatedAt:   httpx.FormatTimestamp(op.CreatedAt),
		RetryCount:  op.RetryCount,
		NextRetryAt: httpx.FormatTimestampPtr(op.NextRetryAt),
		Progress:    progressOf(op.ResultMetadata),
		Details:     operationDetails(op.OperationType, op.ResultMetadata),
	}
	opID := op.OperationID
	dto.OperationID = &opID
	if op.OperationType != "" {
		taskType := op.OperationType
		dto.OperationType = &taskType
	}
	dto.DocumentID = optionalString(op.ResultMetadata, "document_id")
	dto.Filename = optionalString(op.ResultMetadata, "original_filename")
	dto.MentalModelID = optionalString(op.ResultMetadata, storage.MentalModelIDPayloadKey)
	dto.ErrorMessage = stringOrNil(op.Error)
	if !op.UpdatedAt.IsZero() {
		updated := httpx.FormatTimestamp(op.UpdatedAt)
		dto.UpdatedAt = &updated
	}
	return dto
}

// ToOperationStatusDTO maps a stored operation into the single-operation read
// the reference calls OperationStatusResponse: the full status plus the child
// roll-up and, when requested, the raw task payload. rawOpID is the identifier
// the caller sent, echoed back verbatim so a non-canonical spelling round-trips.
func ToOperationStatusDTO(op storage.Operation, rawOpID string, children []storage.ChildOperation, includePayload bool) OperationStatus {
	dto := OperationStatus{
		OperationID: rawOpID,
		Status:      string(op.Status),
	}
	if op.OperationType != "" {
		value := op.OperationType
		dto.TaskType = &value
		operationType := op.OperationType
		dto.OperationType = &operationType
	}
	id := rawOpID
	dto.ID = &id
	dto.MentalModelID = optionalString(op.ResultMetadata, storage.MentalModelIDPayloadKey)
	if !op.CreatedAt.IsZero() {
		created := httpx.FormatTimestamp(op.CreatedAt)
		dto.CreatedAt = &created
	}
	if !op.UpdatedAt.IsZero() {
		updated := httpx.FormatTimestamp(op.UpdatedAt)
		dto.UpdatedAt = &updated
	}
	dto.CompletedAt = httpx.FormatTimestampPtr(op.CompletedAt)
	dto.ErrorMessage = stringOrNil(op.Error)
	retryCount := op.RetryCount
	dto.RetryCount = &retryCount
	dto.NextRetryAt = httpx.FormatTimestampPtr(op.NextRetryAt)
	dto.Progress = progressOf(op.ResultMetadata)
	dto.ResultMetadata = resultMetadataOf(op.ResultMetadata)
	dto.Details = operationDetails(op.OperationType, op.ResultMetadata)
	if op.IsParent {
		dto.ChildOperations = make([]ChildOperationStatus, 0, len(children))
		for _, child := range children {
			dto.ChildOperations = append(dto.ChildOperations, ChildOperationStatus{
				OperationID:   child.OperationID,
				Status:        string(child.Status),
				SubBatchIndex: child.SubBatchIndex,
				ItemsCount:    child.ItemsCount,
				ErrorMessage:  child.ErrorMessage,
			})
		}
	}
	if includePayload {
		dto.TaskPayload = fullTaskPayload(op)
	}
	return dto
}

// fullTaskPayload is the raw task payload the reference returns under
// include_payload: the stored params merged under the three envelope keys the
// reference's submit path always prepends (type, operation_id, bank_id). A
// refresh_mental_model payload therefore surfaces as
// {type, operation_id, bank_id, mental_model_id} rather than the bare stored
// map.
//
// An operation with no stored payload — the batch_retain parent is a
// payload-less status aggregator no worker executes — reports null, exactly as
// the reference does: it returns the task_payload column verbatim, and that
// column is NULL for a parent. A batch_retain row this service wrote with an
// empty object rather than NULL reads the same way. Synthesising the envelope
// for such a row would invent a payload the reference never emits.
// notFoundOperationStatus is the single-operation read for a missing (or
// wrong-bank) identifier: the reference answers 200 with status "not_found"
// rather than a 404, carrying the full sixteen-key body with every other member
// null.
func notFoundOperationStatus(opID string) OperationStatus {
	id := opID
	return OperationStatus{OperationID: opID, Status: string(storage.StatusNotFound), ID: &id}
}

// resultMetadataOf is the found-operation projection of result_metadata. The
// reference always emits the map for a found row, so an empty column serializes
// as {} rather than being dropped.
func resultMetadataOf(metadata map[string]any) map[string]any {
	if metadata == nil {
		return map[string]any{}
	}
	return metadata
}

// progressOf projects the last-known progress snapshot out of result_metadata.
// The reference emits whatever object a producer wrote under "progress", or null
// when none was recorded.
func progressOf(metadata map[string]any) map[string]any {
	if snapshot, ok := metadata["progress"].(map[string]any); ok {
		return snapshot
	}
	return nil
}

// refreshOutcomes and refreshFailureReasons are the reference's typed outcome
// vocabulary for a refresh_mental_model operation. Only a value in these sets is
// projected into the supported `details` field; anything else — the shape a
// rolling upgrade produces — reports no detail rather than an unrecognised one.
var refreshOutcomes = map[string]bool{
	"content_written": true, "content_unchanged": true, "content_preserved_no_new_facts": true,
	"refresh_failed_empty_candidate": true, "refresh_failed_delta_not_applied": true,
	"refresh_failed_structured_output": true, "refresh_failed_error": true,
}

var refreshFailureReasons = map[string]bool{
	"empty_candidate": true, "structured_doc_unreadable": true, "delta_ops_failed": true,
	"delta_ops_all_skipped": true, "delta_not_applied": true, "structured_output_failed": true,
	"retrieval_failed": true, "no_answer": true, "unexpected_error": true,
}

// operationDetails projects the typed per-operation-type outcome the reference
// surfaces in `details`. Today only refresh_mental_model contributes a shape,
// and only once it recorded a recognised outcome.
func operationDetails(operationType string, metadata map[string]any) map[string]any {
	if operationType != "refresh_mental_model" {
		return nil
	}
	outcome, ok := metadata["outcome"].(string)
	if !ok || !refreshOutcomes[outcome] {
		return nil
	}
	details := map[string]any{"operation_type": "refresh_mental_model", "outcome": outcome}
	details["failure_reason"] = nil
	if reason, ok := metadata["failure_reason"].(string); ok && refreshFailureReasons[reason] {
		details["failure_reason"] = reason
	}
	return details
}

// itemsCountOf reads the ready item count a completed operation recorded. The
// value is an int when a processor wrote it in-process and a float64 when it
// round-tripped through JSON, so both are accepted.
func itemsCountOf(metadata map[string]any) int {
	switch count := metadata["items_count"].(type) {
	case float64:
		return int(count)
	case int:
		return count
	case int64:
		return int(count)
	case json.Number:
		if value, err := count.Int64(); err == nil {
			return int(value)
		}
	}
	return 0
}

// optionalString reads a string member from a decoded object, returning nil for
// an absent or non-string value so the field is emitted as null.
func optionalString(object map[string]any, key string) *string {
	if value, ok := object[key].(string); ok {
		return &value
	}
	return nil
}

// stringOrNil returns a pointer to a non-empty string, and nil for an empty one
// so the field is emitted as null.
func stringOrNil(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}
