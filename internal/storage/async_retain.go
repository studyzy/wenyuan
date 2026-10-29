package storage

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/studyzy/wenyuan/internal/entitylabels"
)

// The async retain queue stores work the way the reference does: a parent
// `batch_retain` operation aggregates the batch and carries no task payload of
// its own, and one or more child `retain` operations carry the items that a
// worker actually processes. The parent's primary key is the identity a caller
// submits with (or is handed back), so a retry that reuses it joins the original
// submission instead of duplicating the work.

// AsyncRetainType is the operation type of the parent batch operation and the
// task type carried in a child's payload.
const AsyncRetainType = "batch_retain"

// DefaultRetainSubBatchTokens is the token budget one queued child operation
// carries. A submission larger than this is split into several children, each
// of which a worker runs on its own; the reference's own default is the same
// number, so the split points match.
const DefaultRetainSubBatchTokens = 10000

// AsyncRetainPayloadKey names the serialized RetainRequest a child operation
// carries, so a worker can rebuild the write without the HTTP request.
const AsyncRetainPayloadKey = "retain_request"

// AsyncRetainDefinedType is the `type` field inside the stored task payload,
// matching the reference's task dictionary.
const AsyncRetainDefinedType = "batch_retain"

// AsyncRetainSubmission reports what an async retain submit did.
type AsyncRetainSubmission struct {
	// OperationID is the parent operation's identifier: the value the caller
	// polls and the one to reuse to make a retry idempotent.
	OperationID string
	// ItemsCount is how many items the submission carried.
	ItemsCount int
	// Deduplicated reports that the caller-supplied identifier already named a
	// prior submission, so no new work was queued.
	Deduplicated bool
}

// asyncRetainTask is the JSON payload stored on a child operation. It carries
// everything the worker needs to run the write: the items plus the bank's
// resolved extraction and storage settings, which were read at submit time.
type asyncRetainTask struct {
	Type        string            `json:"type"`
	OperationID string            `json:"operation_id"`
	BankID      string            `json:"bank_id"`
	Items       []asyncRetainItem `json:"items"`
	// DocumentTags is the deprecated request-level tag alias.
	DocumentTags []string `json:"document_tags,omitempty"`
	// Extraction and storage settings resolved when the write was accepted.
	ExtractionMode         string `json:"extraction_mode,omitempty"`
	ExtractionMission      string `json:"extraction_mission,omitempty"`
	ExtractionInstructions string `json:"extraction_instructions,omitempty"`
	OptionalFactDimensions bool   `json:"optional_fact_dimensions,omitempty"`
	ChunkSize              int    `json:"chunk_size,omitempty"`
	StructuredChunkSize    int    `json:"structured_chunk_size,omitempty"`
	DiscardDocumentText    bool   `json:"discard_document_text,omitempty"`
	// Vocabulary is the bank's controlled label vocabulary, resolved at submit
	// time. It is carried because the worker has no request behind it to
	// re-read: without it a queued write to a labelled bank would extract
	// without the labels the synchronous path applies.
	Vocabulary json.RawMessage `json:"vocabulary,omitempty"`
}

// asyncRetainItem is the JSON form of one RetainItem. It exists so the queued
// payload is a stable wire shape of its own rather than the storage struct's
// field names, which are free to change.
type asyncRetainItem struct {
	Content           string              `json:"content"`
	Timestamp         *time.Time          `json:"timestamp,omitempty"`
	HasTimestamp      bool                `json:"has_timestamp,omitempty"`
	Context           string              `json:"context,omitempty"`
	Metadata          map[string]any      `json:"metadata,omitempty"`
	DocumentID        string              `json:"document_id,omitempty"`
	Entities          []asyncRetainEntity `json:"entities,omitempty"`
	ResolveEntities   bool                `json:"resolve_entities,omitempty"`
	Tags              []string            `json:"tags,omitempty"`
	ObservationScopes json.RawMessage     `json:"observation_scopes,omitempty"`
	Strategy          string              `json:"strategy,omitempty"`
	UpdateMode        string              `json:"update_mode,omitempty"`
	ForceReextract    bool                `json:"force_reextract,omitempty"`
	AttachmentIDs     []string            `json:"attachment_ids,omitempty"`
	// AttachmentFilenames are the names the caller gave the item's attachments.
	// They ride the payload so a queued write records them on the document's
	// replayable parameters exactly as a synchronous one does.
	AttachmentFilenames map[string]string `json:"attachment_filenames,omitempty"`
}

type asyncRetainEntity struct {
	Text string `json:"text"`
	Kind string `json:"kind,omitempty"`
}

// AsyncRetainPayload renders the task payload of one queued child operation:
// the request's items plus the settings the worker needs to run them. It is a
// plain map so the store persists it as an ordinary payload on either backend.
func AsyncRetainPayload(bankID, operationID string, req RetainRequest) map[string]any {
	task := asyncRetainTask{
		Type:                   AsyncRetainDefinedType,
		OperationID:            operationID,
		BankID:                 bankID,
		Items:                  make([]asyncRetainItem, 0, len(req.Items)),
		DocumentTags:           req.DocumentTags,
		ExtractionMode:         req.ExtractionMode,
		ExtractionMission:      req.ExtractionMission,
		ExtractionInstructions: req.ExtractionInstructions,
		OptionalFactDimensions: req.OptionalFactDimensions,
		ChunkSize:              req.ChunkSize,
		StructuredChunkSize:    req.StructuredChunkSize,
		DiscardDocumentText:    req.DiscardDocumentText,
	}
	if req.Vocabulary != nil {
		// The struct's own JSON form round-trips exactly, which a re-parse
		// through the config reader would not: that reader expects the raw
		// config shape, not the parsed one.
		if raw, err := json.Marshal(req.Vocabulary); err == nil {
			task.Vocabulary = raw
		}
	}
	for _, item := range req.Items {
		task.Items = append(task.Items, asyncRetainItem{
			Content:             item.Content,
			Timestamp:           item.Timestamp,
			HasTimestamp:        item.HasTimestamp,
			Context:             item.Context,
			Metadata:            item.Metadata,
			DocumentID:          item.DocumentID,
			Entities:            asyncRetainEntities(item.Entities),
			ResolveEntities:     item.ResolveEntities,
			Tags:                item.Tags,
			ObservationScopes:   item.ObservationScopes,
			Strategy:            item.Strategy,
			UpdateMode:          item.UpdateMode,
			ForceReextract:      item.ForceReextract,
			AttachmentIDs:       item.AttachmentIDs,
			AttachmentFilenames: item.AttachmentFilenames,
		})
	}
	// The map round trip is what keeps the payload the store persists free of
	// struct tags it does not know: the worker decodes it back with
	// DecodeAsyncRetainPayload, so the two halves stay in step by construction.
	encoded, err := json.Marshal(task)
	if err != nil {
		return map[string]any{}
	}
	decoded := map[string]any{}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return map[string]any{}
	}
	return decoded
}

// DecodeAsyncRetainPayload rebuilds the write a queued child operation carries.
// The provider-facing boundaries (extraction and embeddings) are not part of the
// payload: the worker supplies them, so a semantic change reaches queued work
// without a schema change.
func DecodeAsyncRetainPayload(payload map[string]any) (RetainRequest, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return RetainRequest{}, err
	}
	var task asyncRetainTask
	if err := json.Unmarshal(raw, &task); err != nil {
		return RetainRequest{}, err
	}
	req := RetainRequest{
		BankID:                 task.BankID,
		OperationID:            task.OperationID,
		DocumentTags:           task.DocumentTags,
		ExtractionMode:         task.ExtractionMode,
		ExtractionMission:      task.ExtractionMission,
		ExtractionInstructions: task.ExtractionInstructions,
		OptionalFactDimensions: task.OptionalFactDimensions,
		ChunkSize:              task.ChunkSize,
		StructuredChunkSize:    task.StructuredChunkSize,
		DiscardDocumentText:    task.DiscardDocumentText,
		// The worker owns the operation record, so the write must not create a
		// second one for itself.
		SkipOperationRecord: true,
	}
	if len(task.Vocabulary) > 0 {
		var vocabulary entitylabels.Vocabulary
		if err := json.Unmarshal(task.Vocabulary, &vocabulary); err == nil {
			req.Vocabulary = &vocabulary
		}
	}
	for _, item := range task.Items {
		req.Items = append(req.Items, RetainItem{
			Content:             item.Content,
			Timestamp:           item.Timestamp,
			HasTimestamp:        item.HasTimestamp,
			Context:             item.Context,
			Metadata:            item.Metadata,
			DocumentID:          item.DocumentID,
			Entities:            retainEntities(item.Entities),
			ResolveEntities:     item.ResolveEntities,
			Tags:                item.Tags,
			ObservationScopes:   item.ObservationScopes,
			Strategy:            item.Strategy,
			UpdateMode:          item.UpdateMode,
			ForceReextract:      item.ForceReextract,
			AttachmentIDs:       item.AttachmentIDs,
			AttachmentFilenames: item.AttachmentFilenames,
		})
	}
	return req, nil
}

func asyncRetainEntities(entities []RetainEntity) []asyncRetainEntity {
	out := make([]asyncRetainEntity, 0, len(entities))
	for _, entity := range entities {
		out = append(out, asyncRetainEntity(entity))
	}
	return out
}

func retainEntities(entities []asyncRetainEntity) []RetainEntity {
	out := make([]RetainEntity, 0, len(entities))
	for _, entity := range entities {
		out = append(out, RetainEntity(entity))
	}
	return out
}

// AsyncRetainSubBatches splits a batch into the children one submission queues.
// A batch that fits one child's token budget stays whole — the overwhelmingly
// common case — and a larger one is split so no single child runs unbounded.
// An item larger than the budget is emitted alone rather than fragmented, and
// items sharing an explicit document id are kept together so two children never
// race on the same document.
func AsyncRetainSubBatches(items []RetainItem, tokensPerBatch int) [][]RetainItem {
	if len(items) == 0 {
		return nil
	}
	if tokensPerBatch <= 0 {
		return [][]RetainItem{items}
	}
	groups := groupByDocumentID(items)
	batches := make([][]RetainItem, 0, len(groups))
	current := make([]RetainItem, 0, len(items))
	currentTokens := 0
	flush := func() {
		if len(current) == 0 {
			return
		}
		batches = append(batches, current)
		current = make([]RetainItem, 0, len(items))
		currentTokens = 0
	}
	for _, group := range groups {
		groupTokens := 0
		for _, item := range group {
			groupTokens += CountTokens(item.Content)
		}
		if len(current) > 0 && currentTokens+groupTokens > tokensPerBatch {
			flush()
		}
		current = append(current, group...)
		currentTokens += groupTokens
	}
	flush()
	return batches
}

// groupByDocumentID groups consecutive items that share a document id, keeping
// items with no explicit document id in their own group: each such item is its
// own document, so it can be split freely.
func groupByDocumentID(items []RetainItem) [][]RetainItem {
	groups := make([][]RetainItem, 0, len(items))
	var currentID string
	hasCurrent := false
	for _, item := range items {
		if !hasCurrent || item.DocumentID == "" || item.DocumentID != currentID {
			groups = append(groups, nil)
			currentID = item.DocumentID
			hasCurrent = true
		}
		groups[len(groups)-1] = append(groups[len(groups)-1], item)
	}
	return groups
}

// BatchParentMetadata renders the parent operation's result metadata: how many
// items and sub-batches it covers, and the flag the listing uses to tell a
// parent from a leaf.
func BatchParentMetadata(itemsCount, numSubBatches int, documentID string) map[string]any {
	metadata := map[string]any{
		"items_count":     itemsCount,
		"num_sub_batches": numSubBatches,
		"is_parent":       true,
	}
	if documentID != "" {
		metadata["document_id"] = documentID
	}
	return metadata
}

// BatchChildMetadata renders one child operation's result metadata, which names
// its parent and its place in the batch so the parent read can roll the batch
// up.
func BatchChildMetadata(parentOperationID string, index, total, itemsCount int, documentID string) map[string]any {
	metadata := map[string]any{
		"parent_operation_id": parentOperationID,
		"sub_batch_index":     index,
		"total_sub_batches":   total,
		"items_count":         itemsCount,
	}
	if documentID != "" {
		metadata["document_id"] = documentID
	}
	return metadata
}

// asyncRetainChild is one child operation a submission queues: the items the
// worker will run, the record's result metadata, the task payload it executes,
// and the serialization key that keeps two children of one document from
// running beside each other.
type asyncRetainChild struct {
	OperationID string
	Items       []RetainItem
	Metadata    map[string]any
	Payload     map[string]any
	// SerializationKey is the document the child targets, or empty when it
	// spans several documents (or none) and so has nothing to be serialized on.
	SerializationKey string
}

// asyncRetainPlan renders the parent and child records one submission queues.
//
// The parent's identity is the caller's when they supplied one, so a retried
// submission rejoins the original rather than duplicating the work; otherwise
// it is freshly minted. The parent carries no task payload: it is a status
// aggregator nothing executes, and its lifecycle is driven entirely by its
// children reaching terminal states. The children run in the caller's stead, in
// item order, each bounded by the token budget.
func asyncRetainPlan(parentID string, req RetainRequest, tokensPerBatch int) (Operation, []asyncRetainChild) {
	batches := AsyncRetainSubBatches(req.Items, tokensPerBatch)
	if len(batches) == 0 {
		// A submission with no items still gets a parent, so the caller has an
		// operation to poll and the batch completes rather than vanishing.
		batches = [][]RetainItem{{}}
	}
	documentID := sharedDocumentID(req.Items)
	parent := Operation{
		OperationID:    parentID,
		BankID:         req.BankID,
		OperationType:  AsyncRetainType,
		Status:         StatusPending,
		ResultMetadata: BatchParentMetadata(len(req.Items), len(batches), documentID),
	}
	children := make([]asyncRetainChild, 0, len(batches))
	for index, items := range batches {
		childID := newUUID()
		childRequest := req
		childRequest.Items = items
		childDocumentID := sharedDocumentID(items)
		children = append(children, asyncRetainChild{
			OperationID:      childID,
			Items:            items,
			Metadata:         BatchChildMetadata(parentID, index+1, len(batches), len(items), childDocumentID),
			Payload:          AsyncRetainPayload(req.BankID, childID, childRequest),
			SerializationKey: childDocumentID,
		})
	}
	return parent, children
}

// sharedDocumentID returns the single document id every item targets, or empty
// when the batch spans more than one document (or any item has none).
func sharedDocumentID(items []RetainItem) string {
	documentID := ""
	for _, item := range items {
		if item.DocumentID == "" {
			return ""
		}
		if documentID == "" {
			documentID = item.DocumentID
			continue
		}
		if item.DocumentID != documentID {
			return ""
		}
	}
	return documentID
}

// summarizeChildErrors renders a parent's error message from its failed
// children: the most common non-empty failed-child message, verbatim, so the
// parent carries a reason a dashboard can attribute rather than a generic
// string. The legacy generic string covers the case where no failed child
// carries a message at all.
func summarizeChildErrors(messages []string) string {
	counts := map[string]int{}
	order := make([]string, 0, len(messages))
	for _, message := range messages {
		message = strings.TrimSpace(message)
		if message == "" {
			continue
		}
		if counts[message] == 0 {
			order = append(order, message)
		}
		counts[message]++
	}
	best := ""
	bestCount := 0
	for _, message := range order {
		if counts[message] > bestCount {
			best, bestCount = message, counts[message]
		}
	}
	if best == "" {
		return "One or more sub-batches failed"
	}
	return best
}
