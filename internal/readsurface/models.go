// Package readsurface serves the document, chunk, tag, entity, and graph route
// families. Field names on these types are public surface.
package readsurface

import (
	"unicode/utf8"

	"github.com/studyzy/wenyuan/internal/httpx"
	"github.com/studyzy/wenyuan/internal/storage"
)

// DocumentListItem is one document as the listing renders it.
type DocumentListItem struct {
	ID              string         `json:"id"`
	BankID          string         `json:"bank_id"`
	ContentHash     *string        `json:"content_hash"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	TextLength      int            `json:"text_length"`
	MemoryUnitCount int            `json:"memory_unit_count"`
	RetainParams    map[string]any `json:"retain_params"`
	Metadata        any            `json:"document_metadata"`
	Tags            []string       `json:"tags"`
}

// DocumentListResponse is the document listing envelope.
type DocumentListResponse struct {
	Items  []DocumentListItem `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

// ChunkAttachment is one attachment a document or chunk text references, in the
// shape every read surface returns it: the placeholder id, the full content
// hash, how to classify and fetch the bytes, and the bank-scoped URL that
// serves them.
type ChunkAttachment struct {
	ID        string `json:"id"`
	Hash      string `json:"hash"`
	Kind      string `json:"kind"`
	MediaType string `json:"media_type"`
	ByteSize  int64  `json:"byte_size"`
	// Filename is the original name the caller gave the attachment, or null
	// when it named none: the reference's attachment model keeps the key with a
	// null value rather than dropping it.
	Filename *string `json:"filename"`
	URL      string  `json:"url"`
}

// DocumentResponse is one document's detail.
type DocumentResponse struct {
	ID     string `json:"id"`
	BankID string `json:"bank_id"`
	// OriginalText is the stored source text, or null when the deployment opted
	// out of keeping it (store_document_text disabled) or when it was never
	// stored.
	OriginalText    *string        `json:"original_text"`
	ContentHash     *string        `json:"content_hash"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	MemoryUnitCount int            `json:"memory_unit_count"`
	Tags            []string       `json:"tags"`
	Metadata        any            `json:"document_metadata"`
	RetainParams    map[string]any `json:"retain_params"`
	NodesByFactType map[string]int `json:"nodes_by_fact_type"`
	// ObservationScopes is the scope spec configured at retain time: a mode
	// name or a list of tag groups, so it stays the shape the caller sent.
	ObservationScopes any               `json:"observation_scopes"`
	Attachments       []ChunkAttachment `json:"attachments"`
}

// UpdateDocumentRequest is the partial-update body. `tags` is the only mutable
// field, and it must be present; any other body field is ignored.
type UpdateDocumentRequest struct {
	Tags *[]string `json:"tags"`
}

// UpdateDocumentResponse is the update result.
type UpdateDocumentResponse struct {
	Success bool `json:"success"`
}

// DeleteDocumentResponse reports the cascade a document delete performed.
type DeleteDocumentResponse struct {
	Success            bool   `json:"success"`
	Message            string `json:"message"`
	DocumentID         string `json:"document_id"`
	MemoryUnitsDeleted int    `json:"memory_units_deleted"`
}

// ChunkResponse is one chunk.
type ChunkResponse struct {
	ChunkID    string `json:"chunk_id"`
	DocumentID string `json:"document_id"`
	BankID     string `json:"bank_id"`
	ChunkIndex int    `json:"chunk_index"`
	ChunkText  string `json:"chunk_text"`
	CreatedAt  string `json:"created_at"`
	// Attachments is null, not absent, when the chunk's text references none:
	// the reference's chunk model always carries the key.
	Attachments []ChunkAttachment `json:"attachments"`
}

// ChunkListResponse is the chunk listing envelope.
type ChunkListResponse struct {
	Items  []ChunkResponse `json:"items"`
	Total  int             `json:"total"`
	Limit  int             `json:"limit"`
	Offset int             `json:"offset"`
}

// ReprocessDocumentResponse is the reprocess result.
type ReprocessDocumentResponse struct {
	Success     bool   `json:"success"`
	OperationID string `json:"operation_id"`
	ItemsCount  int    `json:"items_count"`
}

// TagItem is one tag with its usage count.
type TagItem struct {
	Tag   string `json:"tag"`
	Count int    `json:"count"`
}

// TagListResponse is the tag listing envelope.
type TagListResponse struct {
	Items  []TagItem `json:"items"`
	Total  int       `json:"total"`
	Limit  int       `json:"limit"`
	Offset int       `json:"offset"`
}

// EntityListItem is one entity as the listing renders it. `metadata` is always
// present, an empty object when the entity carries none.
type EntityListItem struct {
	ID            string         `json:"id"`
	CanonicalName string         `json:"canonical_name"`
	MentionCount  int            `json:"mention_count"`
	FirstSeen     *string        `json:"first_seen"`
	LastSeen      *string        `json:"last_seen"`
	Metadata      map[string]any `json:"metadata"`
}

// EntityListResponse is the entity listing envelope.
type EntityListResponse struct {
	Items  []EntityListItem `json:"items"`
	Total  int              `json:"total"`
	Limit  int              `json:"limit"`
	Offset int              `json:"offset"`
}

// EntityObservation is one observation attached to an entity.
type EntityObservation struct {
	Text        string  `json:"text"`
	MentionedAt *string `json:"mentioned_at"`
}

// EntityDetailResponse is one entity's detail. `metadata` is always present (an
// empty object when the entity carries none) and `observations` is always an
// empty list: entity observations were retired in favour of mental models.
type EntityDetailResponse struct {
	ID            string              `json:"id"`
	CanonicalName string              `json:"canonical_name"`
	MentionCount  int                 `json:"mention_count"`
	FirstSeen     *string             `json:"first_seen"`
	LastSeen      *string             `json:"last_seen"`
	Metadata      map[string]any      `json:"metadata"`
	Observations  []EntityObservation `json:"observations"`
}

// GraphNodeData is the payload of a memory-graph node. Every key is always
// present: the reference's graph models are row models, so its route keeps nulls
// rather than dropping absent fields.
type GraphNodeData struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Text     string `json:"text"`
	Date     string `json:"date"`
	Context  string `json:"context"`
	Entities string `json:"entities"`
	Color    string `json:"color"`
}

// GraphNode wraps a node payload, matching the shape clients render.
type GraphNode struct {
	Data GraphNodeData `json:"data"`
}

// GraphEdgeData is the payload of a memory-graph edge. Every key is always
// present.
type GraphEdgeData struct {
	ID       string `json:"id"`
	Source   string `json:"source"`
	Target   string `json:"target"`
	LinkType string `json:"linkType"`
	// The reference models this as a Python float, so a whole weight renders
	// `1.0` — the entity graph's is an int and stays `int`.
	Weight     httpx.PythonFloat `json:"weight"`
	EntityName string            `json:"entityName"`
	Color      string            `json:"color"`
	LineStyle  string            `json:"lineStyle"`
}

// GraphEdge wraps an edge payload.
type GraphEdge struct {
	Data GraphEdgeData `json:"data"`
}

// GraphTableRow is one memory as a flat row. Every key is always present; an
// absent value is a JSON null.
type GraphTableRow struct {
	ID            string   `json:"id"`
	Text          string   `json:"text"`
	Context       string   `json:"context"`
	OccurredStart *string  `json:"occurred_start"`
	OccurredEnd   *string  `json:"occurred_end"`
	MentionedAt   *string  `json:"mentioned_at"`
	Date          string   `json:"date"`
	Entities      string   `json:"entities"`
	DocumentID    *string  `json:"document_id"`
	ChunkID       *string  `json:"chunk_id"`
	FactType      *string  `json:"fact_type"`
	Tags          []string `json:"tags"`
	CreatedAt     *string  `json:"created_at"`
	ProofCount    *int     `json:"proof_count"`
}

// GraphResponse is the memory graph.
type GraphResponse struct {
	Nodes      []GraphNode     `json:"nodes"`
	Edges      []GraphEdge     `json:"edges"`
	TableRows  []GraphTableRow `json:"table_rows"`
	TotalUnits int             `json:"total_units"`
	Limit      int             `json:"limit"`
}

// EntityGraphNodeData is one entity-graph node. The member order is the
// reference model's, which is observable: a response's keys come back in it.
type EntityGraphNodeData struct {
	ID           string `json:"id"`
	Label        string `json:"label"`
	MentionCount int    `json:"mentionCount"`
	Color        string `json:"color,omitempty"`
}

// EntityGraphNode wraps an entity-graph node.
type EntityGraphNode struct {
	Data EntityGraphNodeData `json:"data"`
}

// EntityGraphEdgeData is one entity-graph relation, in the reference model's
// member order.
type EntityGraphEdgeData struct {
	ID             string  `json:"id"`
	Source         string  `json:"source"`
	Target         string  `json:"target"`
	LinkType       string  `json:"linkType,omitempty"`
	Weight         int     `json:"weight"`
	Color          string  `json:"color,omitempty"`
	LineStyle      string  `json:"lineStyle,omitempty"`
	LastCooccurred *string `json:"lastCooccurred"`
}

// EntityGraphEdge wraps an entity-graph relation.
type EntityGraphEdge struct {
	Data EntityGraphEdgeData `json:"data"`
}

// EntityGraphResponse is the entity graph.
type EntityGraphResponse struct {
	Nodes         []EntityGraphNode `json:"nodes"`
	Edges         []EntityGraphEdge `json:"edges"`
	TotalEntities int               `json:"total_entities"`
	TotalEdges    int               `json:"total_edges"`
	Limit         int               `json:"limit"`
}

// toDocumentListItem renders a stored document for the listing.
func toDocumentListItem(doc storage.Document) DocumentListItem {
	tags := doc.Tags
	if tags == nil {
		tags = []string{}
	}
	return DocumentListItem{
		ID:              doc.ID,
		BankID:          doc.BankID,
		ContentHash:     optionalString(doc.ContentHash),
		CreatedAt:       httpx.FormatTimestamp(doc.CreatedAt),
		UpdatedAt:       httpx.FormatTimestamp(doc.UpdatedAt),
		TextLength:      utf8.RuneCountInString(doc.OriginalText),
		MemoryUnitCount: doc.MemoryUnitCount,
		RetainParams:    presentRetainParams(doc.RetainParams),
		Metadata:        doc.Metadata,
		Tags:            tags,
	}
}

// toChunkResponse renders a stored chunk with the attachments its units were
// drawn from.
func toChunkResponse(chunk storage.Chunk, attachments []storage.Attachment) ChunkResponse {
	return ChunkResponse{
		ChunkID:     chunk.ChunkID,
		DocumentID:  chunk.DocumentID,
		BankID:      chunk.BankID,
		ChunkIndex:  chunk.ChunkIndex,
		ChunkText:   chunk.ChunkText,
		CreatedAt:   httpx.FormatTimestamp(chunk.CreatedAt),
		Attachments: toChunkAttachments(chunk.BankID, attachments),
	}
}

// toChunkAttachments renders stored attachments in the shape the read surface
// returns them, including the bank-scoped URL that serves each one's bytes.
func toChunkAttachments(bankID string, attachments []storage.Attachment) []ChunkAttachment {
	if len(attachments) == 0 {
		return nil
	}
	out := make([]ChunkAttachment, 0, len(attachments))
	for _, attachment := range attachments {
		out = append(out, chunkAttachment(bankID, attachment))
	}
	return out
}

// chunkAttachment renders one stored attachment.
func chunkAttachment(bankID string, attachment storage.Attachment) ChunkAttachment {
	return ChunkAttachment{
		ID:        attachment.ShortID,
		Hash:      attachment.AttachmentHash,
		Kind:      attachment.Kind,
		MediaType: attachment.MediaType,
		ByteSize:  attachment.ByteSize,
		Filename:  optionalString(attachment.Filename),
		URL:       bankAttachmentURL(bankID, attachment.ShortID),
	}
}

// presentRetainParams renders a document's retain parameters the way the
// reference does: the recorded object, or null when the write recorded nothing
// to replay. The reference builds the field as `retain_params_parsed or None`,
// so an empty object reads back as a JSON null rather than `{}`.
func presentRetainParams(params map[string]any) map[string]any {
	if len(params) == 0 {
		return nil
	}
	return params
}

// optionalString renders a possibly-empty string as a nullable field, so an
// absent value is a JSON null rather than an empty string.
func optionalString(value string) *string {
	if value == "" {
		return nil
	}
	return &value
}

// toEntityListItem renders a stored entity. `metadata` is always an object on
// the wire, so an absent value becomes an empty one.
func toEntityListItem(entity storage.Entity) EntityListItem {
	metadata := entity.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	return EntityListItem{
		ID:            entity.ID,
		CanonicalName: entity.CanonicalName,
		MentionCount:  entity.MentionCount,
		FirstSeen:     httpx.FormatTimestampPtr(entity.FirstSeen),
		LastSeen:      httpx.FormatTimestampPtr(entity.LastSeen),
		Metadata:      metadata,
	}
}
