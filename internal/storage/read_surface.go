package storage

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// ErrChunkNotFound is returned when a chunk identifier is unknown to a bank.
var ErrChunkNotFound = errors.New("chunk not found")

// ErrEntityNotFound is returned when an entity identifier is unknown to a bank.
var ErrEntityNotFound = errors.New("entity not found")

// DocumentQuery pages and filters a document listing.
type DocumentQuery struct {
	BankID string
	// Tags narrows to documents carrying every listed tag.
	Tags []string
	// Query is a case-insensitive substring match on the document text.
	Query  string
	Limit  int
	Offset int
}

// ChunkQuery pages a chunk listing.
type ChunkQuery struct {
	BankID     string
	DocumentID string
	Limit      int
	Offset     int
}

// EntityQuery pages and filters an entity listing.
type EntityQuery struct {
	BankID string
	Kind   EntityKind
	// Query is a case-insensitive substring match on the canonical name.
	Query  string
	Limit  int
	Offset int
}

// DocumentPage is one page of documents with the pre-paging total.
type DocumentPage struct {
	Documents []Document
	Total     int
	Limit     int
	Offset    int
}

// ChunkPage is one page of chunks with the pre-paging total.
type ChunkPage struct {
	Chunks []Chunk
	Total  int
	Limit  int
	Offset int
}

// EntityPage is one page of entities with the pre-paging total.
type EntityPage struct {
	Entities []Entity
	Total    int
	Limit    int
	Offset   int
}

// TagCount is one tag with how many documents and units carry it.
type TagCount struct {
	Tag   string
	Count int
}

// GraphNodeData is the payload of a memory-graph node.
type GraphNodeData struct {
	ID         string
	Text       string
	FactType   string
	Entities   []string
	DocumentID string
	ChunkID    string
	Tags       []string
	Mentioned  *time.Time
	CreatedAt  *time.Time
}

// GraphEdgeData is the payload of a memory-graph edge.
type GraphEdgeData struct {
	ID   string
	From string
	To   string
	Type string
}

// GraphTableRow is one memory rendered as a flat row, so a client can present
// the same data as a table without walking the edges.
type GraphTableRow struct {
	ID            string
	Text          string
	Context       string
	OccurredStart *time.Time
	OccurredEnd   *time.Time
	MentionedAt   *time.Time
	Date          *time.Time
	Entities      string
	DocumentID    string
	ChunkID       string
	FactType      string
	Tags          []string
	CreatedAt     *time.Time
	ProofCount    int
}

// GraphData is the memory graph of one bank.
type GraphData struct {
	Nodes     []GraphNodeData
	Edges     []GraphEdgeData
	TableRows []GraphTableRow
	Total     int
	Limit     int
}

// EntityGraphNode is one node of the entity graph, in the shape the client
// renders directly.
type EntityGraphNode struct {
	ID    string
	Label string
	Color string
}

// EntityGraphEdge is one weighted relation between two entities.
type EntityGraphEdge struct {
	ID     string
	Source string
	Target string
	Weight int
	Color  string
}

// EntityGraph is the entity graph of one bank.
type EntityGraph struct {
	Nodes         []EntityGraphNode
	Edges         []EntityGraphEdge
	TotalEntities int
	TotalEdges    int
	Limit         int
}

// jsonColumn encodes a map for a jsonb column, defaulting to an empty object so
// the column's not-null shape holds.
func jsonColumn(value map[string]any) ([]byte, error) {
	if value == nil {
		return []byte("{}"), nil
	}
	return json.Marshal(value)
}

// decodePostgresTextArray parses a text[] value that arrived as its literal
// form. The driver returns the array textually for a column selected through a
// plain query, so it is decoded here rather than left to a type the scanner
// cannot accept.
func decodePostgresTextArray(raw []byte) []string {
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "{}" || trimmed == "NULL" {
		return nil
	}
	trimmed = strings.TrimPrefix(trimmed, "{")
	trimmed = strings.TrimSuffix(trimmed, "}")
	if trimmed == "" {
		return nil
	}
	parts := splitArrayElements(trimmed)
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, `"`)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

// splitArrayElements splits on commas that are not inside a quoted element, so
// a value containing a comma survives intact.
func splitArrayElements(input string) []string {
	var out []string
	var current strings.Builder
	inQuotes := false
	for i := 0; i < len(input); i++ {
		c := input[i]
		switch {
		case c == '"':
			inQuotes = !inQuotes
			current.WriteByte(c)
		case c == ',' && !inQuotes:
			out = append(out, current.String())
			current.Reset()
		default:
			current.WriteByte(c)
		}
	}
	out = append(out, current.String())
	return out
}
