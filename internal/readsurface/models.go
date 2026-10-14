// Package readsurface serves the document, chunk, tag, entity, and graph route
// families. Field names on these types are public surface.
package readsurface

import (
	"time"

	"github.com/studyzy/wenyuan/internal/storage"
)

// DocumentListItem is one document as the listing renders it.
type DocumentListItem struct {
	ID              string         `json:"id"`
	BankID          string         `json:"bank_id"`
	ContentHash     string         `json:"content_hash,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	TextLength      int            `json:"text_length"`
	MemoryUnitCount int            `json:"memory_unit_count"`
	RetainParams    map[string]any `json:"retain_params,omitempty"`
	Metadata        map[string]any `json:"document_metadata,omitempty"`
	Tags            []string       `json:"tags"`
}

// DocumentListResponse is the document listing envelope.
type DocumentListResponse struct {
	Items  []DocumentListItem `json:"items"`
	Total  int                `json:"total"`
	Limit  int                `json:"limit"`
	Offset int                `json:"offset"`
}

// DocumentResponse is one document's detail.
type DocumentResponse struct {
	ID              string         `json:"id"`
	BankID          string         `json:"bank_id"`
	OriginalText    string         `json:"original_text"`
	ContentHash     string         `json:"content_hash,omitempty"`
	CreatedAt       string         `json:"created_at"`
	UpdatedAt       string         `json:"updated_at"`
	MemoryUnitCount int            `json:"memory_unit_count"`
	Tags            []string       `json:"tags"`
	Metadata        map[string]any `json:"document_metadata,omitempty"`
	RetainParams    map[string]any `json:"retain_params,omitempty"`
}

// UpdateDocumentRequest is the partial-update body. A nil field is unchanged.
type UpdateDocumentRequest struct {
	Tags     *[]string       `json:"tags"`
	Metadata *map[string]any `json:"document_metadata"`
}

// UpdateDocumentResponse is the update result.
type UpdateDocumentResponse struct {
	Success bool `json:"success"`
}

// ChunkResponse is one chunk.
type ChunkResponse struct {
	ChunkID    string `json:"chunk_id"`
	DocumentID string `json:"document_id"`
	BankID     string `json:"bank_id"`
	ChunkIndex int    `json:"chunk_index"`
	ChunkText  string `json:"chunk_text"`
	CreatedAt  string `json:"created_at"`
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

// EntityListItem is one entity as the listing renders it.
type EntityListItem struct {
	ID            string         `json:"id"`
	CanonicalName string         `json:"canonical_name"`
	MentionCount  int            `json:"mention_count"`
	FirstSeen     *string        `json:"first_seen"`
	LastSeen      *string        `json:"last_seen"`
	Metadata      map[string]any `json:"metadata,omitempty"`
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

// EntityDetailResponse is one entity's detail.
type EntityDetailResponse struct {
	ID            string              `json:"id"`
	CanonicalName string              `json:"canonical_name"`
	MentionCount  int                 `json:"mention_count"`
	FirstSeen     *string             `json:"first_seen"`
	LastSeen      *string             `json:"last_seen"`
	Metadata      map[string]any      `json:"metadata,omitempty"`
	Observations  []EntityObservation `json:"observations"`
}

// GraphNodeData is the payload of a memory-graph node.
type GraphNodeData struct {
	ID         string   `json:"id"`
	Text       string   `json:"text"`
	FactType   string   `json:"fact_type,omitempty"`
	Entities   []string `json:"entities,omitempty"`
	DocumentID string   `json:"document_id,omitempty"`
	ChunkID    string   `json:"chunk_id,omitempty"`
	Tags       []string `json:"tags,omitempty"`
}

// GraphNode wraps a node payload, matching the shape clients render.
type GraphNode struct {
	Data GraphNodeData `json:"data"`
}

// GraphEdgeData is the payload of a memory-graph edge.
type GraphEdgeData struct {
	ID   string `json:"id"`
	From string `json:"from"`
	To   string `json:"to"`
	Type string `json:"type,omitempty"`
}

// GraphEdge wraps an edge payload.
type GraphEdge struct {
	Data GraphEdgeData `json:"data"`
}

// GraphTableRow is one memory as a flat row.
type GraphTableRow struct {
	ID            string   `json:"id"`
	Text          string   `json:"text"`
	Context       string   `json:"context,omitempty"`
	OccurredStart *string  `json:"occurred_start"`
	OccurredEnd   *string  `json:"occurred_end"`
	MentionedAt   *string  `json:"mentioned_at"`
	Date          *string  `json:"date"`
	Entities      string   `json:"entities"`
	DocumentID    string   `json:"document_id,omitempty"`
	ChunkID       string   `json:"chunk_id,omitempty"`
	FactType      string   `json:"fact_type,omitempty"`
	Tags          []string `json:"tags"`
	ProofCount    int      `json:"proof_count"`
}

// GraphResponse is the memory graph.
type GraphResponse struct {
	Nodes      []GraphNode     `json:"nodes"`
	Edges      []GraphEdge     `json:"edges"`
	TableRows  []GraphTableRow `json:"table_rows"`
	TotalUnits int             `json:"total_units"`
	Limit      int             `json:"limit"`
}

// EntityGraphNodeData is one entity-graph node.
type EntityGraphNodeData struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Color string `json:"color,omitempty"`
}

// EntityGraphNode wraps an entity-graph node.
type EntityGraphNode struct {
	Data EntityGraphNodeData `json:"data"`
}

// EntityGraphEdgeData is one entity-graph relation.
type EntityGraphEdgeData struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Target string `json:"target"`
	Weight int    `json:"weight"`
	Color  string `json:"color,omitempty"`
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

// formatTime renders a timestamp the way this surface reports it. A zero time
// renders as the empty string so a client never sees a placeholder date.
func formatTime(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339)
}

// formatTimePtr renders an optional timestamp.
func formatTimePtr(value *time.Time) *string {
	if value == nil || value.IsZero() {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339)
	return &formatted
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
		ContentHash:     doc.ContentHash,
		CreatedAt:       formatTime(doc.CreatedAt),
		UpdatedAt:       formatTime(doc.UpdatedAt),
		TextLength:      len(doc.OriginalText),
		MemoryUnitCount: doc.MemoryUnitCount,
		RetainParams:    doc.RetainParams,
		Metadata:        doc.Metadata,
		Tags:            tags,
	}
}

// toChunkResponse renders a stored chunk.
func toChunkResponse(chunk storage.Chunk) ChunkResponse {
	return ChunkResponse{
		ChunkID:    chunk.ChunkID,
		DocumentID: chunk.DocumentID,
		BankID:     chunk.BankID,
		ChunkIndex: chunk.ChunkIndex,
		ChunkText:  chunk.ChunkText,
		CreatedAt:  formatTime(chunk.CreatedAt),
	}
}

// toEntityListItem renders a stored entity.
func toEntityListItem(entity storage.Entity) EntityListItem {
	return EntityListItem{
		ID:            entity.ID,
		CanonicalName: entity.CanonicalName,
		MentionCount:  entity.MentionCount,
		FirstSeen:     formatTimePtr(entity.FirstSeen),
		LastSeen:      formatTimePtr(entity.LastSeen),
		Metadata:      entity.Metadata,
	}
}
